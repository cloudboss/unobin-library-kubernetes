package object

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apidiscoveryv2 "k8s.io/api/apidiscovery/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func discoveryFixture(path string) any {
	switch path {
	case "/api":
		return metav1.APIVersions{Versions: []string{"v1"}}
	case "/apis":
		return metav1.APIGroupList{Groups: []metav1.APIGroup{
			{Name: "example.io", Versions: []metav1.GroupVersionForDiscovery{
				{GroupVersion: "example.io/v2", Version: "v2"},
				{GroupVersion: "example.io/v1", Version: "v1"},
			}, PreferredVersion: metav1.GroupVersionForDiscovery{
				GroupVersion: "example.io/v2", Version: "v2",
			}},
			{Name: "apps", Versions: []metav1.GroupVersionForDiscovery{
				{GroupVersion: "apps/v1", Version: "v1"},
			}, PreferredVersion: metav1.GroupVersionForDiscovery{
				GroupVersion: "apps/v1", Version: "v1",
			}},
		}}
	case "/api/v1":
		return metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods/status", SingularName: "", Kind: "Pod", Namespaced: true,
				Verbs: []string{"update", "get"}},
			{Name: "namespaces", SingularName: "namespace", Kind: "Namespace",
				Verbs: []string{"list", "get", "get"}},
			{Name: "configmaps", SingularName: "configmap", Kind: "ConfigMap", Namespaced: true,
				Verbs:      []string{"watch", "list", "get", "get"},
				ShortNames: []string{"cm", "cm"}, Categories: []string{"z", "all", "all"}},
		}}
	case "/apis/apps/v1":
		return metav1.APIResourceList{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true},
		}}
	case "/apis/example.io/v1", "/apis/example.io/v2":
		return metav1.APIResourceList{GroupVersion: path[len("/apis/"):],
			APIResources: []metav1.APIResource{
				{Name: "widgets", Kind: "Widget", Verbs: []string{"get", "custom"}},
			}}
	default:
		return nil
	}
}

func discoveryServer(
	t *testing.T, override func(http.ResponseWriter, *http.Request) bool,
) (*config.Configuration, func() []string) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	var mu sync.Mutex
	var calls []string
	handler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		require.Equal(t, "Bearer discovery-identity", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if override != nil && override(w, r) {
			return
		}
		body := discoveryFixture(r.URL.Path)
		if body == nil {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		writeLookupJSON(t, w, body)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(handler))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: new(true),
			Token: new("discovery-identity")}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(calls)
		}
}

func discoveryRows() []APIResourceRow {
	return []APIResourceRow{
		{APIVersion: "v1", Version: "v1", Name: "configmaps", SingularName: "configmap",
			Kind: "ConfigMap", Namespaced: true, Verbs: []string{"get", "list", "watch"},
			ShortNames: []string{"cm"}, Categories: []string{"all", "z"}},
		{APIVersion: "v1", Version: "v1", Name: "namespaces", SingularName: "namespace",
			Kind: "Namespace", Verbs: []string{"get", "list"},
			ShortNames: []string{}, Categories: []string{}},
		{APIVersion: "apps/v1", Group: "apps", Version: "v1", Name: "deployments",
			Kind: "Deployment", Namespaced: true, Verbs: []string{},
			ShortNames: []string{}, Categories: []string{}},
		{APIVersion: "example.io/v2", Group: "example.io", Version: "v2", Name: "widgets",
			Kind: "Widget", Verbs: []string{"custom", "get"},
			ShortNames: []string{}, Categories: []string{}},
	}
}

func TestAPIResourcesDiscoveryAndFilters(t *testing.T) {
	rows := discoveryRows()
	old := rows[3]
	old.APIVersion, old.Version = "example.io/v1", "v1"
	sub := APIResourceRow{APIVersion: "v1", Version: "v1", Name: "pods/status", Kind: "Pod",
		Namespaced: true, Verbs: []string{"get", "update"},
		ShortNames: []string{}, Categories: []string{}}
	for _, tc := range []struct {
		name  string
		query APIResourcesDataSource
		want  []APIResourceRow
	}{
		{"defaults", APIResourcesDataSource{}, rows},
		{"all versions", APIResourcesDataSource{PreferredVersionsOnly: new(false)},
			[]APIResourceRow{rows[0], rows[1], rows[2], old, rows[3]}},
		{"explicit defaults", APIResourcesDataSource{PreferredVersionsOnly: new(true),
			IncludeSubresources: new(false)}, rows},
		{"core", APIResourcesDataSource{APIGroup: new("")}, rows[:2]},
		{"cluster core", APIResourcesDataSource{APIGroup: new(""), Namespaced: new(false)},
			[]APIResourceRow{rows[1]}},
		{"conjunctive", APIResourcesDataSource{APIGroup: new(""), Namespaced: new(true),
			Verbs: new([]string{"get", "watch"})}, rows[:1]},
		{"all required", APIResourcesDataSource{Verbs: new([]string{"get", "custom", "custom"})},
			rows[3:]},
		{"empty verb set", APIResourcesDataSource{Verbs: new([]string{})}, rows},
		{"exact group", APIResourcesDataSource{APIGroup: new("example.*")}, []APIResourceRow{}},
		{"no match", APIResourcesDataSource{Verbs: new([]string{"not-served"})}, []APIResourceRow{}},
		{"subresources", APIResourcesDataSource{IncludeSubresources: new(true)},
			[]APIResourceRow{rows[0], rows[1], sub, rows[2], rows[3]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := discoveryServer(t, nil)
			original := *c
			out, err := tc.query.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &APIResourcesDataSourceOutput{Resources: tc.want}, out)
			require.Equal(t, original, *c)
			require.ElementsMatch(t, []string{"GET /api", "GET /apis", "GET /api/v1",
				"GET /apis/apps/v1", "GET /apis/example.io/v1", "GET /apis/example.io/v2"}, calls())
			if len(tc.want) == 0 {
				body, err := json.Marshal(out.Resources)
				require.NoError(t, err)
				require.JSONEq(t, "[]", string(body))
			}
		})
	}
}

