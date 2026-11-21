package object

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func manifest() string {
	return `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"example"},
		"data":{"value":"first"}}`
}

func changeManifest(t *testing.T, r *ObjectResource, change func(map[string]any)) {
	t.Helper()
	obj, err := decodeManifest(r.Manifest)
	require.NoError(t, err)
	change(obj.Object)
	encoded, err := obj.MarshalJSON()
	require.NoError(t, err)
	r.Manifest = string(encoded)
}

func testAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			body = metav1.APIGroupList{}
		case "/api/v1":
			body = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			}}
		default:
			handle(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, Namespace: new("team")}
}

func TestObjectLifecycle(t *testing.T) {
	var stored *unstructured.Unstructured
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/namespaces/team/configmaps/example", req.URL.Path)
		switch req.Method {
		case http.MethodPatch:
			require.Equal(t, "application/apply-patch+yaml", req.Header.Get("Content-Type"))
			require.Equal(t, "unobin", req.URL.Query().Get("fieldManager"))
			require.Equal(t, "false", req.URL.Query().Get("force"))
			obj := &unstructured.Unstructured{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(&obj.Object))
			if stored != nil {
				require.Equal(t, stored.GetUID(), obj.GetUID())
			}
			obj.SetUID("managed-uid")
			obj.SetManagedFields([]metav1.ManagedFieldsEntry{{
				Manager: "unobin", Operation: metav1.ManagedFieldsOperationApply,
				FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{
					Raw: []byte(`{"f:data":{"f:value":{}}}`),
				},
			}})
			stored = obj
		case http.MethodGet:
			if stored == nil {
				w.WriteHeader(http.StatusNotFound)
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404,
				}))
				return
			}
		case http.MethodDelete:
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, types.UID("managed-uid"), *options.Preconditions.UID)
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored.Object))
	})
	r := &ObjectResource{Manifest: manifest()}
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "managed-uid", created.UID)
	require.Equal(t, "team", created.Namespace)
	p := prior{Inputs: *r, Outputs: created, Configuration: c}
	r.Manifest = manifest()
	changeManifest(t, r, func(m map[string]any) {
		m["metadata"].(map[string]any)["name"] = "different"
	})
	observed, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, created, observed)
	stored.Object["data"].(map[string]any)["value"] = "drift"
	observed, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.NotEqual(t, created.Values, observed.Values)
	r.Manifest = manifest()
	changeManifest(t, r, func(m map[string]any) { m["data"] = map[string]any{"value": "updated"} })
	updated, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, map[string]any{`["f:data","f:value"]`: "updated"}, updated.Values)
	stored.SetUID("replacement-uid")
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID")
	stored.SetUID("managed-uid")
	require.NoError(t, r.Delete(t.Context(), c, p))
	_, err = r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	require.NoError(t, r.Delete(t.Context(), c, p))
}

func TestObjectValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*ObjectResource)
	}{
		{name: "empty", change: func(r *ObjectResource) { r.Manifest = "" }},
		{name: "kind", change: func(r *ObjectResource) {
			changeManifest(t, r, func(m map[string]any) { delete(m, "kind") })
		}},
		{name: "name", change: func(r *ObjectResource) {
			changeManifest(t, r, func(m map[string]any) { m["metadata"] = map[string]any{} })
		}},
		{name: "status", change: func(r *ObjectResource) {
			changeManifest(t, r, func(m map[string]any) { m["status"] = map[string]any{} })
		}},
		{name: "UID", change: func(r *ObjectResource) {
			changeManifest(t, r, func(m map[string]any) {
				m["metadata"].(map[string]any)["uid"] = "caller-uid"
			})
		}},
		{name: "manager", change: func(r *ObjectResource) { r.FieldManager = new("") }},
		{name: "timeout", change: func(r *ObjectResource) { r.DeleteTimeout = new("0s") }},
		{name: "multiple documents", change: func(r *ObjectResource) {
			r.Manifest += "\n---\n" + manifest()
		}},
		{name: "duplicate keys", change: func(r *ObjectResource) {
			r.Manifest = "apiVersion: v1\nkind: ConfigMap\nkind: Pod\nmetadata:\n  name: example\n"
		}},
		{name: "namespace type", change: func(r *ObjectResource) {
			changeManifest(t, r, func(m map[string]any) {
				m["metadata"].(map[string]any)["namespace"] = true
			})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &ObjectResource{Manifest: manifest()}
			tt.change(r)
			err := r.ResourceDefinition().Validate(context.Background(), *r, nil)
			require.Error(t, err)
		})
	}
}

func TestManifestFormats(t *testing.T) {
	obj, err := decodeManifest("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\n")
	require.NoError(t, err)
	require.Equal(t, "example", obj.GetName())
}
