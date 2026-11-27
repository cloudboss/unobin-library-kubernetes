package object

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestStructuredManifest(t *testing.T) {
	for _, value := range []any{nil, "{}", true, 42, []any{}, map[string]any(nil)} {
		_, err := (&ObjectResource{Manifest: value}).object()
		require.Error(t, err, "%#v", value)
	}
	input := manifest()
	input["spec"] = map[string]any{
		"serial": int64(9007199254740993), "replicas": float64(2),
		"nested": []any{true, nil, "text", json.Number("3.5")},
	}
	obj, err := structuredManifest(input)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"serial": int64(9007199254740993), "replicas": int64(2),
		"nested": []any{true, nil, "text", float64(3.5)},
	}, obj.Object["spec"])
	obj.Object["spec"].(map[string]any)["serial"] = int64(1)
	require.Equal(t, int64(9007199254740993), input["spec"].(map[string]any)["serial"])
	for _, number := range []any{math.NaN(), math.Inf(1)} {
		input["spec"] = map[string]any{"value": number}
		_, err := structuredManifest(input)
		require.Error(t, err)
	}
}

func TestObjectMigration(t *testing.T) {
	for _, text := range []string{
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"example"}}`,
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\n",
	} {
		prior := runtime.MigrationState{
			Inputs:  map[string]any{"manifest": text, "field-manager": "custom"},
			Outputs: map[string]any{"uid": "saved", "fields": "managed", "values": map[string]any{"x": "y"}},
		}
		migrated, err := migrateObject(1, prior)
		require.NoError(t, err)
		require.Equal(t, text, prior.Inputs["manifest"])
		require.Equal(t, "custom", migrated.Inputs["field-manager"])
		require.Equal(t, prior.Outputs, migrated.Outputs)
		obj, err := (&ObjectResource{Manifest: migrated.Inputs["manifest"]}).object()
		require.NoError(t, err)
		require.Equal(t, "example", obj.GetName())
	}
	for _, text := range []any{
		nil, true, "", "[]", "{", "apiVersion: v1\n---\nkind: Pod", "kind: Pod\nkind: Job",
	} {
		_, err := migrateObject(1, runtime.MigrationState{Inputs: map[string]any{"manifest": text}})
		require.ErrorContains(t, err, "migrate")
	}
	_, err := migrateObject(0, runtime.MigrationState{})
	require.Error(t, err)
}