func TestAPIResourcesFreshAndDeterministic(t *testing.T) {
	var current atomic.Int32
	c, _ := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v1" {
			return false
		}
		body := discoveryFixture("/api/v1").(metav1.APIResourceList)
		invocation := current.Load()
		if invocation > 0 {
			slices.Reverse(body.APIResources)
			for i := range body.APIResources {
				slices.Reverse(body.APIResources[i].Verbs)
			}
		}
		if invocation > 1 {
			body.APIResources = append(body.APIResources, metav1.APIResource{
				Name: "zebras", Kind: "Zebra", Namespaced: true})
		}
		writeLookupJSON(t, w, body)
		return true
	})
	query := APIResourcesDataSource{APIGroup: new("")}
	for invocation := int32(0); invocation < 3; invocation++ {
		current.Store(invocation)
		want := discoveryRows()[:2]
		if invocation > 1 {
			want = append(want, APIResourceRow{APIVersion: "v1", Version: "v1", Name: "zebras",
				Kind: "Zebra", Namespaced: true, Verbs: []string{},
				ShortNames: []string{}, Categories: []string{}})
		}
		out, err := query.Read(t.Context(), c)
		require.NoError(t, err)
		require.Equal(t, want, out.Resources)
	}
}

func TestAPIResourcesDiscoveryErrors(t *testing.T) {
	for _, path := range []string{"/apis", "/apis/example.io/v1", "/apis/example.io/v2"} {
		for _, group := range []*string{nil, new("")} {
			t.Run(path+"/filtered="+stringBool(group != nil), func(t *testing.T) {
				c, _ := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != path {
						return false
					}
					w.WriteHeader(http.StatusForbidden)
					writeLookupJSON(t, w, metav1.Status{
						TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
						Status:   "Failure", Reason: "Forbidden",
						Code: http.StatusForbidden, Message: "discovery denied"})
					return true
				})
				out, err := (&APIResourcesDataSource{APIGroup: group}).Read(t.Context(), c)
				require.Nil(t, out)
				require.ErrorContains(t, err, "discover Kubernetes API resources")
				if path != "/apis" {
					require.ErrorContains(t, err, "discovery denied")
					var partial *discovery.ErrGroupDiscoveryFailed
					require.ErrorAs(t, err, &partial)
				} else {
					require.True(t, apierrors.IsForbidden(err), "%#v", err)
				}
			})
		}
	}
}

func TestAPIResourcesMissingDiscoveryEndpoints(t *testing.T) {
	for _, path := range []string{"/api", "/api/v1"} {
		t.Run(path, func(t *testing.T) {
			c, _ := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != path {
					return false
				}
				w.WriteHeader(http.StatusNotFound)
				writeLookupJSON(t, w, metav1.Status{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
					Status:   "Failure", Reason: "NotFound", Code: http.StatusNotFound,
				})
				return true
			})
			out, err := (&APIResourcesDataSource{APIGroup: new("example.io")}).Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "discover Kubernetes API resources")
			require.True(t, apierrors.IsNotFound(err), "%v", err)
		})
	}
}

