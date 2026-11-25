package v1

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestServiceAccountStableID(t *testing.T) {
	reg := Library().Resources["service-account"]
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://cluster", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
	_, err = reg.StableID(map[string]any{}, map[string]any{})
	require.Error(t, err)
}

func TestServiceAccountPlanning(t *testing.T) {
	reg := Library().Resources["service-account"]
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{}}}
	normalized := map[string]any{"object": map[string]any{
		"metadata": map[string]any{"namespace": "default", "annotations": map[string]any{},
			"labels": map[string]any{}}, "automount-service-account-token": true,
		"secrets": []any{}, "image-pull-secrets": []any{},
	}, "create-timeout": "30000ms", "ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, normalized))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	for _, field := range []string{"secrets", "image-pull-secrets"} {
		input := map[string]any{"object": map[string]any{"metadata": map[string]any{},
			field: []any{map[string]any{"name": "z"}, map[string]any{}, map[string]any{"name": "a"}}}}
		receiver := reg.NewReceiver()
		require.NoError(t, runtime.Decode(receiver, input))
		prior := map[string]any{"object": map[string]any{"metadata": map[string]any{},
			field: []any{map[string]any{"name": "a"}, map[string]any{"name": "z"},
				map[string]any{"name": "a"}, map[string]any{"name": ""}}}}
		equal, err := reg.InputsEqual(receiver, prior)
		require.NoError(t, err)
		require.True(t, equal)
	}
	for _, field := range []string{"name", "generate-name", "namespace"} {
		desired := map[string]any{"object": map[string]any{"metadata": map[string]any{field: "changed"}}}
		require.Equal(t, []string{"object"}, replacementReasons(t, reg, desired, before,
			nil, nil, nil, nil))
	}
	require.Empty(t, replacementReasons(t, reg, normalized, before, nil, nil, nil, nil))
	generated := map[string]any{"object": map[string]any{
		"metadata": map[string]any{"generate-name": "prefix-"},
	}}
	outputs := map[string]any{"server": "https://saved", "api-version": "v1", "kind": "ServiceAccount",
		"object": map[string]any{"metadata": map[string]any{"name": "prefix-actual", "uid": "uid"}}}
	require.Empty(t, replacementReasons(t, reg, generated, generated, outputs, outputs, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "new"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, nil, nil,
			map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, reg, before, before, nil, nil,
		map[string]any{"token": "old", "namespace": "old"},
		&config.Configuration{Token: new("new"), Namespace: new("new")}))
}
