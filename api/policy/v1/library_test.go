package v1

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestPodDisruptionBudgetStableID(t *testing.T) {
	reg := Library().Resources["pod-disruption-budget"]
	id, err := reg.StableID(map[string]any{}, map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
}

func TestPodDisruptionBudgetInputsEqual(t *testing.T) {
	reg := Library().Resources["pod-disruption-budget"]
	receiver := reg.NewReceiver()
	desired := map[string]any{"metadata": map[string]any{"generate-name": "pdb-"},
		"spec": map[string]any{"min-available": "+01", "selector": map[string]any{}}}
	require.NoError(t, runtime.Decode(receiver, desired))
	equal, err := reg.InputsEqual(receiver, desired)
	require.NoError(t, err)
	require.True(t, equal)
}
