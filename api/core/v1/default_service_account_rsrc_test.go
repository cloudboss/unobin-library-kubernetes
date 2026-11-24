package v1

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledDefaultServiceAccountInputs(t *testing.T) {
	for _, test := range []struct {
		fixture, diagnostic string
		native              bool
	}{
		{fixture: "missing-object", diagnostic: "object"},
		{fixture: "missing-metadata", diagnostic: "metadata"},
		{fixture: "generate-name", diagnostic: "generate-name"},
		{fixture: "metadata-uid", diagnostic: "uid"},
		{"fixed-name", "object.metadata.name must be default", true},
		{"label-key", "object.metadata.labels must have qualified keys", true},
		{"label-value", "object.metadata.labels must have valid label values", true},
		{"annotation-key", "object.metadata.annotations must have qualified keys", true},
		{"annotation-pattern", "ignore-annotations must contain valid regular expressions", true},
		{"label-pattern", "ignore-labels must contain valid regular expressions", true},
		{"timeout", "create-timeout must be a positive duration", true},
	} {
		t.Run(test.fixture, func(t *testing.T) {
			t.Parallel()
			err := compileDefaultServiceAccount(t, test.fixture, nil)
			require.ErrorContains(t, err, test.diagnostic)
			if test.native {
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestDefaultServiceAccountRequiresNativeCompilerAPI(t *testing.T) {
	old := libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	err := compileDefaultServiceAccount(t, "compatible", &old)
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
	require.NoError(t, compileDefaultServiceAccount(t, "compatible", nil))
}

func compileDefaultServiceAccount(
	t *testing.T, fixture string, descriptor *libraryapi.Descriptor,
) error {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	factory := filepath.Join("testdata/ub/default-service-account", fixture, "factory.ub")
	var log bytes.Buffer
	work := coreCompileDirectory(t)
	options := compile.Options{
		FactoryPath: factory, OutDir: work, StackName: "default-service-account",
		Version: "v0.0.0", CLIVersion: "v0.13.0",
		GoVersion: "1.26.2", LibraryAPIDescriptor: descriptor,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log, Build: descriptor == nil && fixture == "compatible",
	}
	result, err := compile.RunResult(options)
	if err != nil && log.Len() > 0 {
		t.Log(log.String())
	}
	if err != nil || !options.Build {
		return err
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = work
		return cmd.CombinedOutput()
	}
	for _, file := range []string{"config.ub", "invalid-config.ub"} {
		body, err := os.ReadFile(filepath.Join(filepath.Dir(factory), file))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, file), body, 0o600))
		output, err := run("pin", "-c", file)
		require.NoError(t, err, "%s", output)
		output, err = run("plan", "-c", file)
		if file == "invalid-config.ub" {
			require.Error(t, err, "%s", output)
			require.Contains(t, string(output), "resource.account: schema: constraints[0]")
			require.Contains(t, string(output), "object.metadata.name must be default")
		} else {
			require.NoError(t, err, "%s", output)
		}
	}
	return nil
}
