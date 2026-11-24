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

func TestCronJobSchemaAndFixtures(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	runtimeRoot := strings.TrimSpace(string(out))
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/batch/v1"), goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: runtimeRoot})
	require.NoError(t, err)
	require.Empty(t, warnings)
	paths, err := filepath.Glob("testdata/ub/cron-job-schema/*/factory.ub")
	require.NoError(t, err)
	for _, fixture := range paths {
		t.Run(filepath.Base(filepath.Dir(fixture)), func(t *testing.T) {
			data, err := os.ReadFile(fixture)
			require.NoError(t, err)
			file, err := syntax.ParseSource(fixture, data)
			if strings.Contains(fixture, "invalid-") && err != nil {
				return
			}
			require.NoError(t, err)
			errs := check.NewSyntax(file.Factory.Body, map[string]*runtime.Library{
				"batch": {Schema: schema},
			}, nil, "").References(nil)
			if strings.Contains(fixture, "invalid-") {
				require.Error(t, errs.Err())
				return
			}
			require.NoError(t, errs.Err())
		})
	}
	outputRoot := filepath.Join(root, "_output", "cron-job-schema")
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	directory, err := os.MkdirTemp(outputRoot, "compiled-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	scenario := filepath.Join(root, "tests/compile/testdata/ub/compiled/cron-job")
	for _, fixture := range []string{
		"testdata/ub/cron-job-schema/full",
		"testdata/ub/cron-job-schema/embedded-identities",
		scenario,
	} {
		var log bytes.Buffer
		output := filepath.Join(directory, filepath.Base(fixture))
		err = compile.Run(compile.Options{
			FactoryPath: fixture, OutDir: output, StackName: "cron-job-schema",
			GoVersion: compile.GoMajorMinor(), Version: "v0.0.0", CLIVersion: "v0.13.0",
			ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
			Build:            true, Stdout: &log, Stderr: &log,
		})
		require.NoError(t, err, "%s", log.String())
		if fixture != scenario {
			continue
		}
		for _, name := range []string{
			"config.ub", "config-update.ub", "config-api.ub", "config-api-update.ub",
		} {
			data, err := os.ReadFile(filepath.Join(fixture, name))
			require.NoError(t, err)
			configPath := filepath.Join(output, name)
			require.NoError(t, os.WriteFile(configPath, data, 0o600))
			for _, action := range []string{"pin", "validate"} {
				cmd := exec.Command(filepath.Join(output, "cron-job-schema"), action, "-c", configPath)
				cmd.Dir = output
				result, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s %s: %s", action, name, result)
			}
		}
	}
}
