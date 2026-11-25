package dynamic

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledEnvCoreVersions(t *testing.T) {
	for _, fixture := range []string{"valid-core", "valid-leading-slash"} {
		t.Run(fixture, func(t *testing.T) {
			require.NoError(t, compileEnvFixture(t, fixture, nil))
		})
	}
}

func TestCompiledEnvLiteralDiagnostics(t *testing.T) {
	const rule = "resource.selection: constraints["
	for _, tc := range []struct{ fixture, diagnostic string }{
		{"version-missing", `missing required input "api-version"`},
		{"version-invalid", rule + "0]"}, {"version-empty", rule + "0]"},
		{"kind-invalid", rule + "1]"},
		{"name-slash", rule + "2]"}, {"name-percent", rule + "2]"},
		{"name-dot", rule + "2]"}, {"name-dot-dot", rule + "2]"},
		{"name-empty", rule + "2]"},
		{"selectors-both", rule + "3]"}, {"selectors-neither", rule + "3]"},
		{"container-blank", rule + "4]"}, {"init-unicode-blank", rule + "5]"},
		{"env-empty", rule + "6]"}, {"env-null", "got null"},
		{"env-missing", `missing required input "env"`},
		{"env-name-missing", `missing required field "name"`},
		{"env-wrong-literal", "expected optional(string), got boolean"},
		{"env-reference-list", "got list(object"},
		{"env-resource-missing", `missing required field "resource"`},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			require.ErrorContains(t, compileEnvFixture(t, tc.fixture, nil), tc.diagnostic)
		})
	}
}

func TestCompiledEnvRejectsOlderLibraryAPI(t *testing.T) {
	descriptor := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	err := compileEnvFixture(t, "valid-core", descriptor)
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Hint, "The library recommends Unobin v0.13.1.")
}

func compileEnvFixture(t *testing.T, fixture string, descriptor *libraryapi.Descriptor) error {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	_, err = compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(root, "dynamic", "testdata", "ub", "env", fixture, "factory.ub"),
		OutDir:      "-", StackName: "dynamic-env", Version: "v0.0.0", CLIVersion: "v0.13.1",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		LibraryAPIDescriptor: descriptor, Stdout: io.Discard, Stderr: io.Discard,
	})
	return err
}
