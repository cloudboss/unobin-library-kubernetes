package v1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompiledSecretLookupLiterals(t *testing.T) {
	for _, tc := range []struct{ fixture, diagnostic, field string }{
		{"valid", "", ""},
		{"missing-metadata", `missing required input "metadata" on core.secret`, ""},
		{"null-metadata", "got null", ""},
		{"list-metadata", "got list(", ""},
		{"bad-scalar", "expected optional(string), got boolean", ""},
		{"bad-label-entry", "expected optional(string), got boolean", ""},
		{"bad-annotation-entry", "expected optional(string), got list(", ""},
		{"bad-binary-entry", "expected optional(string), got integer", ""},
		{"misspelled", `unknown field "generateName"`, ""},
		{"output-metadata", `unknown field "uid"`, ""},
		{"output-fields", `unknown field "data" on core.secret`, ""},
		{"empty-name", "constraints[0] (predicate)", ""},
		{"upper-name", "constraints[0] (predicate)", ""},
		{"long-name", "constraints[0] (predicate)", ""},
		{"empty-prefix", "constraints[1] (predicate)", ""},
		{"upper-prefix", "constraints[1] (predicate)", ""},
		{"long-prefix", "constraints[1] (predicate)", ""},
		{"long-masked-prefix", "constraints[1] (predicate)", ""},
		{"label-key-syntax", "constraints[2] (predicate)", "metadata.labels["},
		{"label-key-length", "constraints[2] (predicate)", "metadata.labels["},
		{"label-value-syntax", "constraints[3] (predicate)", "metadata.labels["},
		{"label-value-length", "constraints[3] (predicate)", "metadata.labels["},
		{"annotation-key-syntax", "constraints[4] (predicate)", "metadata.annotations["},
		{"annotation-key-length", "constraints[4] (predicate)", "metadata.annotations["},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			err := compileCoreInputFixture(t, "secret-lookup/literals/"+tc.fixture)
			if tc.diagnostic == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.diagnostic)
			if strings.HasPrefix(tc.diagnostic, "constraints[") {
				require.ErrorContains(t, err, "data-source.endpoint")
			}
			if tc.field != "" {
				require.ErrorContains(t, err, tc.field)
			}
			if tc.fixture == "output-fields" {
				for _, field := range []string{"type", "immutable", "id"} {
					require.ErrorContains(t, err, `unknown field "`+field+`" on core.secret`)
				}
			}
		})
	}
}
