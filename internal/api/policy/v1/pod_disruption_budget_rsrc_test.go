package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type pdbServer struct {
	object    *policyv1.PodDisruptionBudget
	calls     []string
	paths     []string
	patch     []pdbPatchOperation
	deleted   metav1.DeleteOptions
	failure   int
	creates   int
	finalizer bool
}

func (s *pdbServer) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	s.calls = append(s.calls, r.Method)
	s.paths = append(s.paths, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	respond := func(value any) { require.NoError(t, json.NewEncoder(w).Encode(value)) }
	fail := func(code int, reason metav1.StatusReason) {
		w.WriteHeader(code)
		respond(metav1.Status{Status: "Failure", Code: int32(code), Reason: reason})
	}
	if s.failure != 0 {
		reason := metav1.StatusReasonForbidden
		if s.failure == 404 {
			reason = metav1.StatusReasonNotFound
		}
		fail(s.failure, reason)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.object = &policyv1.PodDisruptionBudget{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(s.object))
		if s.object.Name == "" {
			s.object.Name = s.object.GenerateName + "generated"
		}
		s.creates++
		s.object.UID = types.UID(fmt.Sprintf("uid-%d", s.creates))
	case http.MethodGet:
		if s.object == nil {
			fail(404, metav1.StatusReasonNotFound)
			return
		}
	case http.MethodPatch:
		require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&s.patch))
		body, err := json.Marshal(s.patch)
		require.NoError(t, err)
		patch, err := jsonpatch.DecodePatch(body)
		require.NoError(t, err)
		body, err = json.Marshal(s.object)
		require.NoError(t, err)
		body, err = patch.Apply(body)
		if err != nil {
			fail(409, metav1.StatusReasonConflict)
			return
		}
		s.object = &policyv1.PodDisruptionBudget{}
		require.NoError(t, json.Unmarshal(body, s.object))
	case http.MethodDelete:
		require.NoError(t, json.NewDecoder(r.Body).Decode(&s.deleted))
		if s.object != nil && *s.deleted.Preconditions.UID != s.object.UID {
			fail(409, metav1.StatusReasonConflict)
			return
		}
		if !s.finalizer {
			s.object = nil
		}
		respond(metav1.Status{Status: "Success"})
		return
	default:
		t.Errorf("unexpected request %s", r.Method)
	}
	respond(s.object)
}

func pdbAPI(t *testing.T, state *pdbServer) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.serve(t, w, r)
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, Namespace: new("config-team"),
		InsecureSkipTLSVerify: new(true)}
}

func validPDB() *PodDisruptionBudgetResource {
	return &PodDisruptionBudgetResource{Metadata: PodDisruptionBudgetMetadata{
		GenerateName: new("pdb-")}, Spec: PodDisruptionBudgetSpec{MinAvailable: new("+01")}}
}

func TestPDBLifecycle(t *testing.T) {
	s := &pdbServer{}
	c := pdbAPI(t, s)
	r := validPDB()
	r.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/Key": ""}
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, s.calls)
	require.Equal(t, []string{"/apis/policy/v1/namespaces/default/poddisruptionbudgets",
		"/apis/policy/v1/namespaces/default/poddisruptionbudgets/pdb-generated"}, s.paths)
	require.Equal(t, "1", created.Spec.MinAvailable)
	require.Equal(t, int32(1), s.object.Spec.MinAvailable.IntVal)
	require.NotNil(t, s.object.Spec.Selector)
	require.Equal(t, "pdb-generated", created.Name)
	require.Contains(t, s.object.Annotations, "EXAMPLE.COM/Key")
	p := pdbPrior{Inputs: *r, Outputs: created}
	s.object.Annotations["controller.kubernetes.io/hidden"] = "keep"
	s.object.Annotations["ignored/key"] = "hidden"
	s.object.Annotations["app.kubernetes.io/name"] = "shown"
	r.IgnoreAnnotations = &[]string{"^ignored/"}
	read, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"EXAMPLE.COM/Key": "", "app.kubernetes.io/name": "shown"},
		read.Metadata.Annotations)
	p.Observed = read
	r.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/Key": "changed", "a~/key": ""}
	updated, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, "keep", s.object.Annotations["controller.kubernetes.io/hidden"])
	require.Equal(t, []pdbPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: created.UID},
		{Op: "replace", Path: "/metadata/annotations/EXAMPLE.COM~1Key", Value: "changed"},
		{Op: "remove", Path: "/metadata/annotations/app.kubernetes.io~1name"},
		{Op: "add", Path: "/metadata/annotations/a~0~1key", Value: ""},
	}, s.patch)
	require.Equal(t, []string{"PATCH", "GET"}, s.calls[len(s.calls)-2:])
	p.Outputs, p.Observed = updated, nil
	s.finalizer = true
	before := len(s.calls)
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, []string{"DELETE"}, s.calls[before:])
	require.NotNil(t, s.object)
	require.Equal(t, created.UID, string(*s.deleted.Preconditions.UID))
	require.Nil(t, s.deleted.PropagationPolicy)
	require.Nil(t, s.deleted.GracePeriodSeconds)
}

