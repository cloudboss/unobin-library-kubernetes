package object

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestObjectInfoDiscoveryAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, version, kind string
		namespace           *string
		path                string
	}{
		{"core", "v1", "ConfigMap", new("team"), "/api/v1/namespaces/team/configmaps/example"},
		{"custom", "example.io/v2", "Widget", new("team"),
			"/apis/example.io/v2/namespaces/team/widgets/example"},
		{"preferred version", "example.io/", "Widget", new("team"),
			"/apis/example.io/v1/namespaces/team/widgets/example"},
		{"default", "v1", "ConfigMap", nil, "/api/v1/namespaces/default/configmaps/example"},
		{"empty", "v1", "ConfigMap", new(""), "/api/v1/namespaces/default/configmaps/example"},
		{"cluster scoped", "v1", "Namespace", new("ignored/value"), "/api/v1/namespaces/example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := map[string]any{
				"apiVersion": tc.version, "kind": tc.kind,
				"metadata": map[string]any{
					"name": "example", "resourceVersion": "82", "uid": "remote-uid",
				},
				"spec": map[string]any{
					"numericPort": int64(8080), "namedPort": "https", "large": int64(9007199254740993),
					"properties": map[string]any{
						"ordered": []any{"first", int64(2), false, nil},
						"enabled": true, "threshold": 0.25, "explicitNull": nil,
						"emptyMap": map[string]any{}, "emptyList": []any{},
					},
				},
				"status": map[string]any{"currentPhase": "Ready"},
			}
			if tc.name == "preferred version" {
				observed["apiVersion"] = "example.io/v1"
			}
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != tc.path {
					http.NotFound(w, req)
					return
				}
				writeLookupJSON(t, w, observed)
			})
			original := *c
			d := &ObjectDataSource{APIVersion: tc.version, Kind: tc.kind, Metadata: ObjectLookupMetadata{
				Name: "example", Namespace: tc.namespace,
			}}
			out, err := d.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &ObjectDataSourceOutput{Object: &observed}, out)
			require.Equal(t, original, *c)
			require.Equal(t, "configured", *c.Namespace)
			requireLookupCalls(t, calls(), "GET "+tc.path)
		})
	}
}

func TestObjectInfoFailuresAndAbsence(t *testing.T) {
	const target = "/api/v1/namespaces/default/configmaps/example"
	for _, tc := range []struct {
		name, kind  string
		namespace   *string
		status      *apierrors.StatusError
		response    map[string]any
		configError bool
		wantError   string
		checkError  func(error) bool
		wantCalls   []string
	}{
		{name: "absent", kind: "ConfigMap", wantCalls: []string{"GET " + target},
			status: apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "example")},
		{name: "empty members", kind: "ConfigMap", wantCalls: []string{"GET " + target},
			response: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "data": map[string]any{}}},
		{name: "forbidden", kind: "ConfigMap", wantCalls: []string{"GET " + target},
			status:    apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "example", nil),
			wantError: "get Kubernetes ConfigMap example", checkError: apierrors.IsForbidden},
		{name: "unknown kind", kind: "Missing", wantError: "discover Kubernetes kind",
			checkError: meta.IsNoMatchError},
		{name: "invalid namespace", kind: "ConfigMap", namespace: new("ignored/value"),
			wantError: "invalid namespace"},
		{name: "invalid configuration", kind: "ConfigMap", configError: true,
			wantError: "load Kubernetes configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				if tc.status != nil {
					w.WriteHeader(int(tc.status.ErrStatus.Code))
					writeLookupJSON(t, w, tc.status.ErrStatus)
					return
				}
				writeLookupJSON(t, w, tc.response)
			})
			if tc.configError {
				c.Kubeconfig = new(filepath.Join(os.Getenv("GOTMPDIR"), "absent-object-info-kubeconfig"))
			}
			d := &ObjectDataSource{APIVersion: "v1", Kind: tc.kind, Metadata: ObjectLookupMetadata{
				Name: "example", Namespace: tc.namespace,
			}}
			out, err := d.Read(t.Context(), c)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, out)
				if tc.checkError != nil {
					require.True(t, tc.checkError(err), "%v", err)
				}
			} else {
				require.NoError(t, err)
				want := &ObjectDataSourceOutput{}
				if tc.response != nil {
					want.Object = &tc.response
				}
				require.Equal(t, want, out)
			}
			if tc.configError {
				require.Empty(t, calls())
			} else {
				requireLookupCalls(t, calls(), tc.wantCalls...)
			}
		})
	}
}

