package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompiledSecretDataLiterals(t *testing.T) {
	require.NoError(t, compileCoreInputFixture(t, "secret-data/literals/valid"))
	for _, tc := range []struct{ fixture, detail string }{
		{"missing-object", "object"},
		{"null-object", "expected"},
		{"wrong-object", "expected"},
		{"missing-metadata", "metadata"},
		{"null-metadata", "expected"},
		{"wrong-metadata", "expected"},
		{"missing-name", "name"},
		{"null-name", "expected string"},
		{"wrong-name", "expected string"},
		{"missing-data", "data"},
		{"null-data", "expected map"},
		{"wrong-data", "expected map"},
		{"null-value", "expected string"},
		{"wrong-value", "expected string"},
		{"unknown-field", "type"},
		{"wrong-namespace", "expected"},
		{"wrong-manager", "expected"},
		{"wrong-force", "expected"},
		{"empty-name", "constraints["},
		{"malformed-name", "constraints["},
		{"long-name", "constraints["},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			err := compileCoreInputFixture(t, "secret-data/literals/"+tc.fixture)
			require.ErrorContains(t, err, tc.detail)
			if tc.detail == "constraints[" {
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, "object.metadata.name")
			}
		})
	}
}
