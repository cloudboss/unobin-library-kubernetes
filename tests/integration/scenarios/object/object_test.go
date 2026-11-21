//go:build integration

package object_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type objectPrior = runtime.Prior[
	svc.ObjectResource, *svc.ObjectResourceOutput, *config.Configuration,
]

func objectAPI(t *testing.T) (dynamic.ResourceInterface, string) {
	t.Helper()
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.NewDynamic(nil)
	require.NoError(t, err)
	resource, namespace, err := api.Resolve(
		schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "")
	require.NoError(t, err)
	return resource, namespace
}

func configMap(t *testing.T) *svc.ObjectResource {
	t.Helper()
	manifest := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "unobin-object-" + strings.ToLower(rand.Text()[:10])},
		"data":     map[string]any{"value": "first", "removed": "initial"},
	}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	return &svc.ObjectResource{Manifest: string(encoded), DeleteTimeout: new("5s")}
}

func editManifest(t *testing.T, r *svc.ObjectResource, change func(map[string]any)) {
	t.Helper()
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Manifest), &manifest))
	change(manifest)
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	r.Manifest = string(encoded)
}

func TestObjectLifecycle(t *testing.T) {
	api, namespace := objectAPI(t)
	r := configMap(t)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := objectPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	require.Equal(t, namespace, created.Namespace)
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	patch := []byte(`{"metadata":{"labels":{"external":"retained"}}}`)
	_, err = api.Patch(t.Context(), created.Name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: "other"})
	require.NoError(t, err)
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	editManifest(t, r, func(m map[string]any) { m["data"] = map[string]any{"value": "updated"} })
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"value": "updated"}, live.Object["data"])
	require.Equal(t, "retained", live.GetLabels()["external"])
	p = objectPrior{Inputs: *r, Outputs: updated}
	_, err = api.Patch(t.Context(), created.Name, types.MergePatchType,
		[]byte(`{"data":{"value":"drift"}}`), metav1.PatchOptions{FieldManager: "other"})
	require.NoError(t, err)
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.NotEqual(t, updated.Values, read.Values)
	_, err = r.Update(t.Context(), nil, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	r.ForceConflicts = new(true)
	updated, err = r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	p = objectPrior{Inputs: *r, Outputs: updated}
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, updated, read)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}

func TestReplacementUIDProtection(t *testing.T) {
	api, _ := objectAPI(t)
	r := configMap(t)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := objectPrior{Inputs: *r, Outputs: created}
	require.NoError(t, r.Delete(t.Context(), nil, p))
	replacement := &unstructured.Unstructured{}
	require.NoError(t, replacement.UnmarshalJSON([]byte(r.Manifest)))
	replacement, err = api.Create(t.Context(), replacement, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		uid := replacement.GetUID()
		err := api.Delete(context.Background(), created.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &uid},
		})
		require.NoError(t, err)
	})
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorContains(t, err, "UID changed")
	r.ForceConflicts = new(true)
	editManifest(t, r, func(m map[string]any) {
		m["data"] = map[string]any{"value": "must-not-be-applied"}
	})
	_, err = r.Update(t.Context(), nil, p)
	require.Error(t, err)
	require.NoError(t, api.Delete(t.Context(), created.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: new(replacement.GetUID())},
	}))
	_, err = r.Update(t.Context(), nil, p)
	require.Error(t, err)
	_, err = api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "%v", err)
	replacement.SetUID("")
	replacement.SetResourceVersion("")
	newObject, err := api.Create(t.Context(), replacement, metav1.CreateOptions{})
	require.NoError(t, err)
	replacement = newObject
	err = r.Delete(t.Context(), nil, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, replacement.GetUID(), live.GetUID())
	require.Equal(t, "first", live.Object["data"].(map[string]any)["value"])
}

func TestDeletionWaitsForFinalizers(t *testing.T) {
	api, _ := objectAPI(t)
	r := configMap(t)
	editManifest(t, r, func(m map[string]any) {
		m["metadata"].(map[string]any)["finalizers"] = []any{"unobin.cloudboss.io/test"}
	})
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := objectPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() {
		_, err := api.Patch(context.Background(), created.Name, types.MergePatchType,
			[]byte(`{"metadata":{"finalizers":[]}}`), metav1.PatchOptions{})
		if !apierrors.IsNotFound(err) {
			require.NoError(t, err)
		}
		cleanupPrior := p
		cleanupPrior.Inputs.DeleteTimeout = new("10s")
		require.NoError(t, r.Delete(context.Background(), nil, cleanupPrior))
	})
	p.Inputs.DeleteTimeout = new("5s")
	err = r.Delete(t.Context(), nil, p)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, live.GetDeletionTimestamp())
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorContains(t, err, "being deleted")
	_, err = api.Patch(t.Context(), created.Name, types.MergePatchType,
		[]byte(`{"metadata":{"finalizers":[]}}`), metav1.PatchOptions{})
	require.NoError(t, err)
	p.Inputs.DeleteTimeout = new("5s")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, r.Delete(ctx, nil, p))
}
