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

func TestCompiledClusterRoleFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/rbac-v1-cluster-role")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, name := range []string{"valid", "computed", "empty", "deferred", "missing-metadata",
		"namespace",
		"server-metadata", "unsupported", "missing-verbs", "null-verbs", "null-map",
		"null-selector-values"} {
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			fixture, err := filepath.Abs(filepath.Join("testdata/ub/cluster-role", name))
			require.NoError(t, err)
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{FactoryPath: filepath.Join(fixture,
				"factory.ub"),
				OutDir: work, StackName: "cluster-role-test", Version: "v0.0.0", CLIVersion: "dev",
				ReplaceUnobin: clusterRoleSDKDir(t),
				GoVersion:     "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root}, Build: name == "valid",
				Stdout: &log, Stderr: &log})
			if name != "valid" && name != "computed" && name != "empty" && name != "deferred" {
				require.Error(t, err, "%s", log.String())
				return
			}
			require.NoError(t, err, "%s", log.String())
			if name != "valid" {
				return
			}
			require.True(t, result.Built)
			body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
			for _, args := range [][]string{{"pin", "-c", "config.ub"}, {"validate", "-c", "config.ub"}} {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
				cmd.Dir = work
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
		})
	}
}

func TestCompiledClusterRoleScenario(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/rbac-v1-cluster-role")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "scenario-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	scenario := filepath.Join(root, "tests/compile/testdata/ub/compiled/cluster-role")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{FactoryPath: filepath.Join(scenario,
		"factory.ub"),
		OutDir: work, StackName: "cluster-role-scenario", Version: "v0.0.0", CLIVersion: "dev",
		ReplaceUnobin: clusterRoleSDKDir(t),
		GoVersion:     "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root}, Build: true, Stdout: &log,
		Stderr: &log})
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	for _, name := range []string{"config.ub", "config-update.ub"} {
		body, err := os.ReadFile(filepath.Join(scenario, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, name), body, 0o600))
		for _, args := range [][]string{{"pin", "-c", name}, {"validate", "-c", name}} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}

func TestCompiledClusterRoleConstraints(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/rbac-v1-cluster-role")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, name := range []string{"missing-identity", "empty-name", "empty-prefix", "both-identities",
		"dot-name", "dot-prefix", "empty-api-groups", "slash-name", "percent-prefix",
		"label-key", "label-value", "annotation-key", "annotation-regex", "label-regex"} {
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			fixture, err := filepath.Abs(filepath.Join("testdata/ub/cluster-role", name))
			require.NoError(t, err)
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{FactoryPath: filepath.Join(fixture, "factory.ub"),
				OutDir: work, StackName: "cluster-role-constraints", Version: "v0", CLIVersion: "dev",
				ReplaceUnobin: clusterRoleSDKDir(t),
				GoVersion:     "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root}, Build: false, Stdout: &log,
				Stderr: &log})
			require.ErrorContains(t, err, "constraints[", "%s", log.String())
			require.Contains(t, err.Error(), "resource.reader")
		})
	}
}

func TestCompiledClusterRoleConfigurationConstraints(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/rbac-v1-cluster-role")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "configured-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err := filepath.Abs("testdata/ub/cluster-role/configured")
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{FactoryPath: filepath.Join(fixture, "factory.ub"),
		OutDir: work, StackName: "cluster-role-configured", Version: "v0", CLIVersion: "dev",
		ReplaceUnobin: clusterRoleSDKDir(t),
		GoVersion:     "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root}, Build: true, Stdout: &log,
		Stderr: &log})
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	for _, name := range []string{"valid", "empty-name", "empty-api-groups"} {
		file := "config-" + name + ".ub"
		body, err := os.ReadFile(filepath.Join(fixture, file))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, file), body, 0o600))
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", file)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		cmd = exec.CommandContext(t.Context(), result.BinaryPath, "plan", "-c", file, "-o", "plan.ubp")
		cmd.Dir = work
		out, err = cmd.CombinedOutput()
		if name == "valid" {
			require.NoError(t, err, "%s", out)
		} else {
			require.Error(t, err, "%s", out)
			require.Contains(t, string(out), "constraints")
		}
	}
}

func clusterRoleSDKDir(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
