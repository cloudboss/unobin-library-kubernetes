package v1

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestPVLookupCompiler(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	const metadataType = "object({ name: string  annotations: map(string)  labels: map(string) })"
	for _, tc := range []struct {
		fixture string
		api     *libraryapi.Descriptor
		errors  []string
	}{
		{fixture: "valid"},
		{fixture: "valid", api: &libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		}, errors: []string{
			"required library API 1.1 is newer than implemented API 1.0",
		}},
		{fixture: "invalid-literals", errors: []string{
			"schema: data-source.empty-name: constraints[0]",
			"schema: data-source.long-name: constraints[0]",
			"schema: data-source.invalid-name: constraints[0]",
			"schema: data-source.invalid-label-key: constraints[1]",
			"schema: data-source.invalid-label-value: constraints[2]",
			"schema: data-source.invalid-annotation-key: constraints[3]",
		}},
		{fixture: "invalid-inputs", errors: []string{
			`missing required input "metadata" on core.persistent-volume`,
			":7:17: type: type mismatch: expected " + metadataType + ", got null",
			":10:17: type: type mismatch: expected " + metadataType +
				", got list(object({ name: string }))",
			`missing required field "name"`,
			":16:25: type: type mismatch: expected string, got null",
			":19:50: type: type mismatch: expected string, got null",
			"type mismatch: expected string, got integer",
			`unknown field "namespace"`, `unknown field "generate-name"`,
			`unknown field "spec"`, `unknown field "ignore-annotations"`,
		}},
		{fixture: "invalid-outputs", errors: []string{
			`resolve: unknown field "id" on core.persistent-volume`,
			`resolve: unknown field "status" on core.persistent-volume`,
			`type: unknown field "namespace" on object(`,
			`type: unknown field "generate-name" on object(`,
		}},
	} {
		name := tc.fixture
		if tc.api != nil {
			name += "/api-1.0"
		}
		t.Run(name, func(t *testing.T) {
			var log bytes.Buffer
			_, err := compile.RunResult(compile.Options{
				FactoryPath: filepath.Join("testdata/ub/persistent-volume-lookup", tc.fixture, "factory.ub"),
				OutDir:      coreCompileDirectory(t), StackName: "persistent-volume-lookup",
				Version: "v0.0.0", CLIVersion: "v0.13.0", GoVersion: "1.26.2",
				LibraryAPIDescriptor: tc.api,
				ReplaceGoModules:     map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
				Stdout:               &log, Stderr: &log,
			})
			if tc.errors == nil {
				require.NoError(t, err, "%s", log.String())
				return
			}
			for _, message := range tc.errors {
				require.ErrorContains(t, err, message)
			}
		})
	}
}
