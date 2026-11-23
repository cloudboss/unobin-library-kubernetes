package compile_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompiledConfigMapDataLiterals(t *testing.T) {
	require.NoError(t, compileFixture(t, "config-map-data/valid"))
	for _, tc := range []struct{ fixture, detail string }{
		{"missing-object", "object"},
		{"missing-metadata", "metadata"},
		{"missing-name", "name"},
		{"null-name", "expected string"},
		{"missing-data", "data"},
		{"null-data", "expected map"},
		{"wrong-value", "expected string"},
		{"unknown-field", "immutable"},
		{"empty-name", "constraints["},
		{"long-name", "constraints["},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			err := compileFixture(t, "config-map-data/"+tc.fixture)
			require.ErrorContains(t, err, tc.detail)
			if tc.detail == "constraints[" {
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, "object.metadata.name")
			}
		})
	}
}
