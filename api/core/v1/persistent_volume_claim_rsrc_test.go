package v1

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestPersistentVolumeClaimConfigurationReplacement(t *testing.T) {
	reg := Library().Resources["persistent-volume-claim"]
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{"name": "claim"},
		"spec": map[string]any{"access-modes": []any{}, "resources": map[string]any{}}}}
	outputs := map[string]any{"server": "server", "api-version": "v1", "kind": "PersistentVolumeClaim",
		"object": map[string]any{"metadata": map[string]any{"name": "claim", "namespace": "default",
			"uid": "uid"}, "spec": map[string]any{"access-modes": []any{}, "resources": map[string]any{}}}}
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "new"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, outputs, outputs,
			map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, reg, before, before, outputs, outputs,
		map[string]any{"namespace": "old"}, &config.Configuration{Namespace: new("new")}))
}
