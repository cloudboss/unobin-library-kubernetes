package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	discoveryv2 "k8s.io/api/apidiscovery/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestDynamicDiscovery(t *testing.T) {
	for _, preferredVersion := range []string{"v1", "v2"} {
		t.Run("preferred "+preferredVersion, func(t *testing.T) {
			checkDynamicDiscovery(t, preferredVersion)
		})
	}
}

func checkDynamicDiscovery(t *testing.T, preferredVersion string) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			body = metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "example.com",
				Versions: []metav1.GroupVersionForDiscovery{{
					GroupVersion: "example.com/v1", Version: "v1",
				}, {
					GroupVersion: "example.com/v2", Version: "v2",
				}},
				PreferredVersion: metav1.GroupVersionForDiscovery{
					GroupVersion: "example.com/" + preferredVersion, Version: preferredVersion,
				},
			}}}
		case "/api/v1":
			body = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
				{Name: "namespaces", Kind: "Namespace"},
			}}
		case "/apis/example.com/v1":
			body = metav1.APIResourceList{GroupVersion: "example.com/v1",
				APIResources: []metav1.APIResource{{Name: "people", Kind: "Person", Namespaced: true}}}
		case "/apis/example.com/v2":
			w.WriteHeader(http.StatusForbidden)
			body = metav1.Status{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
				Status:   metav1.StatusFailure, Reason: metav1.StatusReasonForbidden,
				Message: "example.com/v2 discovery is forbidden", Code: http.StatusForbidden,
			}
		default:
			require.Equal(t, "/apis/example.com/v1/namespaces/team/people/alice", r.URL.Path)
			body = map[string]any{"apiVersion": "example.com/v1", "kind": "Person",
				"metadata": map[string]any{"name": "alice"}}
		}
		require.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(server.Close)
	c, err := NewDynamicContext(t.Context(),
		&config.Configuration{Server: &server.URL, Namespace: new("team")})
	require.NoError(t, err)
	for _, tt := range []struct {
		kind      schema.GroupVersionKind
		namespace string
		want      string
	}{
		{kind: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, want: "team"},
		{kind: schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"},
			namespace: "other", want: "other"},
		{kind: schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}},
	} {
		_, namespace, err := c.Resolve(tt.kind, tt.namespace)
		require.NoError(t, err)
		require.Equal(t, tt.want, namespace)
	}
	_, _, err = c.Resolve(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, "team")
	require.ErrorContains(t, err, "cluster-scoped")
	_, _, err = c.Resolve(schema.GroupVersionKind{Version: "v1", Kind: "Unknown"}, "")
	require.True(t, meta.IsNoMatchError(err), "%v", err)
	_, _, err = c.Resolve(schema.GroupVersionKind{
		Group: "example.com", Version: "v2", Kind: "Person",
	}, "")
	require.ErrorContains(t, err, "discover Kubernetes kind")
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	_, _, err = c.Resolve(schema.GroupVersionKind{Group: "example.com", Kind: "Widget"}, "")
	require.ErrorContains(t, err, "discover Kubernetes kind")
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	_, _, err = c.Resolve(schema.GroupVersionKind{Group: "example.com", Kind: "Person"}, "")
	require.NoError(t, err)
	resource, namespace, err := c.Resolve(schema.GroupVersionKind{
		Group: "example.com", Version: "v1", Kind: "Person",
	}, "")
	require.NoError(t, err)
	require.Equal(t, "team", namespace)
	obj, err := resource.Get(t.Context(), "alice", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "alice", obj.GetName())
}

func TestDynamicAggregatedStaleGroupDiscovery(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var group discoveryv2.APIGroupDiscovery
		switch r.URL.Path {
		case "/api":
			group.Versions = []discoveryv2.APIVersionDiscovery{
				{Version: "v1", Freshness: discoveryv2.DiscoveryFreshnessCurrent},
			}
		case "/apis":
			group.Name = "example.com"
			group.Versions = []discoveryv2.APIVersionDiscovery{
				{Version: "v2", Freshness: discoveryv2.DiscoveryFreshnessStale},
			}
		default:
			t.Errorf("unexpected request for %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", discovery.AcceptV2)
		require.NoError(t, json.NewEncoder(w).Encode(discoveryv2.APIGroupDiscoveryList{
			Items: []discoveryv2.APIGroupDiscovery{group},
		}))
	}))
	t.Cleanup(server.Close)
	client, err := NewDynamicContext(t.Context(), &config.Configuration{Server: &server.URL})
	require.NoError(t, err)
	_, _, err = client.Resolve(schema.GroupVersionKind{Group: "other.com", Kind: "Widget"}, "")
	require.True(t, meta.IsNoMatchError(err), "%v", err)
	_, _, err = client.Resolve(schema.GroupVersionKind{Group: "example.com", Kind: "Widget"}, "")
	var stale discovery.StaleGroupVersionError
	require.True(t, errors.As(err, &stale), "%v", err)
}

func TestDynamicConfigurationError(t *testing.T) {
	_, err := NewDynamic(&config.Configuration{Kubeconfig: new("testdata/malformed.yaml")})
	require.ErrorContains(t, err, "load Kubernetes configuration")
}
