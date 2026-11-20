package object

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestTargetClientPreservesIdentity(t *testing.T) {
	methods := []string{}
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/namespaces/team/configmaps/example", r.URL.Path)
		methods = append(methods, r.Method)
		if r.Method == http.MethodPatch {
			require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "example", "namespace": "team", "uid": "original"},
		}))
	})
	connection, err := Connect(c, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "")
	require.NoError(t, err)
	require.Equal(t, *c.Server, connection.Server)
	require.Equal(t, "team", connection.Namespace)
	_, err = connection.Resource.Patch(t.Context(), "example", types.JSONPatchType,
		[]byte(`[{"op":"add","path":"/data","value":{"key":"value"}}]`), metav1.PatchOptions{})
	require.NoError(t, err)
	target := Target{Server: connection.Server, APIVersion: "v1", Kind: "ConfigMap",
		Name: "example", Namespace: "team", UID: "original"}
	current := &config.Configuration{Server: new("https://unused.invalid"), Namespace: new("other")}
	saved, err := TargetClient(current, target)
	require.NoError(t, err)
	obj, err := saved.Resource.Get(t.Context(), target.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, CheckUID(target, obj))
	obj.SetUID("replacement")
	require.ErrorContains(t, CheckUID(target, obj), "UID changed")
	_, err = TargetClient(c, Target{})
	require.Error(t, err)
	require.Equal(t, []string{http.MethodPatch, http.MethodGet}, methods)
	require.Equal(t, "https://unused.invalid", *current.Server)
	require.Equal(t, "other", *current.Namespace)
}

func TestDeletePreservesOptions(t *testing.T) {
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(r.Body).Decode(&options))
			require.Equal(t, types.UID("original"), *options.Preconditions.UID)
			require.Equal(t, "12", *options.Preconditions.ResourceVersion)
			require.Equal(t, metav1.DeletePropagationForeground, *options.PropagationPolicy)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
			Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
		}))
	})
	target := Target{Server: *c.Server, APIVersion: "v1", Kind: "ConfigMap",
		Name: "example", Namespace: "team", UID: "original"}
	options := metav1.DeleteOptions{
		PropagationPolicy: new(metav1.DeletePropagationForeground),
		Preconditions:     &metav1.Preconditions{ResourceVersion: new("12")},
	}
	require.NoError(t, Delete(t.Context(), c, target, options, time.Second))
	require.Nil(t, options.Preconditions.UID)
	require.Error(t, Delete(t.Context(), c, target, options, 0))
	require.Error(t, CheckUID(Target{}, &unstructured.Unstructured{}))
}