func TestPDBAvailabilityAndSelector(t *testing.T) {
	for _, test := range []struct{ input, output string }{
		{"", ""}, {"-1", "-1"}, {"+01", "1"}, {"-2147483648", "-2147483648"},
		{"01%", "01%"}, {"+01%", "+01%"}, {"-0%", "-0%"},
	} {
		t.Run(test.input, func(t *testing.T) {
			r := validPDB()
			r.Spec.MinAvailable, r.Spec.MaxUnavailable = &test.input, &test.input
			r.Spec.Selector.MatchExpressions = &[]PodDisruptionBudgetExpression{
				{Key: new("second"), Operator: new("API-validates"), Values: &[]string{"z", "a", "z"}}, {},
			}
			spec, err := nativePDBSpec(r.Spec)
			require.NoError(t, err)
			require.Equal(t, test.output, projectPDBSpec(spec).MinAvailable)
			require.Equal(t, test.output, projectPDBSpec(spec).MaxUnavailable)
			require.Equal(t, []string{"a", "z"}, spec.Selector.MatchExpressions[0].Values)
			require.Equal(t, "second", spec.Selector.MatchExpressions[0].Key)
			require.Equal(t, "", spec.Selector.MatchExpressions[1].Key)
			if test.input == "" {
				require.Nil(t, spec.MinAvailable)
				require.Nil(t, spec.MaxUnavailable)
			}
			require.Empty(t, checkNativePDBInputs(t, "pod-disruption-budget", r))
		})
	}
	spec, err := nativePDBSpec(PodDisruptionBudgetSpec{})
	require.NoError(t, err)
	body, err := json.Marshal(spec)
	require.NoError(t, err)
	require.JSONEq(t, `{"selector":{}}`, string(body))
}

func TestPDBHistoricalIdentityAndFailures(t *testing.T) {
	s := &pdbServer{}
	c := pdbAPI(t, s)
	r := validPDB()
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	p := pdbPrior{Outputs: created}
	r.Metadata = PodDisruptionBudgetMetadata{}
	c.Server = new("https://unused.invalid")
	_, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	s.object.UID = "different"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed")
	require.Error(t, r.Delete(t.Context(), c, p))
	for _, status := range []int{403, 409, 500, 404} {
		s.failure = status
		before := len(s.calls)
		_, err = r.Read(t.Context(), c, p)
		if status == 404 {
			require.ErrorIs(t, err, runtime.ErrNotFound)
			require.NoError(t, r.Delete(t.Context(), c, p))
		} else {
			require.Error(t, err)
			require.Error(t, r.Delete(t.Context(), c, p))
		}
		require.Equal(t, []string{"GET", "DELETE"}, s.calls[before:])
	}
	s.failure = 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.Read(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, r.Delete(ctx, c, p), context.Canceled)
	deadline, finish := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer finish()
	_, err = r.Read(deadline, c, p)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, r.Delete(deadline, c, p), context.DeadlineExceeded)
	for _, output := range []*PodDisruptionBudgetResourceOutput{nil, {}, {Server: "saved"}} {
		_, err = r.Read(t.Context(), c, pdbPrior{Outputs: output})
		require.ErrorContains(t, err, "identity")
		require.ErrorContains(t, r.Delete(t.Context(), c, pdbPrior{Outputs: output}), "identity")
	}
}

