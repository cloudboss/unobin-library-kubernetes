package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJobStableID(t *testing.T) {
	registration := Library().Resources["job"]
	inputs := map[string]any{
		"metadata": map[string]any{"generate-name": "example-"},
	}
	outputs := map[string]any{
		"server": "https://saved.example", "uid": "saved-uid", "name": "example-generated",
	}
	id, err := registration.StableID(inputs, outputs)
	require.NoError(t, err)
	require.Equal(t, new("https://saved.example/saved-uid"), id)
}
