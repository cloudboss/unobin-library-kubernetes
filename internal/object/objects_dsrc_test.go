package object

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/structdecode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const (
	configMapsPath = "/api/v1/namespaces/default/configmaps"
	micePath       = "/apis/example.io/v2/namespaces/default/mice"
)

func TestObjectsDiscoveryAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, version, kind string
		options             map[string]any
		path                string
	}{
		{"omitted namespace", "v1", "ConfigMap", nil, configMapsPath},
		{"null namespace", "v1", "ConfigMap", map[string]any{"namespace": nil}, configMapsPath},
		{"empty namespace", "v1", "ConfigMap", map[string]any{"namespace": ""}, configMapsPath},
		{"explicit namespace", "v1", "ConfigMap", map[string]any{"namespace": "team"},
			"/api/v1/namespaces/team/configmaps"},
		{"cluster scoped", "v1", "Namespace", map[string]any{"namespace": "ignored/value"},
			"/api/v1/namespaces"},
		{"explicit custom version", "example.io/v2", "Mouse", nil, micePath},
		{"preferred custom version", "example.io/", "Mouse", nil,
			"/apis/example.io/v1/namespaces/default/mice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []map[string]any{{
				"apiVersion": tc.version, "kind": tc.kind,
				"metadata": map[string]any{"name": "example"},
			}}
			if tc.name == "preferred custom version" {
				items[0]["apiVersion"] = "example.io/v1"
			}
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				writeLookupJSON(t, w, objectList(tc.version, tc.kind, items))
			})
			original := *c
			out, err := readObjects(t, c, tc.version, tc.kind, tc.options)
			require.NoError(t, err)
			require.Equal(t, &ObjectsDataSourceOutput{Objects: &items}, out)
			require.Equal(t, original, *c)
			require.Equal(t, "configured", *c.Namespace)
			requireLookupCalls(t, calls(), "GET "+tc.path)
		})
	}
}

func TestObjectsListOptions(t *testing.T) {
	const label = "tier in (edge, proxy),owner!=ops"
	const field = "metadata.name=a+b,metadata.namespace=default"
	for _, tc := range []struct {
		name    string
		options map[string]any
		want    url.Values
	}{
		{"omitted", nil, url.Values{}},
		{"null", map[string]any{"field-selector": nil, "label-selector": nil, "limit": nil},
			url.Values{}},
		{"empty and zero", map[string]any{"field-selector": "", "label-selector": "", "limit": int64(0)},
			url.Values{}},
		{"selectors", map[string]any{"field-selector": field, "label-selector": label},
			url.Values{"fieldSelector": {field}, "labelSelector": {label}}},
		{"subunit", map[string]any{"limit": 0.5}, url.Values{}},
		{"fractional", map[string]any{"limit": 2.8}, url.Values{"limit": {"2"}}},
		{"negative fractional", map[string]any{"limit": -2.8}, url.Values{"limit": {"-2"}}},
		{"exact large integer", map[string]any{"limit": int64(9007199254740993)},
			url.Values{"limit": {"9007199254740993"}}},
		{"max integer", map[string]any{"limit": int64(math.MaxInt64)},
			url.Values{"limit": {"9223372036854775807"}}},
		{"above int64", map[string]any{"limit": 1e20}, url.Values{"limit": {"9223372036854775807"}}},
		{"below int64", map[string]any{"limit": -1e20}, url.Values{"limit": {"-9223372036854775808"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []map[string]any{}
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				assert.Equal(t, tc.want, req.URL.Query())
				writeLookupJSON(t, w, objectList("v1", "ConfigMap", items))
			})
			out, err := readObjects(t, c, "v1", "ConfigMap", tc.options)
			require.NoError(t, err)
			require.Equal(t, &ObjectsDataSourceOutput{Objects: &items}, out)
			path := configMapsPath
			if len(tc.want) != 0 {
				path += "?" + tc.want.Encode()
			}
			requireLookupCalls(t, calls(), "GET "+path)
		})
	}
}

