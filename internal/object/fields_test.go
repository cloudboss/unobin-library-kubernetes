package object

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOwnedFieldValues(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "example", "resourceVersion": "100"},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "sidecar", "image": "injected"},
				map[string]any{"name": "app", "image": "first", "imagePullPolicy": "Always"},
			},
			"tags":   []any{"external", "managed"},
			"atomic": map[string]any{"key": "value"},
		},
		"status": map[string]any{"phase": "Running"},
	}}
	obj.SetManagedFields([]metav1.ManagedFieldsEntry{{
		Manager: "unobin", Operation: metav1.ManagedFieldsOperationApply, FieldsType: "FieldsV1",
		FieldsV1: &metav1.FieldsV1{Raw: []byte(`{
			"f:metadata":{"f:resourceVersion":{},"f:name":{}},
			"f:status":{"f:phase":{}},
			"f:spec":{
				"f:containers":{"k:{\"name\":\"app\"}":{".":{},"f:name":{},"f:image":{}}},
				"f:tags":{"v:\"managed\"":{}},"f:atomic":{}
			}
		}`)},
	}})
	fields, err := OwnedFields(obj, "unobin")
	require.NoError(t, err)
	values, err := FieldValues(obj, fields)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		`["f:spec","f:containers","k:{\"name\":\"app\"}","f:name"]`:  "app",
		`["f:spec","f:containers","k:{\"name\":\"app\"}","f:image"]`: "first",
		`["f:spec","f:tags","v:\"managed\""]`:                        "managed",
		`["f:spec","f:atomic"]`:                                      map[string]any{"key": "value"},
	}, values)
	changed := obj.DeepCopy()
	changed.SetResourceVersion("200")
	changed.Object["status"] = map[string]any{"phase": "Succeeded"}
	containers := changed.Object["spec"].(map[string]any)["containers"].([]any)
	containers[0], containers[1] = containers[1], containers[0]
	containers[1].(map[string]any)["image"] = "other"
	observed, err := FieldValues(changed, fields)
	require.NoError(t, err)
	require.Equal(t, values, observed)
	containers[0].(map[string]any)["image"] = "drift"
	changed.SetManagedFields(nil)
	observed, err = FieldValues(changed, fields)
	require.NoError(t, err)
	require.NotEqual(t, values, observed)
	delete(changed.Object, "spec")
	observed, err = FieldValues(changed, fields)
	require.NoError(t, err)
	require.Empty(t, observed)
}

func TestInvalidFieldSelection(t *testing.T) {
	_, err := FieldValues(&unstructured.Unstructured{}, "invalid")
	require.Error(t, err)
	obj := &unstructured.Unstructured{}
	obj.SetManagedFields([]metav1.ManagedFieldsEntry{{
		Manager: "unobin", Operation: metav1.ManagedFieldsOperationApply,
		FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{}}`)},
	}})
	fields, err := OwnedFields(obj, "another-manager")
	require.NoError(t, err)
	require.JSONEq(t, `{}`, fields)
}
