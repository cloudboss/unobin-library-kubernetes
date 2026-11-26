package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestCRDEstablishmentAndFreshDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name            string
		change          func(*api.CustomResourceDefinition)
		discoveryStatus int
		block           string
		diagnostic      string
	}{
		{name: "ready"},
		{name: "unserved", change: func(live *api.CustomResourceDefinition) {
			live.Spec.Versions[0].Served = false
		}},
		{name: "names rejected", change: func(live *api.CustomResourceDefinition) {
			live.Status.Conditions = append(live.Status.Conditions,
				api.CustomResourceDefinitionCondition{
					Type: api.NamesAccepted, Status: api.ConditionFalse, Reason: "Conflict",
				})
		}, diagnostic: "names rejected: Conflict"},
		{name: "terminating", change: func(live *api.CustomResourceDefinition) {
			live.Status.Conditions = append(live.Status.Conditions,
				api.CustomResourceDefinitionCondition{
					Type: api.Terminating, Status: api.ConditionTrue,
				})
		}, diagnostic: "terminating"},
		{name: "deletion", change: func(live *api.CustomResourceDefinition) {
			now := metav1.Now()
			live.DeletionTimestamp = &now
		}, diagnostic: "being deleted"},
		{name: "replacement", change: func(live *api.CustomResourceDefinition) {
			live.UID = "another-uid"
		}, diagnostic: "UID changed"},
		{name: "spec changed", change: func(live *api.CustomResourceDefinition) {
			live.Spec.Names.Categories = []string{"changed"}
		}, diagnostic: "spec changed"},
		{name: "not established", change: func(live *api.CustomResourceDefinition) {
			live.Status.Conditions = nil
		}, diagnostic: "context deadline exceeded"},
		{name: "discovery missing", discoveryStatus: 404, diagnostic: "context deadline exceeded"},
		{name: "discovery unauthorized", discoveryStatus: 401, diagnostic: "provide credentials"},
		{name: "discovery forbidden", discoveryStatus: 403, diagnostic: "unknown"},
		{name: "discovery failed", discoveryStatus: 500, diagnostic: "server"},
		{name: "blocking discovery", block: "discovery", diagnostic: "context deadline exceeded"},
		{name: "blocking get", block: "get", diagnostic: "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			written, err := prepareCRD(desiredCRD())
			require.NoError(t, err)
			written.UID = "uid-1"
			written.Status.Conditions = []api.CustomResourceDefinitionCondition{{
				Type: api.Established, Status: api.ConditionTrue,
			}}
			live := written.DeepCopy()
			if tc.change != nil {
				tc.change(live)
				if tc.name == "unserved" {
					written = live.DeepCopy()
				}
			}
			calls := make(chan string, 20)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				calls <- r.URL.Path
				isDiscovery := r.URL.Path == "/apis/example.test/v1"
				if (isDiscovery && tc.block == "discovery") || (!isDiscovery && tc.block == "get") {
					<-r.Context().Done()
					return
				}
				if isDiscovery {
					if tc.discoveryStatus != 0 {
						code := tc.discoveryStatus
						w.WriteHeader(code)
						_ = json.NewEncoder(w).Encode(&metav1.Status{
							Status: "Failure", Code: int32(code), Message: http.StatusText(code),
						})
						return
					}
					_ = json.NewEncoder(w).Encode(&metav1.APIResourceList{
						GroupVersion: "example.test/v1", APIResources: []metav1.APIResource{{
							Name: "widgets", Kind: "Widget", Namespaced: true,
						}},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(live)
			}))
			defer server.Close()
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			connection, err := crdClient(ctx, &config.Configuration{Server: &server.URL}, nil)
			require.NoError(t, err)
			got, err := waitCRD(ctx, connection, written)
			if tc.diagnostic != "" {
				require.ErrorContains(t, err, tc.diagnostic)
				return
			}
			require.NoError(t, err)
			require.Equal(t, live, got)
			close(calls)
			var paths []string
			for path := range calls {
				paths = append(paths, path)
			}
			want := []string{
				"/apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
			}
			if tc.name != "unserved" {
				want = append(want, "/apis/example.test/v1")
			}
			want = append(want,
				"/apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test")
			require.Equal(t, want, paths)
		})
	}
}

func TestCRDDiscoversEveryServedVersion(t *testing.T) {
	written, err := prepareCRD(desiredCRD())
	require.NoError(t, err)
	written.UID = "uid-1"
	written.Spec.Versions = append(written.Spec.Versions, api.CustomResourceDefinitionVersion{
		Name: "v2", Served: true,
	})
	written.Status.Conditions = []api.CustomResourceDefinitionCondition{{
		Type: api.Established, Status: api.ConditionTrue,
	}}
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/apis/example.test/v1" || r.URL.Path == "/apis/example.test/v2" {
			_ = json.NewEncoder(w).Encode(&metav1.APIResourceList{
				APIResources: []metav1.APIResource{{
					Name: "widgets", Kind: "Widget", Namespaced: true,
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(written)
	}))
	defer server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	connection, err := crdClient(t.Context(), &config.Configuration{Server: &server.URL}, nil)
	require.NoError(t, err)
	_, err = waitCRD(t.Context(), connection, written)
	require.NoError(t, err)
	require.Equal(t, []string{
		"/apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
		"/apis/example.test/v1", "/apis/example.test/v2",
		"/apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
	}, calls)
}

func TestCRDRejectsSpecChangeDuringDiscovery(t *testing.T) {
	written, err := prepareCRD(desiredCRD())
	require.NoError(t, err)
	written.UID = "uid-1"
	written.Status.Conditions = []api.CustomResourceDefinitionCondition{{
		Type: api.Established, Status: api.ConditionTrue,
	}}
	live := written.DeepCopy()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/apis/example.test/v1" {
			live.Spec.Names.Categories = []string{"concurrent-change"}
			_ = json.NewEncoder(w).Encode(&metav1.APIResourceList{
				APIResources: []metav1.APIResource{{
					Name: "widgets", Kind: "Widget", Namespaced: true,
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(live)
	}))
	defer server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	connection, err := crdClient(t.Context(), &config.Configuration{Server: &server.URL}, nil)
	require.NoError(t, err)
	_, err = waitCRD(t.Context(), connection, written)
	require.ErrorContains(t, err, "spec changed")
}