func TestPDBMetadataPatchAndProjection(t *testing.T) {
	s := &pdbServer{}
	c := pdbAPI(t, s)
	r := validPDB()
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	s.object.Labels = map[string]string{"controller.kubernetes.io/hidden": "remote"}
	p := pdbPrior{Inputs: *r, Outputs: created}
	r.Metadata.Labels = &map[string]string{"managed": ""}
	updated, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []pdbPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: created.UID},
		{Op: "add", Path: "/metadata/labels", Value: map[string]any{"managed": ""}},
	}, s.patch)
	require.Equal(t, map[string]string{"managed": ""}, s.object.Labels)
	p.Outputs = updated
	r.Metadata.Labels = nil
	updated, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Empty(t, updated.Metadata.Labels)
	p.Outputs = updated
	before := len(s.calls)
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"GET"}, s.calls[before:])
	projected, err := projectPDBMetadata(map[string]string{
		"controller.kubernetes.io/key": "managed", "ignored/key": "managed",
		"app.kubernetes.io/name": "visible", "service.beta.kubernetes.io/key": "visible",
		"other.kubernetes.io/key": "hidden", "deprecated.daemonset.template.generation": "hidden",
	}, map[string]string{"controller.kubernetes.io/key": "", "ignored/key": ""},
		&[]string{"^ignored/"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"controller.kubernetes.io/key": "managed", "ignored/key": "managed",
		"app.kubernetes.io/name": "visible", "service.beta.kubernetes.io/key": "visible",
	}, projected)
	metadata := normalizePDBMetadata(PodDisruptionBudgetMetadata{Namespace: new("")})
	require.Equal(t, "", metadata.Namespace)
}

func TestPDBMutationFailuresAndCancellation(t *testing.T) {
	s := &pdbServer{}
	c := pdbAPI(t, s)
	r := validPDB()
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	p := pdbPrior{Inputs: *r, Outputs: created}
	r.Metadata.Labels = &map[string]string{"key": "value"}
	s.object.UID = "different"
	_, err = r.Update(t.Context(), c, p)
	require.Error(t, err)
	require.Empty(t, s.object.Labels)
	s.object.UID = "uid-1"
	for _, status := range []int{403, 409, 500} {
		s.failure = status
		before := len(s.calls)
		_, err = r.Update(t.Context(), c, p)
		require.ErrorContains(t, err, "patch PodDisruptionBudget")
		_, err = r.Create(t.Context(), c)
		require.ErrorContains(t, err, "create PodDisruptionBudget")
		require.Equal(t, []string{"PATCH", "POST"}, s.calls[before:])
	}
	s.failure = 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Update(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	r.Spec.MinAvailable = new("2")
	before := len(s.calls)
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	require.Equal(t, before, len(s.calls))
}

func TestPDBCreateRefreshFailure(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			require.NoError(t, json.NewEncoder(w).Encode(policyv1.PodDisruptionBudget{
				ObjectMeta: metav1.ObjectMeta{Name: "pdb-generated", Namespace: "default", UID: "uid"},
			}))
		} else {
			w.WriteHeader(403)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Status: "Failure", Code: 403, Reason: metav1.StatusReasonForbidden,
			}))
		}
	}))
	defer server.Close()
	c := &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: new(true)}
	_, err := validPDB().Create(t.Context(), c)
	require.ErrorContains(t, err, "read PodDisruptionBudget")
	require.Equal(t, []string{"POST", "GET"}, calls)
}

func TestPDBExplicitEmptyNamespace(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		require.Equal(t, "/apis/policy/v1/poddisruptionbudgets", r.URL.Path)
		var posted policyv1.PodDisruptionBudget
		require.NoError(t, json.NewDecoder(r.Body).Decode(&posted))
		require.Empty(t, posted.Namespace)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
			Status: "Failure", Code: 400, Reason: metav1.StatusReasonBadRequest,
		}))
	}))
	defer server.Close()
	c := &config.Configuration{Server: &server.URL, Namespace: new("configured"),
		InsecureSkipTLSVerify: new(true)}
	r := validPDB()
	r.Metadata.Namespace = new("")
	_, err := r.Create(t.Context(), c)
	require.ErrorContains(t, err, "create PodDisruptionBudget")
	require.Equal(t, []string{"POST"}, calls)
}