func TestObjectInfoDiscoveryFailure(t *testing.T) {
	const widgetPath = "/apis/example.io/v2/namespaces/default/widgets/example"
	for _, tc := range []struct {
		name, version, kind, failingPath string
		succeeds                         bool
	}{
		{"group list fails", "example.io/v2", "Widget", "/apis", false},
		{"requested group version fails", "example.io/v2", "Widget",
			"/apis/example.io/v2", false},
		{"unrelated group version fails", "example.io/v2", "Widget",
			"/apis/example.io/v1", true},
		{"preferred version fails without a fallback", "example.io/", "Missing",
			"/apis/example.io/v1", false},
		{"preferred version fails with a fallback", "example.io/", "Widget",
			"/apis/example.io/v1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := map[string]any{
				"apiVersion": "example.io/v2", "kind": "Widget",
				"metadata": map[string]any{"name": "example", "namespace": "default"},
			}
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				writeLookupJSON(t, w, observed)
			}, tc.failingPath)
			d := &ObjectDataSource{
				APIVersion: tc.version, Kind: tc.kind,
				Metadata: ObjectLookupMetadata{Name: "example"},
			}
			out, err := d.Read(t.Context(), c)
			if tc.succeeds {
				require.NoError(t, err)
				require.Equal(t, &ObjectDataSourceOutput{Object: &observed}, out)
				requireLookupCalls(t, calls(), "GET "+widgetPath)
				return
			}
			require.ErrorContains(t, err, "discover Kubernetes kind")
			require.Nil(t, out)
			if tc.failingPath == "/apis" {
				require.Equal(t, []string{"GET /api?timeout=32s", "GET /apis?timeout=32s"}, calls())
			} else {
				requireLookupCalls(t, calls())
			}
			require.True(t, apierrors.IsForbidden(err), "%T: %v", err, err)
		})
	}
}

func TestObjectInfoCancellation(t *testing.T) {
	for _, phase := range []string{"discovery", "object GET"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := make(chan struct{}, 1)
			block := func(w http.ResponseWriter, req *http.Request) {
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-req.Context().Done():
				case <-ctx.Done():
				}
			}
			c, _ := objectInfoServer(t, block)
			if phase == "discovery" {
				server := httptest.NewServer(http.HandlerFunc(block))
				t.Cleanup(server.Close)
				c.Server = &server.URL
			}
			finished := make(chan error, 1)
			go func() {
				d := &ObjectDataSource{APIVersion: "v1", Kind: "ConfigMap", Metadata: ObjectLookupMetadata{
					Name: "example",
				}}
				_, err := d.Read(ctx, c)
				finished <- err
			}()
			select {
			case <-started:
				cancel()
			case <-ctx.Done():
				t.Fatal("Kubernetes request did not start")
			}
			select {
			case err := <-finished:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("read did not return after cancellation")
			}
		})
	}
}

func objectInfoServer(
	t *testing.T, object http.HandlerFunc, failDiscovery ...string,
) (*config.Configuration, func() []string) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(os.Getenv("GOTMPDIR"), "absent-object-info-kubeconfig"))
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if len(failDiscovery) != 0 && req.URL.Path == failDiscovery[0] {
			forbidden := apierrors.NewForbidden(schema.GroupResource{}, "", nil)
			w.WriteHeader(http.StatusForbidden)
			writeLookupJSON(t, w, forbidden.ErrStatus)
			return
		}
		switch req.URL.Path {
		case "/api":
			writeLookupJSON(t, w, metav1.APIVersions{Versions: []string{"v1"}})
		case "/apis":
			v1 := metav1.GroupVersionForDiscovery{GroupVersion: "example.io/v1", Version: "v1"}
			v2 := metav1.GroupVersionForDiscovery{GroupVersion: "example.io/v2", Version: "v2"}
			writeLookupJSON(t, w, metav1.APIGroupList{Groups: []metav1.APIGroup{
				{Name: "example.io", Versions: []metav1.GroupVersionForDiscovery{v1, v2}, PreferredVersion: v1},
			}})
		case "/api/v1":
			writeLookupJSON(t, w, metav1.APIResourceList{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{
					{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
					{Name: "namespaces", Kind: "Namespace"},
				},
			})
		case "/apis/example.io/v1", "/apis/example.io/v2":
			writeLookupJSON(t, w, metav1.APIResourceList{GroupVersion: req.URL.Path[len("/apis/"):],
				APIResources: []metav1.APIResource{
					{Name: "widgets", Kind: "Widget", Namespaced: true},
					{Name: "mice", Kind: "Mouse", Namespaced: true},
				},
			})
		default:
			object(w, req)
		}
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, Namespace: new("configured")}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func writeLookupJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("write Kubernetes response: %v", err)
	}
}

func requireLookupCalls(t *testing.T, calls []string, wantObjects ...string) {
	t.Helper()
	const core, groups = "GET /api?timeout=32s", "GET /apis?timeout=32s"
	require.GreaterOrEqual(t, len(calls), 5, "%v", calls)
	require.Equal(t, []string{core, groups}, calls[:2])
	require.ElementsMatch(t, []string{
		"GET /api/v1?timeout=32s", "GET /apis/example.io/v1?timeout=32s",
		"GET /apis/example.io/v2?timeout=32s",
	}, calls[2:5])
	require.Equal(t, wantObjects, append([]string(nil), calls[5:]...))
}
