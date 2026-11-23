package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestStatefulSetSchemaAndFixtures(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/apps/v1"), goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	paths, err := filepath.Glob("testdata/ub/stateful-set-*/factory.ub")
	require.NoError(t, err)
	for _, fixture := range paths {
		t.Run(filepath.Base(filepath.Dir(fixture)), func(t *testing.T) {
			data, err := os.ReadFile(fixture)
			require.NoError(t, err)
			file, err := syntax.ParseSource(fixture, data)
			require.NoError(t, err)
			checker := check.NewSyntax(file.Factory.Body, map[string]*runtime.Library{"apps": {
				Schema: schema}}, nil, "")
			errs := checker.References(nil)
			if errs.Err() == nil {
				errs = checker.LiteralConstraints()
			}
			if strings.Contains(fixture, "invalid-") {
				require.Error(t, errs.Err())
				if strings.Contains(fixture, "invalid-native-name") {
					require.ErrorContains(t, errs.Err(), "constraints[")
				}
			} else {
				require.NoError(t, errs.Err())
			}
		})
	}
	outputRoot := filepath.Join(root, "_output", "stateful-set-schema")
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	directory, err := os.MkdirTemp(outputRoot, "compiled-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	var log bytes.Buffer
	err = compile.Run(compile.Options{
		ReplaceUnobin: strings.TrimSpace(string(out)),
		FactoryPath:   "testdata/ub/stateful-set-full",
		OutDir:        directory, StackName: "stateful-set-schema", GoVersion: compile.GoMajorMinor(),
		Version: "v0.0.0", CLIVersion: "dev", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root}, Build: true, Stdout: &log,
		Stderr: &log})
	require.NoError(t, err, "%s", log.String())
}

func TestStatefulSetScenarioFactoriesCompile(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	outputRoot := filepath.Join(root, "_output", "stateful-set-schema")
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	directory, err := os.MkdirTemp(outputRoot, "scenario-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	source := filepath.Join(root, "tests/compile/testdata/ub/compiled/stateful-set")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		ReplaceUnobin: strings.TrimSpace(string(out)), FactoryPath: source,
		OutDir: directory, StackName: "apps-v1-stateful-set",
		GoVersion: compile.GoMajorMinor(), Version: "v0.0.0", CLIVersion: "dev",
		ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root}, Build: true, Stdout: &log,
		Stderr: &log})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{"config.ub", "config-update.ub", "config-api.ub",
		"config-api-update.ub"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(source, name))
			require.NoError(t, err)
			path := filepath.Join(directory, name)
			require.NoError(t, os.WriteFile(path, data, 0o600))
			for _, action := range []string{"pin", "validate"} {
				command := exec.CommandContext(t.Context(), result.BinaryPath, action, "-c", path)
				command.Dir = directory
				output, err := command.CombinedOutput()
				require.NoError(t, err, "%s", output)
			}
		})
	}
}
