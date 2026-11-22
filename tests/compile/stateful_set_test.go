package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestCompiledStatefulSetConstraints(t *testing.T) {
	for _, name := range []string{
		"named", "generated", "missing", "empty", "conflict", "revision", "policy", "min-ready",
		"port", "claim", "retention", "restart",
	} {
		t.Run(name, func(t *testing.T) {
			err := compileFixture(t, filepath.Join("stateful-set", name))
			if name == "named" || name == "generated" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "resource.workload")
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestCompiledStatefulSetEvaluatedConstraints(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output", "tests", "compile-stateful-set")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	var log bytes.Buffer
	fixture := "testdata/ub/stateful-set/configured"
	result, err := compile.RunResult(compile.Options{
		FactoryPath: fixture, OutDir: work, StackName: "stateful-set", Version: "v0.0.0",
		CLIVersion: "dev", GoVersion: compile.GoMajorMinor(), Build: true,
		ReplaceUnobin:    strings.TrimSpace(string(module)),
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	paths, err := filepath.Glob(filepath.Join(fixture, "config-*.ub"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, source := range paths {
		name := filepath.Base(source)
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(source)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(work, name), data, 0o600))
			for _, action := range []string{"pin", "plan"} {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, action, "-c", name)
				cmd.Dir = work
				output, err := cmd.CombinedOutput()
				if action == "pin" || name == "config-named.ub" || name == "config-generated.ub" {
					require.NoError(t, err, "%s", output)
				} else {
					require.Error(t, err, "%s", output)
					require.Contains(t, string(output), "resource.workload: schema: constraints[")
				}
			}
		})
	}
}
