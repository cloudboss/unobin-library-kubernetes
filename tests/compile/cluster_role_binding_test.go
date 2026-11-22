package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestCompiledClusterRoleBindingFixtures(t *testing.T) {
	for _, name := range []string{
		"valid", "generated", "unrestricted", "deferred", "missing-metadata", "namespace",
		"server-metadata", "missing-ref", "missing-ref-member", "missing-subject",
		"missing-subject-kind", "missing-subject-name", "null-subject-name", "null-map",
		"non-string-map", "null-regex", "unsupported", "missing-identity", "empty-name",
		"empty-prefix", "both-identities", "dot-name", "dot-prefix", "slash-name",
		"percent-prefix", "ref-group", "ref-kind", "empty-subject", "label-key", "label-value",
		"annotation-key", "label-regex", "annotation-regex",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := filepath.Join("testdata/ub/cluster-role-binding", name)
			result, work, log, err := compileClusterBinding(t, fixture, name == "valid")
			switch name {
			case "valid", "generated", "unrestricted", "deferred":
				require.NoError(t, err, "%s", log)
			default:
				require.Error(t, err, "%s", log)
				switch name {
				case "missing-identity", "empty-name", "empty-prefix", "both-identities",
					"dot-name", "dot-prefix", "slash-name", "percent-prefix", "ref-group",
					"ref-kind", "empty-subject", "label-key", "label-value", "annotation-key",
					"label-regex", "annotation-regex":
					require.ErrorContains(t, err, "constraints[")
					require.ErrorContains(t, err, "resource.binding")
				}
				return
			}
			if name == "valid" {
				require.True(t, result.Built)
				copyClusterBindingConfig(t, fixture, work, "config.ub")
				for _, command := range []string{"pin", "validate", "plan"} {
					cmd := exec.CommandContext(t.Context(), result.BinaryPath,
						command, "-c", "config.ub")
					cmd.Dir = work
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
				}
			}
		})
	}
}

func TestCompiledClusterRoleBindingEvaluatedConstraints(t *testing.T) {
	fixture := "testdata/ub/cluster-role-binding/configured"
	result, work, log, err := compileClusterBinding(t, fixture, true)
	require.NoError(t, err, "%s", log)
	for _, name := range []string{"valid", "empty-name", "group", "empty-subject"} {
		t.Run(name, func(t *testing.T) {
			file := "config-" + name + ".ub"
			copyClusterBindingConfig(t, fixture, work, file)
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", file)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			cmd = exec.CommandContext(t.Context(), result.BinaryPath, "plan", "-c", file)
			cmd.Dir = work
			out, err = cmd.CombinedOutput()
			if name == "valid" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err, "%s", out)
				require.Contains(t, string(out), "constraints[")
				require.Contains(t, string(out), "resource.binding")
			}
		})
	}
}

func TestCompiledClusterRoleBindingScenario(t *testing.T) {
	fixture := "../integration/scenarios/rbac-v1-cluster-role-binding"
	result, work, log, err := compileClusterBinding(t, fixture, true)
	require.NoError(t, err, "%s", log)
	for _, file := range []string{"config.ub", "config-update.ub"} {
		copyClusterBindingConfig(t, fixture, work, file)
		for _, command := range []string{"pin", "validate"} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, command, "-c", file)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}

func compileClusterBinding(
	t *testing.T, fixture string, build bool,
) (*compile.Result, string, string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/rbac-v1-cluster-role-binding")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err = filepath.Abs(fixture)
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "cluster-binding-test", Version: "v0", CLIVersion: "dev", GoVersion: "1.26.2",
		ReplaceUnobin: clusterRoleSDKDir(t), Build: build,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	return result, work, log.String(), err
}

func copyClusterBindingConfig(t *testing.T, fixture, work, file string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fixture, file))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, file), body, 0o600))
}
