package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type crdAPI struct {
	requests    []*api.CustomResourceDefinition
	mu          sync.Mutex
	live        *api.CustomResourceDefinition
	calls       []string
	discovery   int
	conflicts   int
	failPut     bool
	keepDeleted bool
	deletion    metav1.DeleteOptions
}

func (a *crdAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	a.calls = append(a.calls, r.Method+" "+r.URL.Path)
	var out any
	switch {
	case r.URL.Path == "/apis/example.test/v1":
		a.discovery++
		resources := []metav1.APIResource{}
		if a.discovery > 1 {
			resources = append(resources, metav1.APIResource{
				Name: "widgets", Kind: "Widget", Namespaced: true,
			})
		}
		out = &metav1.APIResourceList{GroupVersion: "example.test/v1", APIResources: resources}
	case r.URL.Path == "/api":
		out = &metav1.APIVersions{Versions: []string{"v1"}}
	case r.URL.Path == "/apis":
		out = &metav1.APIGroupList{Groups: []metav1.APIGroup{{
			Name: "apiextensions.k8s.io",
			Versions: []metav1.GroupVersionForDiscovery{{
				GroupVersion: "apiextensions.k8s.io/v1", Version: "v1",
			}},
			PreferredVersion: metav1.GroupVersionForDiscovery{
				GroupVersion: "apiextensions.k8s.io/v1", Version: "v1",
			},
		}}}
	case r.URL.Path == "/apis/apiextensions.k8s.io/v1":
		out = &metav1.APIResourceList{
			GroupVersion: "apiextensions.k8s.io/v1",
			APIResources: []metav1.APIResource{{
				Name: "customresourcedefinitions", Kind: "CustomResourceDefinition",
			}},
		}
	case r.URL.Path == "/api/v1":
		out = &metav1.APIResourceList{GroupVersion: "v1"}
	case r.Method == http.MethodPost || r.Method == http.MethodPut:
		var body api.CustomResourceDefinition
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a.requests = append(a.requests, body.DeepCopy())
		if r.Method == http.MethodPut && (a.conflicts > 0 || a.failPut) {
			a.conflicts--
			code, reason := 409, metav1.StatusReasonConflict
			if a.failPut {
				code, reason = 422, metav1.StatusReasonInvalid
			}
			w.WriteHeader(code)
			out = &metav1.Status{Status: "Failure", Code: int32(code), Reason: reason}
			break
		}
		body.UID, body.ResourceVersion = "uid-1", "42"
		body.Status.Conditions = []api.CustomResourceDefinitionCondition{
			{Type: api.Established, Status: api.ConditionTrue},
		}
		a.live = &body
		out = a.live
	case r.Method == http.MethodDelete:
		_ = json.NewDecoder(r.Body).Decode(&a.deletion)
		if !a.keepDeleted {
			a.live = nil
		}
		out = &metav1.Status{Status: "Success"}
	case r.Method == http.MethodGet:
		if a.live == nil {
			w.WriteHeader(404)
			out = &metav1.Status{Status: "Failure", Code: 404, Reason: metav1.StatusReasonNotFound}
		} else {
			out = a.live
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func crdServer(t *testing.T) (*crdAPI, *config.Configuration) {
	t.Helper()
	a := &crdAPI{}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(server.Close)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	return a, &config.Configuration{Server: &server.URL, Namespace: ptr.To("ignored")}
}

func TestCRDCreateDiscoveryReadAndUpdate(t *testing.T) {
	a, cfg := crdServer(t)
	r := desiredCRD()
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, "uid-1", out.UID)
	require.Equal(t, "widget", *out.Spec.Names.Singular)
	a.mu.Lock()
	require.Equal(t, 2, a.discovery)
	require.Equal(t, "POST /apis/apiextensions.k8s.io/v1/customresourcedefinitions", a.calls[0])
	a.calls = nil
	a.live.Labels = map[string]string{"drift": "true"}
	a.live.Spec.Versions[0].Schema = &api.CustomResourceValidation{
		OpenAPIV3Schema: &api.JSONSchemaProps{Type: "object"},
	}
	a.live.Finalizers = []string{"preserve"}
	a.live.OwnerReferences = []metav1.OwnerReference{{Name: "owner", UID: "owner-uid"}}
	a.conflicts = 1
	wantUpdate := a.live.DeepCopy()
	prepared, err := prepareCRD(r)
	require.NoError(t, err)
	wantUpdate.Spec = prepared.Spec
	wantUpdate.Labels, wantUpdate.Annotations = nil, nil
	a.mu.Unlock()
	p := crdPrior{Inputs: r, Outputs: out}
	read, err := r.Read(t.Context(), cfg, p)
	require.NoError(t, err)
	require.Equal(t, &map[string]string{"drift": "true"}, read.Metadata.Labels)
	require.Equal(t, &map[string]any{"type": "object"}, read.Spec.Versions[0].Schema)
	a.mu.Lock()
	require.Equal(t, []string{
		"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
	}, a.calls)
	a.mu.Unlock()

	r.WaitForEstablished = ptr.To(false)
	updated, err := r.Update(t.Context(), cfg, p)
	require.NoError(t, err)
	require.Equal(t, out.UID, updated.UID)
	a.mu.Lock()
	require.Equal(t, wantUpdate, a.requests[len(a.requests)-1])
	require.Nil(t, a.live.Labels)
	require.Nil(t, a.live.Spec.Versions[0].Schema)
	require.Equal(t, []string{"preserve"}, a.live.Finalizers)
	require.Equal(t, []metav1.OwnerReference{{Name: "owner", UID: "owner-uid"}},
		a.live.OwnerReferences)
	require.Equal(t, "42", a.live.ResourceVersion)
	a.mu.Unlock()
}

func TestCRDSavedIdentityAndDeleteHistorical(t *testing.T) {
	a, cfg := crdServer(t)
	r := desiredCRD()
	r.WaitForEstablished = ptr.To(false)
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	changed := &config.Configuration{Server: ptr.To("http://127.0.0.1:1")}
	p := crdPrior{Inputs: r, Outputs: out}
	_, err = r.Read(t.Context(), changed, p)
	require.NoError(t, err)
	a.mu.Lock()
	a.live.UID = "replacement"
	a.mu.Unlock()
	_, err = r.Update(t.Context(), cfg, p)
	require.ErrorContains(t, err, "UID changed")
	a.mu.Lock()
	a.live.UID = types.UID(out.UID)
	a.mu.Unlock()
	p.Inputs.Spec.Versions[0].Schema = &map[string]any{"invalid": make(chan int)}
	require.NoError(t, r.Delete(t.Context(), changed, p))
	a.mu.Lock()
	require.Equal(t, new(types.UID(out.UID)), a.deletion.Preconditions.UID)
	require.Nil(t, a.live)
	a.mu.Unlock()
}

func TestCRDReadCancellation(t *testing.T) {
	_, cfg := crdServer(t)
	r := desiredCRD()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Create(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
}

func TestCRDReadRequiresSavedIdentity(t *testing.T) {
	_, cfg := crdServer(t)
	r := desiredCRD()
	_, err := r.Read(t.Context(), cfg, crdPrior{})
	require.ErrorContains(t, err, "missing CRD identity")
}

func TestCRDDeleteDoesNotDeleteReplacement(t *testing.T) {
	a, cfg := crdServer(t)
	r := desiredCRD()
	r.WaitForEstablished = ptr.To(false)
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	a.mu.Lock()
	a.live.UID = "replacement"
	a.calls = nil
	a.mu.Unlock()
	require.NoError(t, r.Delete(t.Context(), cfg, crdPrior{Inputs: r, Outputs: out}))
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Equal(t, types.UID("replacement"), a.live.UID)
	for _, call := range a.calls {
		require.NotContains(t, call, "DELETE")
	}
}

func TestCRDUpdateErrorsAndFiniteConflictRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		conflicts int
		invalid   bool
		puts      int
	}{
		{name: "incompatible schema", invalid: true, puts: 1},
		{name: "finite conflicts", conflicts: 100, puts: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, cfg := crdServer(t)
			r := desiredCRD()
			r.WaitForEstablished = ptr.To(false)
			out, err := r.Create(t.Context(), cfg)
			require.NoError(t, err)
			a.mu.Lock()
			a.calls = nil
			a.conflicts, a.failPut = tc.conflicts, tc.invalid
			a.live.Status.StoredVersions = []string{"v1"}
			a.mu.Unlock()
			_, err = r.Update(t.Context(), cfg, crdPrior{Inputs: r, Outputs: out})
			require.Error(t, err)
			if tc.invalid {
				require.True(t, apierrors.IsInvalid(err))
			} else {
				require.True(t, apierrors.IsConflict(err))
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			var want []string
			for range tc.puts {
				want = append(want,
					"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
					"PUT /apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test")
			}
			require.Equal(t, want, a.calls)
			require.Equal(t, []string{"v1"}, a.live.Status.StoredVersions)
		})
	}
}

func TestCRDCreateRefusesAdoption(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(&metav1.Status{
			Status: "Failure", Code: 409, Reason: metav1.StatusReasonAlreadyExists,
		})
	}))
	defer server.Close()
	r := desiredCRD()
	_, err := r.Create(t.Context(), &config.Configuration{Server: &server.URL})
	require.True(t, apierrors.IsAlreadyExists(err))
	require.Equal(t, []string{"POST"}, calls)
}

