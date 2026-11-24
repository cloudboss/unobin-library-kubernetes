package v1

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

const endpointSliceLookupFixture = "testdata/ub/endpoint-slice-lookup"

func compileEndpointSliceLookup(
	t *testing.T, fixture string, descriptor *libraryapi.Descriptor,
) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	source, err := filepath.Abs(filepath.Join(endpointSliceLookupFixture, fixture, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	_, err = compile.RunResult(compile.Options{
		FactoryPath: source, OutDir: t.TempDir(),
		StackName: "endpoint-slice-lookup", Version: "v0.0.0", CLIVersion: "v0.13.0-a.11",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		LibraryAPIDescriptor: descriptor, Stdout: &log, Stderr: &log,
	})
	if err != nil {
		return log.String() + "\n" + err.Error(), err
	}
	return log.String(), nil
}

func TestEndpointSliceLookupDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name string
		want []string
	}{
		{name: "rules", want: []string{
			"data-source.empty-name: constraints[0] (predicate)",
			"data-source.upper-name: constraints[0] (predicate)",
			"data-source.trailing-dash: constraints[0] (predicate)",
			"data-source.empty-prefix: constraints[1] (predicate)",
			"data-source.bad-prefix: constraints[1] (predicate)",
			"data-source.bad-label-key: constraints[2] (predicate)",
			"data-source.bad-label-value: constraints[3] (predicate)",
			"data-source.bad-annotation-key: constraints[4] (predicate)",
		}},
		{name: "schema", want: []string{
			`missing required input "metadata" on discovery.endpoint-slice`,
			"got list(",
			"expected optional(string), got boolean",
			`unknown field "uid"`,
			`unknown field "address-type" on discovery.endpoint-slice`,
			`unknown field "endpoints" on discovery.endpoint-slice`,
			`unknown field "ports" on discovery.endpoint-slice`,
		}},
		{name: "excluded", want: []string{
			`unknown field "id" on discovery.endpoint-slice`,
			`unknown field "status" on discovery.endpoint-slice`,
			`unknown field "hints" on object(`,
			`unknown field "topology" on object(`,
			`unknown field "kind" on object(`,
			`unknown field "api-version" on object(`,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			diagnostics, err := compileEndpointSliceLookup(t, tt.name, nil)
			require.Error(t, err, "%s", diagnostics)
			for _, want := range tt.want {
				require.Contains(t, diagnostics, want)
			}
		})
	}
}

func TestEndpointSliceLookupCompatibility(t *testing.T) {
	descriptor := libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	diagnostics, err := compileEndpointSliceLookup(t, "rules", &descriptor)
	require.Error(t, err, "%s", diagnostics)
	var apiErr *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &apiErr, "%s", diagnostics)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, apiErr.Required)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 0}, apiErr.Implemented)
	require.NotContains(t, diagnostics, "constraints[")
}
