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

func TestCompiledStorageClassInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule string
	}{
		{"valid", ""}, {"generated", ""},
		{"deferred-metadata", "exactly one"}, {"deferred-topologies", "allowed-topologies"},
		{"missing-metadata", "metadata"}, {"missing-provisioner", "provisioner"},
		{"non-string-provisioner", "expected string, got integer"}, {"namespace", "namespace"},
		{"missing-identity", "exactly one"}, {"both-identities", "exactly one"},
		{"empty-name", "metadata.name"}, {"invalid-name", "metadata.name"},
		{"long-name", "metadata.name"}, {"empty-prefix", "metadata.generate-name"},
		{"invalid-prefix", "metadata.generate-name"}, {"long-prefix", "metadata.generate-name"},
		{"label-key", "metadata.labels keys"}, {"label-value", "metadata.labels values"},
		{"annotation-key", "metadata.annotations keys"}, {"reclaim-policy", "reclaim-policy"},
		{"binding-mode", "volume-binding-mode"}, {"topologies", "allowed-topologies"},
		{"annotation-regex", "ignore-annotations"}, {"label-regex", "ignore-labels"},
		{"deferred-settled-root", "reclaim-policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join("testdata/ub/storage-class", tc.name)
			_, _, log, err := compileStorageClass(t, fixture, false)
			if tc.rule == "" {
				require.NoError(t, err, "%s", log)
				return
			}
			require.ErrorContains(t, err, tc.rule, "%s", log)
			switch tc.name {
			case "missing-metadata", "missing-provisioner", "non-string-provisioner", "namespace":
			default:
				require.ErrorContains(t, err, "resource.class")
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestCompiledStorageClassEvaluatedInputs(t *testing.T) {
	fixture := "testdata/ub/storage-class/configured"
	result, work, log, err := compileStorageClass(t, fixture, true)
	require.NoError(t, err, "%s", log)
	require.True(t, result.Built)
	for _, tc := range []struct {
		name  string
		field string
	}{
		{"valid", ""}, {"name", "metadata.name"}, {"policy", "reclaim-policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := "config-" + tc.name + ".ub"
			copyStorageClassConfig(t, fixture, work, config)
			pin := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", config)
			pin.Dir = work
			out, err := pin.CombinedOutput()
			require.NoError(t, err, "%s", out)
			plan := exec.CommandContext(t.Context(), result.BinaryPath, "plan", "-c", config)
			plan.Dir = work
			out, err = plan.CombinedOutput()
			if tc.field == "" {
				require.NoError(t, err, "%s", out)
				return
			}
			require.Error(t, err)
			require.Contains(t, string(out), "constraints[")
			require.Contains(t, string(out), "resource.class")
			require.Contains(t, string(out), tc.field)
		})
	}
}

func TestCompiledStorageClassScenario(t *testing.T) {
	fixture := "../integration/scenarios/storage-v1-storage-class"
	result, work, log, err := compileStorageClass(t, fixture, true)
	require.NoError(t, err, "%s", log)
	require.True(t, result.Built)
	for _, config := range []string{"config.ub", "config-update.ub"} {
		copyStorageClassConfig(t, fixture, work, config)
		for _, command := range []string{"pin", "validate", "plan"} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, command, "-c", config)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}

func compileStorageClass(
	t *testing.T, fixture string, build bool,
) (*compile.Result, string, string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/storage-v1-storage-class")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err = filepath.Abs(fixture)
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "storage-class-test", Version: "v0", CLIVersion: "v0.13.0-a.8",
		GoVersion: "1.26.2", Build: build,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	return result, work, log.String(), err
}

func copyStorageClassConfig(t *testing.T, fixture, work, file string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fixture, file))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, file), body, 0o600))
}
