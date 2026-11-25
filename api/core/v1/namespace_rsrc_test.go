package v1

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestNamespaceStableID(t *testing.T) {
	reg := Library().Resources["namespace"]
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://cluster", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
}

func TestNamespacePlanning(t *testing.T) {
	reg := Library().Resources["namespace"]
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{}}}
	normalized := map[string]any{"object": map[string]any{"metadata": map[string]any{
		"annotations": map[string]any{}, "labels": map[string]any{},
	}}, "wait-for-default-service-account": false, "delete-timeout": "5m",
		"ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, normalized))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	for _, change := range []string{
		"name", "generate-name", "annotations", "labels",
		"wait-for-default-service-account", "delete-timeout",
	} {
		metadata := map[string]any{}
		desired := map[string]any{"object": map[string]any{"metadata": metadata}}
		switch change {
		case "name", "generate-name":
			metadata[change] = "changed"
		case "annotations", "labels":
			metadata[change] = map[string]any{"key": "value"}
		case "wait-for-default-service-account":
			desired[change] = true
		case "delete-timeout":
			desired[change] = "1s"
		}
		reasons := replacementReasons(t, reg, desired, before, nil, nil, nil, nil)
		if change == "name" || change == "generate-name" {
			require.Equal(t, []string{"object"}, reasons)
		} else {
			require.Empty(t, reasons)
		}
	}
	generated := map[string]any{"object": map[string]any{
		"metadata": map[string]any{"generate-name": "prefix-"},
	}}
	outputs := map[string]any{"server": "https://saved", "api-version": "v1", "kind": "Namespace",
		"object": map[string]any{"metadata": map[string]any{"name": "prefix-actual", "uid": "uid"}}}
	require.Empty(t, replacementReasons(t, reg, generated, generated, outputs, outputs, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, nil, nil,
			map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, reg, before, before, nil, nil,
		map[string]any{"namespace": "old"}, &config.Configuration{Namespace: new("new")}))
}