func TestCRDDeleteWaitsForFinalizers(t *testing.T) {
	a, cfg := crdServer(t)
	r := desiredCRD()
	r.WaitForEstablished = ptr.To(false)
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	a.mu.Lock()
	a.keepDeleted = true
	a.live.Finalizers = []string{"controller.example.test/cleanup"}
	a.mu.Unlock()
	r.DeleteTimeout = ptr.To("100ms")
	err = r.Delete(t.Context(), cfg, crdPrior{Inputs: r, Outputs: out})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Equal(t, []string{"controller.example.test/cleanup"}, a.live.Finalizers)
	require.Equal(t, ptr.To(types.UID(out.UID)), a.deletion.Preconditions.UID)
}

func TestCRDSavedServerUsesCurrentCredentials(t *testing.T) {
	a := &crdAPI{}
	credentials := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credentials <- r.Header.Get("Authorization")
		a.serve(w, r)
	}))
	defer server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	cfg := &config.Configuration{
		Server: &server.URL, Token: ptr.To("initial"), InsecureSkipTLSVerify: ptr.To(true),
	}
	r := desiredCRD()
	r.WaitForEstablished = ptr.To(false)
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	cfg.Server, cfg.Token = ptr.To("https://127.0.0.1:1"), ptr.To("current")
	_, err = r.Read(t.Context(), cfg, crdPrior{Inputs: r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, "Bearer initial", <-credentials)
	require.Equal(t, "Bearer current", <-credentials)
}

