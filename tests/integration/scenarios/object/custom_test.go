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

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func TestCustomObject(t *testing.T) {
	_, namespace := objectAPI(t)
	group := "object-" + strings.ToLower(rand.Text()[:10]) + ".unobin.cloudboss.io"
	manifest, err := os.ReadFile("testdata/widget-crd.yaml")
	require.NoError(t, err)
	crd := &svc.ObjectResource{
		Manifest: strings.ReplaceAll(string(manifest), "GROUP", group), DeleteTimeout: new("10s"),
	}
	definition, err := crd.Create(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, definition.Namespace)
	t.Cleanup(func() {
		require.NoError(t, crd.Delete(context.Background(), nil,
			objectPrior{Inputs: *crd, Outputs: definition}))
	})
	dynamic, err := client.NewDynamic(nil)
	require.NoError(t, err)
	definitions, _, err := dynamic.Resolve(schema.GroupVersionKind{
		Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition",
	}, "")
	require.NoError(t, err)
	err = wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, 10*time.Second, true,
		func(ctx context.Context) (bool, error) {
			obj, err := definitions.Get(ctx, definition.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			conditions, _, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
			if err != nil {
				return false, err
			}
			for _, condition := range conditions {
				entry := condition.(map[string]any)
				if entry["type"] == "Established" && entry["status"] == "True" {
					return true, nil
				}
			}
			return false, nil
		})
	require.NoError(t, err)
	encoded, err := json.Marshal(map[string]any{
		"apiVersion": group + "/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "example"},
		"spec": map[string]any{"value": "first", "members": []any{
			map[string]any{"name": "app", "value": "managed"},
		}},
	})
	require.NoError(t, err)
	r := &svc.ObjectResource{Manifest: string(encoded), DeleteTimeout: new("5s")}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := objectPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	require.Equal(t, namespace, created.Namespace)
	dynamic, err = client.NewDynamic(nil)
	require.NoError(t, err)
	api, _, err := dynamic.Resolve(schema.GroupVersionKind{
		Group: group, Version: "v1", Kind: "Widget",
	}, "")
	require.NoError(t, err)
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "server-default", live.Object["spec"].(map[string]any)["defaulted"])
	other := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1", "kind": "Widget",
		"metadata": map[string]any{"name": created.Name, "namespace": namespace},
		"spec": map[string]any{"members": []any{
			map[string]any{"name": "sidecar", "value": "external"},
		}},
	}}
	live, err = api.Apply(t.Context(), created.Name, other,
		metav1.ApplyOptions{FieldManager: "other"})
	require.NoError(t, err)
	live.Object["status"] = map[string]any{"phase": "Ready"}
	_, err = api.UpdateStatus(t.Context(), live, metav1.UpdateOptions{})
	require.NoError(t, err)
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	editManifest(t, r, func(m map[string]any) {
		m["spec"].(map[string]any)["value"] = "updated"
	})
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	p = objectPrior{Inputs: *r, Outputs: updated}
	live, err = api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, created.UID, string(live.GetUID()))
	require.Equal(t, "updated", live.Object["spec"].(map[string]any)["value"])
	require.ElementsMatch(t, []any{
		map[string]any{"name": "app", "value": "managed"},
		map[string]any{"name": "sidecar", "value": "external"},
	}, live.Object["spec"].(map[string]any)["members"])
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, updated, read)
	require.NoError(t, r.Delete(t.Context(), nil, p))
}
