package dynamic

import (
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledAnnotationsLiterals(t *testing.T) {
	const rule = "resource.selection: constraints["
	for _, tc := range []struct{ fixture, diagnostic string }{
		{"valid-top", ""}, {"valid-template", ""}, {"valid-both", ""}, {"valid-empty", ""},
		{"omitted", rule + "0] (at-least-one-of [annotations, template-annotations])"},
		{"null", rule + "0] (at-least-one-of [annotations, template-annotations])"},
		{"wrong-map", "got list(string)"},
		{"wrong-map-value", "expected string, got boolean"},
		{"null-map-value", "expected string, got null"},
		{"missing-version", `missing required input "api-version"`},
		{"missing-kind", `missing required input "kind"`},
		{"missing-metadata", `missing required input "metadata"`},
		{"missing-name", `missing required field "name"`},
		{"invalid-version", rule + "1]"},
		{"empty-kind", rule + "2]"},
		{"invalid-name", rule + "3]"},
		{"dot-name", rule + "3]"},
		{"dot-dot-name", rule + "3]"},
		{"slash-name", rule + "3]"},
		{"blank-manager", rule + "4]"},
		{"unicode-manager", rule + "4]"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			_, err := compileDynamicFixture(t,
				filepath.Join("testdata/ub/annotations/literals", tc.fixture, "factory.ub"),
				"dynamic-annotations", false, nil,
			)
			if tc.diagnostic == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.diagnostic)
		})
	}
}

func TestCompiledAnnotationsRequireLibraryAPI11(t *testing.T) {
	descriptor := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	_, err := compileDynamicFixture(t,
		"testdata/ub/annotations/literals/valid-top/factory.ub",
		"dynamic-annotations", false, descriptor,
	)
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Hint, "The library recommends Unobin v0.13.0")
}