func TestAPIResourcesPrefixedServer(t *testing.T) {
	for _, missing := range []string{"", "/api", "/api/v1"} {
		t.Run("missing="+missing, func(t *testing.T) {
			c, calls := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) bool {
				path, ok := strings.CutPrefix(r.URL.Path, "/prefix")
				require.True(t, ok, "request must use the configured server prefix")
				if path == missing {
					w.WriteHeader(http.StatusNotFound)
					writeLookupJSON(t, w, metav1.Status{
						TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
						Status:   "Failure", Reason: "NotFound", Code: http.StatusNotFound,
					})
					return true
				}
				body := discoveryFixture(path)
				require.NotNil(t, body, "unexpected discovery endpoint %s", r.URL.Path)
				writeLookupJSON(t, w, body)
				return true
			})
			c.Server = new(*c.Server + "/prefix")
			out, err := (&APIResourcesDataSource{APIGroup: new("example.io")}).Read(t.Context(), c)
			if missing == "" {
				require.NoError(t, err)
				require.Equal(t, discoveryRows()[3:], out.Resources)
				require.ElementsMatch(t, []string{
					"GET /prefix/api", "GET /prefix/apis", "GET /prefix/api/v1",
					"GET /prefix/apis/apps/v1", "GET /prefix/apis/example.io/v1",
					"GET /prefix/apis/example.io/v2",
				}, calls())
			} else {
				require.Nil(t, out)
				require.ErrorContains(t, err, "discover Kubernetes API resources")
				require.True(t, apierrors.IsNotFound(err), "%v", err)
			}
		})
	}
}

func TestAPIResourcesAggregatedDiscovery(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(stringBool(stale), func(t *testing.T) {
			c, calls := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api" && r.URL.Path != "/apis" {
					return false
				}
				w.Header().Set("Content-Type",
					"application/json;g=apidiscovery.k8s.io;v=v2;as=APIGroupDiscoveryList")
				groups := []apidiscoveryv2.APIGroupDiscovery{}
				if r.URL.Path == "/api" {
					groups = append(groups, aggregatedGroup("", "v1", false))
				} else {
					groups = append(groups, aggregatedGroup("example.io", "v2", stale))
				}
				writeLookupJSON(t, w, apidiscoveryv2.APIGroupDiscoveryList{Items: groups})
				return true
			})
			out, err := (&APIResourcesDataSource{}).Read(t.Context(), c)
			if stale {
				require.Nil(t, out)
				var partial *discovery.ErrGroupDiscoveryFailed
				require.ErrorAs(t, err, &partial)
			} else {
				require.NoError(t, err)
				require.Equal(t, []APIResourceRow{
					{APIVersion: "v1", Version: "v1", Name: "widgets", SingularName: "widget",
						Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list"},
						ShortNames: []string{}, Categories: []string{}},
					{APIVersion: "example.io/v2", Group: "example.io", Version: "v2",
						Name: "widgets", SingularName: "widget", Kind: "Widget", Namespaced: true,
						Verbs: []string{"get", "list"}, ShortNames: []string{}, Categories: []string{}},
				}, out.Resources)
			}
			for _, call := range calls() {
				require.Contains(t, []string{"GET /api", "GET /apis"}, call)
			}
		})
	}
}

func aggregatedGroup(group, version string, stale bool) apidiscoveryv2.APIGroupDiscovery {
	freshness := apidiscoveryv2.DiscoveryFreshnessCurrent
	if stale {
		freshness = apidiscoveryv2.DiscoveryFreshnessStale
	}
	return apidiscoveryv2.APIGroupDiscovery{
		ObjectMeta: metav1.ObjectMeta{Name: group},
		Versions: []apidiscoveryv2.APIVersionDiscovery{{
			Version: version, Freshness: freshness,
			Resources: []apidiscoveryv2.APIResourceDiscovery{{
				Resource: "widgets", SingularResource: "widget", Scope: apidiscoveryv2.ScopeNamespace,
				ResponseKind: &metav1.GroupVersionKind{Group: group, Version: version, Kind: "Widget"},
				Verbs:        []string{"list", "get"},
			}},
		}},
	}
}

func stringBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestAPIResourcesCancellation(t *testing.T) {
	for _, path := range []string{"", "/api", "/apis", "/api/v1", "/apis/example.io/v2"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := make(chan struct{}, 1)
			c, calls := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != path {
					return false
				}
				select {
				case started <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return true
			})
			if path == "" {
				cancel()
			}
			finished := make(chan error, 1)
			go func() {
				out, err := (&APIResourcesDataSource{}).Read(ctx, c)
				if out != nil {
					t.Error("canceled discovery returned successful output")
				}
				finished <- err
			}()
			if path != "" {
				select {
				case <-started:
					cancel()
				case <-ctx.Done():
					t.Fatal("discovery request did not start")
				}
			}
			select {
			case err := <-finished:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("discovery did not stop after cancellation")
			}
			if path == "" {
				require.Empty(t, calls())
			}
		})
	}
}

func TestAPIResourcesResponseBodyDeadline(t *testing.T) {
	for _, path := range []string{"/apis", "/api/v1"} {
		t.Run(path, func(t *testing.T) {
			c, _ := discoveryServer(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != path {
					return false
				}
				_, _ = w.Write([]byte("{"))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return true
			})
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			started := time.Now()
			out, err := (&APIResourcesDataSource{}).Read(ctx, c)
			require.Nil(t, out)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(started), time.Second)
		})
	}
}
