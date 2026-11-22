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

func TestHPACompiledFixtures(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"missing-identity", "exactly-one-of"},
		{"conflicting-identity", "exactly-one-of"},
		{"empty-name", "must"},
		{"empty-prefix", "must"},
		{"invalid-metric-type", "metric type"},
		{"empty-policy", "policy must not be empty"},
		{"source-unbounded", ""},
		{"pending", ""},
		{"minimal", ""},
		{"full", ""},
		{"missing-object", "object"},
		{"null-object", "object"},
		{"missing-metadata", "metadata"},
		{"null-metadata", "metadata"},
		{"missing-spec", "spec"},
		{"null-spec", "spec"},
		{"missing-max", "max-replicas"},
		{"null-max", "expected integer, got null"},
		{"missing-ref", "scale-target-ref"},
		{"missing-kind", "kind"},
		{"missing-name", "name"},
		{"null-map-member", "expected optional(map(string))"},
		{"unknown", "unknown"},
		{"snake", "no match found"},
		{"camel", "maxReplicas"},
		{"uid", "uid"},
		{"status", "status"},
		{"missing-target-type", "type"},
		{"missing-described-version", "api-version"},
		{"null-policy-value", "value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compileHPAFixture(t, filepath.Join("testdata/ub/hpa",
				tc.name), tc.want)
		})
	}
}

func TestHPACompiledScenario(t *testing.T) {
	compileHPAFixture(t,
		"../../tests/integration/scenarios/autoscaling-v2-horizontal-pod-autoscaler", "")
}

func compileHPAFixture(t *testing.T, fixture, want string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	fixture, err = filepath.Abs(fixture)
	require.NoError(t, err)
	work := t.TempDir()
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{FactoryPath: filepath.Join(fixture,
		"factory.ub"), OutDir: work, StackName: "hpa-test", Version: "v0.0.0", CLIVersion: "v0.13.0-a.8",
		GoVersion: "1.26.2", Build: want == "", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root}, Stdout: &log, Stderr: &log})
	if want != "" {
		require.ErrorContains(t, err, want, "%s", log.String())
		return
	}
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	configs, err := filepath.Glob(filepath.Join(fixture, "config*.ub"))
	require.NoError(t, err)
	require.NotEmpty(t, configs)
	for _, path := range configs {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		name := filepath.Base(path)
		require.NoError(t, os.WriteFile(filepath.Join(work, name), body, 0o600))
		for _, args := range [][]string{{"pin", "-c", name}, {"validate", "-c", name}} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}
