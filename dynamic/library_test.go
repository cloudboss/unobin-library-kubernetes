package dynamic

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/cfg"
	"github.com/cloudboss/unobin/pkg/typecheck"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func TestLibrary(t *testing.T) {
	lib := Library()
	require.Equal(t, "kubernetes-dynamic", lib.Name)
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	require.Equal(t, reflect.TypeFor[*svc.ObjectResourceOutput](),
		lib.Resources["object"].OutputType())
	require.True(t, lib.Resources["object"].HasStableID())
	require.Equal(t, 1, lib.Resources["object"].SchemaVersion())
	receiver := lib.Resources["object"].NewReceiver().(*svc.ObjectResource)
	require.Error(t, lib.Resources["object"].ValidateInputs(t.Context(), receiver, nil))
	require.NoError(t, runtime.Decode(receiver, map[string]any{
		"manifest": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"example"}}`,
	}))
	require.NoError(t, lib.Resources["object"].ValidateInputs(t.Context(), receiver, nil))
	equal, err := lib.Resources["object"].InputsEqual(receiver, map[string]any{
		"manifest": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\n",
	})
	require.NoError(t, err)
	require.True(t, equal)
}

func TestLibrarySchema(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	view, err := cfg.View(config.LibraryConfiguration())
	require.NoError(t, err)
	require.Equal(t, view.SchemaDigest, schema.ConfigurationDigest)
	require.Equal(t, map[string]typecheck.Type{
		"manifest":        typecheck.TString(),
		"field-manager":   typecheck.TOptional(typecheck.TString()),
		"force-conflicts": typecheck.TOptional(typecheck.TBoolean()),
		"delete-timeout":  typecheck.TOptional(typecheck.TString()),
	}, schema.Resources["object"].Inputs)
	require.Equal(t, []string{"manifest"}, schema.Resources["object"].SensitiveInputs)
	require.Equal(t, typecheck.TString(), schema.Resources["object"].Outputs["api-version"])
	require.NotContains(t, schema.Resources["object"].Outputs, "apiVersion")
	require.ElementsMatch(t, []string{"fields", "values"},
		schema.Resources["object"].SensitiveOutputs)
}

func dynamicCompileDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/dynamic")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	return work
}

func compileDynamicFixture(
	t *testing.T, fixture, stack string, build bool, descriptor *libraryapi.Descriptor,
) (string, error) {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	path, err := filepath.Abs(fixture)
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: path, OutDir: dynamicCompileDirectory(t),
		StackName: stack, Version: "v0.0.0", CLIVersion: "v0.13.0",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		LibraryAPIDescriptor: descriptor, Build: build, Stdout: &log, Stderr: &log,
	})
	if err != nil {
		t.Log(log.String())
		return "", err
	}
	return result.BinaryPath, nil
}