func TestObjectsPreservesFirstPage(t *testing.T) {
	zebra := map[string]any{
		"metadata": map[string]any{
			"name": "zebra", "uid": "live-uid", "resourceVersion": "271",
			"managedFields": []any{map[string]any{"manager": "controller"}},
		},
		"spec": map[string]any{
			"port": int64(8080), "large": int64(9007199254740993), "fractional": 0.25,
			"nested":       []any{true, nil, map[string]any{}, []any{}},
			"explicitNull": nil,
		},
		"status": map[string]any{"currentPhase": "Ready"},
	}
	ant := map[string]any{
		"apiVersion": "other.io/v3", "kind": "SpecialMouse",
		"metadata": map[string]any{"name": "ant"},
		"spec":     map[string]any{"port": "https", "nested": []any{"one"}},
	}
	for _, tc := range []struct {
		name  string
		items []map[string]any
		limit int64
		query string
	}{
		{"server returned more than the limit", []map[string]any{zebra, ant}, 1, "?limit=1"},
		{"short page", []map[string]any{zebra}, 2, "?limit=2"},
		{"empty page", []map[string]any{}, 2, "?limit=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != micePath {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				resp := objectList("example.io/v2", "Mouse", tc.items)
				resp["metadata"] = map[string]any{
					"continue": "next-page", "resourceVersion": "123", "remainingItemCount": 11,
				}
				writeLookupJSON(t, w, resp)
			})
			out, err := readObjects(t, c, "example.io/v2", "Mouse", map[string]any{"limit": tc.limit})
			require.NoError(t, err)
			want := append([]map[string]any{}, tc.items...)
			if len(want) > 0 {
				want[0] = map[string]any{
					"apiVersion": "example.io/v2", "kind": "Mouse",
					"metadata": zebra["metadata"], "spec": zebra["spec"], "status": zebra["status"],
				}
			}
			require.Equal(t, &ObjectsDataSourceOutput{Objects: &want}, out)
			requireLookupCalls(t, calls(), "GET "+micePath+tc.query)
		})
	}
}

func TestObjectsListFailures(t *testing.T) {
	resource := schema.GroupResource{Resource: "configmaps"}
	for _, tc := range []struct {
		name       string
		status     *apierrors.StatusError
		options    map[string]any
		call       string
		wantError  string
		checkError func(error) bool
	}{
		{name: "List not found", status: apierrors.NewNotFound(resource, ""),
			call: "GET " + configMapsPath},
		{name: "List forbidden", status: apierrors.NewForbidden(resource, "", nil),
			call: "GET " + configMapsPath, wantError: "list Kubernetes ConfigMap",
			checkError: apierrors.IsForbidden},
		{name: "invalid selector", status: apierrors.NewBadRequest("bad label selector"),
			options:   map[string]any{"label-selector": "tier in ("},
			call:      "GET " + configMapsPath + "?labelSelector=tier+in+%28",
			wantError: "bad label selector", checkError: apierrors.IsBadRequest},
		{name: "invalid list response", call: "GET " + configMapsPath,
			wantError: "cannot unmarshal"},
		{name: "invalid namespace", options: map[string]any{"namespace": "ignored/value"},
			wantError: "invalid namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				if tc.status != nil {
					tc.status.ErrStatus.APIVersion = "v1"
					tc.status.ErrStatus.Kind = "Status"
					w.WriteHeader(int(tc.status.ErrStatus.Code))
					writeLookupJSON(t, w, tc.status.ErrStatus)
					return
				}
				list := objectList("v1", "ConfigMap", nil)
				list["items"] = "cannot decode as items"
				writeLookupJSON(t, w, list)
			})
			out, err := readObjects(t, c, "v1", "ConfigMap", tc.options)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, out)
				if tc.checkError != nil {
					require.True(t, tc.checkError(err), "%T: %v", err, err)
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, &ObjectsDataSourceOutput{}, out)
			}
			var wantCalls []string
			if tc.call != "" {
				wantCalls = []string{tc.call}
			}
			requireLookupCalls(t, calls(), wantCalls...)
		})
	}
}