func TestCRDDeleteHandlesReplacementRace(t *testing.T) {
	a := &crdAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodDelete {
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, ptr.To(types.UID("uid-1")), options.Preconditions.UID)
			a.mu.Lock()
			a.live.UID = "replacement"
			a.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(&metav1.Status{
				Status: "Failure", Code: 409, Reason: metav1.StatusReasonConflict,
			})
			return
		}
		a.serve(w, req)
	}))
	defer server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	cfg := &config.Configuration{Server: &server.URL}
	r := desiredCRD()
	r.WaitForEstablished = ptr.To(false)
	out, err := r.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.NoError(t, r.Delete(t.Context(), cfg, crdPrior{Inputs: r, Outputs: out}))
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Equal(t, types.UID("replacement"), a.live.UID)
}

func TestCRDCreateDeadlineIncludesDiscovery(t *testing.T) {
	a := &crdAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/apis/example.test/v1" {
			<-req.Context().Done()
			return
		}
		a.serve(w, req)
	}))
	defer server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	r := desiredCRD()
	r.CreateTimeout = ptr.To("100ms")
	_, err := r.Create(t.Context(), &config.Configuration{Server: &server.URL})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	a.mu.Lock()
	defer a.mu.Unlock()
	require.NotNil(t, a.live)
	require.Equal(t, []string{
		"POST /apis/apiextensions.k8s.io/v1/customresourcedefinitions",
		"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
	}, a.calls)
}
