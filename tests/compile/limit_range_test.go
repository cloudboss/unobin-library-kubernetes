package compile_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLimitRangeLiteralInputs(t *testing.T) {
	for _, tt := range []struct{ name, diagnostic string }{
		{"named", ""},
		{"generated", ""},
		{"missing-object", `missing required input "object" on core.limit-range`},
		{"missing-metadata", `missing required field "metadata"`},
		{"missing-identity", "constraints[0]"},
		{"both", "constraints[0]"},
		{"empty-name", "constraints[1]"},
		{"empty-prefix", "constraints[2]"},
		{"invalid-name", "constraints[1]"},
		{"invalid-prefix", "constraints[2]"},
		{"invalid-label-key", "constraints[3]"},
		{"invalid-label-value", "constraints[4]"},
		{"invalid-annotation", "constraints[5]"},
		{"invalid-annotation-bound", "constraints[5]"},
		{"invalid-ignore-annotations", "constraints[6]"},
		{"invalid-ignore-labels", "constraints[7]"},
		{"numeric-quantity", "type mismatch: expected string, got integer"},
		{"list-spec", "got list(object({ limit: list(unknown) }))"},
		{"long-name", "constraints[1]"},
		{"long-prefix", "constraints[2]"},
		{"invalid-label-key-bound", "constraints[3]"},
		{"invalid-label-value-bound", "constraints[4]"},
		{"invalid-annotation-prefix-bound", "constraints[5]"},
		{"pod-request", "constraints[8]"},
		{"pod-pending-literal", "constraints[8]"},
		{"invalid-metadata-partial", "constraints[1]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := compileFixture(t, "limit-range/"+tt.name)
			if tt.diagnostic == "" {
				require.NoError(t, err)
				return
			}
			if strings.HasPrefix(tt.diagnostic, "constraints[") {
				require.ErrorContains(t, err, "resource.subject")
			}
			require.ErrorContains(t, err, tt.diagnostic)
		})
	}
}
