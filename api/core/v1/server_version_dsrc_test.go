package v1

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledServerVersionInputs(t *testing.T) {
	err := compileServerVersion(t, "testdata/ub/server-version/arguments/factory.ub", nil)
	require.ErrorContains(t, err, `unknown field "version" on core.server-version`)
	require.ErrorContains(t, err, `unknown field "server" on core.server-version`)
	require.ErrorContains(t, err, `unknown field "metadata" on core.server-version`)
}

func TestCompiledServerVersionHasNoID(t *testing.T) {
	err := compileServerVersion(t, "testdata/ub/server-version/id/factory.ub", nil)
	require.ErrorContains(t, err, `unknown field "id"`)
}

func TestCompiledServerVersionScenario(t *testing.T) {
	const factory = "../../../tests/integration/scenarios/core-v1/factory.ub"
	require.NoError(t, compileServerVersion(t, factory, nil))
	old := libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	err := compileServerVersion(t, factory, &old)
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
}

func compileServerVersion(
	t *testing.T, fixture string, descriptor *libraryapi.Descriptor,
) error {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	factory, err := filepath.Abs(fixture)
	require.NoError(t, err)
	var log bytes.Buffer
	_, err = compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: coreCompileDirectory(t),
		StackName: "server-version-test", Version: "v0.0.0", CLIVersion: "v0.13.1",
		GoVersion: "1.26.2", LibraryAPIDescriptor: descriptor,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	if err != nil && log.Len() > 0 {
		t.Log(log.String())
	}
	return err
}