func TestObjectsDiscoveryFailures(t *testing.T) {
	for _, tc := range []struct {
		name, version, kind, discoveryFailure string
		configError                           bool
		wantError                             string
		checkError                            func(error) bool
	}{
		{name: "invalid configuration", version: "v1", kind: "ConfigMap",
			configError: true, wantError: "load Kubernetes configuration"},
		{name: "genuine no match", version: "v1", kind: "Missing",
			wantError: "discover Kubernetes kind", checkError: meta.IsNoMatchError},
		{name: "requested version forbidden", version: "example.io/v2", kind: "Mouse",
			discoveryFailure: "/apis/example.io/v2", wantError: "discover Kubernetes kind",
			checkError: apierrors.IsForbidden},
		{name: "preferred version forbidden with no match", version: "example.io/", kind: "Missing",
			discoveryFailure: "/apis/example.io/v1", wantError: "discover Kubernetes kind",
			checkError: apierrors.IsForbidden},
		{name: "available fallback", version: "example.io/", kind: "Mouse",
			discoveryFailure: "/apis/example.io/v1"},
		{name: "unrelated failure", version: "example.io/v2", kind: "Mouse",
			discoveryFailure: "/apis/example.io/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []map[string]any{}
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, req *http.Request) {
				writeLookupJSON(t, w, objectList("example.io/v2", "Mouse", items))
			}, tc.discoveryFailure)
			if tc.configError {
				c.Kubeconfig = new(filepath.Join(os.Getenv("GOTMPDIR"), "absent-objects-kubeconfig"))
			}
			out, err := readObjects(t, c, tc.version, tc.kind, nil)
			var wantCalls []string
			if tc.wantError == "" {
				require.NoError(t, err)
				require.Equal(t, &ObjectsDataSourceOutput{Objects: &items}, out)
				wantCalls = []string{"GET " + micePath}
			} else {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, out)
				if tc.checkError != nil {
					require.True(t, tc.checkError(err), "%T: %v", err, err)
				}
			}
			if tc.configError {
				require.Empty(t, calls())
			} else {
				requireLookupCalls(t, calls(), wantCalls...)
			}
		})
	}
}

func TestObjectsCancellation(t *testing.T) {
	for _, phase := range []string{"discovery", "list"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := make(chan string, 1)
			block := func(w http.ResponseWriter, req *http.Request) {
				started <- req.URL.Path
				<-req.Context().Done()
			}
			c, calls := objectInfoServer(t, block)
			wantPath := configMapsPath
			if phase == "discovery" {
				server := httptest.NewServer(http.HandlerFunc(block))
				t.Cleanup(server.Close)
				c.Server = &server.URL
				wantPath = "/api"
			}
			finished := make(chan error, 1)
			go func() {
				d := &ObjectsDataSource{APIVersion: "v1", Kind: "ConfigMap"}
				_, err := d.Read(ctx, c)
				finished <- err
			}()
			select {
			case path := <-started:
				cancel()
				require.Equal(t, wantPath, path)
			case <-ctx.Done():
				t.Fatal("Kubernetes request did not start")
			}
			select {
			case err := <-finished:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("read did not return after cancellation")
			}
			if phase == "list" {
				requireLookupCalls(t, calls(), "GET "+configMapsPath)
			}
		})
	}
}

func readObjects(
	t *testing.T, c *config.Configuration, version, kind string, options map[string]any,
) (*ObjectsDataSourceOutput, error) {
	t.Helper()
	input := map[string]any{"api-version": version, "kind": kind}
	for name, value := range options {
		input[name] = value
	}
	var d ObjectsDataSource
	require.NoError(t, structdecode.Decode(&d, input))
	return d.Read(t.Context(), c)
}

func objectList(version, kind string, items []map[string]any) map[string]any {
	return map[string]any{"apiVersion": version, "kind": kind + "List", "items": items}
}
