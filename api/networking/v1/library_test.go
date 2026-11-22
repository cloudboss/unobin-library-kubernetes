package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIngressStableID(t *testing.T) {
	lib := Library()
	reg := lib.Resources["ingress"]
	require.NotNil(t, reg)
	id, err := reg.StableID(map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}},
		map[string]any{"server": "https://cluster", "api-version": "networking.k8s.io/v1",
			"kind": "Ingress", "name": "example", "namespace": "default", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
}
