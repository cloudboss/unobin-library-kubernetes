package v1

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestSecretStableID(t *testing.T) {
	reg := Library().Resources["secret"]
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://saved", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
}

func TestSecretReplacementMatrix(t *testing.T) {
	reg := Library().Resources["secret"]
	for _, immutable := range []bool{false, true} {
		for _, field := range []string{"name", "generate-name", "namespace", "type", "data",
			"binary-data", "immutable", "annotations", "labels"} {
			t.Run(field+"/immutable="+stringBool(immutable), func(t *testing.T) {
				before := map[string]any{"object": map[string]any{
					"metadata": map[string]any{}, "immutable": immutable,
				}}
				object := map[string]any{"metadata": map[string]any{}, "immutable": immutable}
				desired := map[string]any{"object": object}
				metadata := object["metadata"].(map[string]any)
				want := false
				switch field {
				case "name", "generate-name", "namespace":
					metadata[field] = "changed"
					want = true
				case "type":
					object[field] = "custom"
					want = true
				case "data", "binary-data":
					object[field] = map[string]any{"key": "YQ=="}
					want = immutable
				case "immutable":
					object[field] = !immutable
					want = immutable
				case "annotations", "labels":
					metadata[field] = map[string]any{"key": "value"}
				}
				reasons := replacementReasons(t, reg, desired, before, nil, nil, nil, nil)
				if want {
					require.Equal(t, []string{"object"}, reasons)
				} else {
					require.Empty(t, reasons)
				}
			})
		}
	}
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{}}}
	desired := map[string]any{"object": map[string]any{"metadata": map[string]any{
		"namespace": "default", "annotations": map[string]any{}, "labels": map[string]any{},
	}, "binary-data": map[string]any{}, "immutable": false, "type": "Opaque"},
		"wait-for-service-account-token": true, "create-timeout": "60s",
		"ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, desired))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	desired["object"].(map[string]any)["data"] = map[string]any{}
	require.NoError(t, runtime.Decode(receiver, desired))
	equal, err = reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.False(t, equal)
	outputs := map[string]any{"server": "https://saved", "object": map[string]any{
		"metadata":  map[string]any{"name": "generated", "namespace": "default", "uid": "uid"},
		"immutable": false,
	}}
	observed := map[string]any{"server": "https://saved", "object": map[string]any{
		"metadata":  map[string]any{"name": "generated", "namespace": "default", "uid": "uid"},
		"immutable": true,
	}}
	require.Equal(t, []string{"object.immutable"},
		replacementReasons(t, reg, before, before, outputs, observed, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "new"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, nil, nil,
			map[string]any{field: "old"}, current))
	}
}

func stringBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
