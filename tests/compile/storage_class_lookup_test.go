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

func TestCompiledStorageClassInfoLiterals(t *testing.T) {
	version := storageClassInfoUnobinVersion(t)
	for _, tc := range []struct {
		name       string
		rule       string
		constraint bool
	}{
		{"valid", "", false},
		{"optional", "", false},
		{"empty-name", "metadata.name", true},
		{"uppercase-name", "metadata.name", true},
		{"underscore-name", "metadata.name", true},
		{"endpoint-name", "metadata.name", true},
		{"long-name", "metadata.name", true},
		{"label-syntax", "metadata.labels keys", true},
		{"label-prefix", "metadata.labels keys", true},
		{"label-suffix", "metadata.labels keys", true},
		{"label-value", "metadata.labels values", true},
		{"long-label-value", "metadata.labels values", true},
		{"annotation-syntax", "metadata.annotations keys", true},
		{"annotation-prefix", "metadata.annotations keys", true},
		{"annotation-suffix", "metadata.annotations keys", true},
		{"topologies", "allowed-topologies", true},
		{"missing-metadata", "metadata", false},
		{"output-only", "provisioner", false},
		{"computed-metadata", "uid", false},
		{"null-list-entry", "expected string, got null", false},
		{"wrong-fallback-type", "expected optional(string), got integer", false},
		{"excluded-generated-name", "generate-name", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join("testdata/ub/storage-class", "literal", tc.name)
			_, _, log, err := compileStorageClassInfo(t, version, fixture, false)
			if tc.rule == "" {
				require.NoError(t, err, "%s", log)
				return
			}
			require.ErrorContains(t, err, tc.rule, "%s", log)
			if tc.constraint {
				require.ErrorContains(t, err, "data-source.class")
				require.ErrorContains(t, err, "constraints[")
			}
			for _, entry := range map[string][]string{
				"label-syntax":      {"x/y/z", "Example.org/good", "-start"},
				"label-value":       {"metadata.labels['a']", "metadata.labels['b']"},
				"annotation-syntax": {"_omitted", "x/-bad", "é"},
			}[tc.name] {
				require.ErrorContains(t, err, entry)
			}
		})
	}
}

func storageClassInfoUnobinVersion(t *testing.T) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Version}}",
		"github.com/cloudboss/unobin")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func compileStorageClassInfo(
	t *testing.T, version, fixture string, build bool,
) (*compile.Result, string, string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/storage-v1-storage-class-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err = filepath.Abs(fixture)
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "storage-class-info-test", Version: "v0", CLIVersion: version,
		GoVersion: "1.26.2", Build: build,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	return result, work, log.String(), err
}

func TestCompiledStorageClassInfoScenario(t *testing.T) {
	fixture := "testdata/ub/compiled/storage-class-lookup"
	result, work, log, err := compileStorageClassInfo(t, storageClassInfoUnobinVersion(t),
		fixture, true)
	require.NoError(t, err, "%s", log)
	require.True(t, result.Built)
	for _, config := range []string{"config.ub", "config-update.ub"} {
		copyStorageClassConfig(t, fixture, work, config)
		for _, command := range []string{"pin", "validate"} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, command, "-c", config)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}
