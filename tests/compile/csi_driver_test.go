package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledCSIDriverInputs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		diagnostic string
		native     bool
	}{
		{"valid", "", false},
		{"missing-metadata", "metadata", false},
		{"namespace", "namespace", false},
		{"computed-identity", "uid", false},
		{"missing-attach", "attach-required", false},
		{"null-attach", "expected boolean, got null", false},
		{"wrong-attach-type", "expected boolean, got string", false},
		{"wrong-spec-type", "got list(unknown)", false},
		{"missing-identity", "exactly one", true},
		{"both-identities", "exactly one", true},
		{"empty-name", "metadata.name", true},
		{"invalid-name", "metadata.name", true},
		{"long-name", "metadata.name", true},
		{"empty-prefix", "metadata.generate-name", true},
		{"invalid-prefix", "metadata.generate-name", true},
		{"long-prefix", "metadata.generate-name", true},
		{"label-key", "metadata.labels keys", true},
		{"label-prefix-length", "metadata.labels keys", true},
		{"label-key-length", "metadata.labels keys", true},
		{"label-value", "metadata.labels values", true},
		{"label-value-length", "metadata.labels values", true},
		{"annotation-key", "metadata.annotations keys", true},
		{"annotation-prefix-length", "metadata.annotations keys", true},
		{"annotation-regex", "ignore-annotations", true},
		{"label-regex", "ignore-labels", true},
		{"mode", "spec.volume-lifecycle-modes", true},
		{"mode-before-pending", "spec.volume-lifecycle-modes", true},
		{"mode-after-pending", "spec.volume-lifecycle-modes", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join("testdata/ub/csi-driver", tc.name)
			_, _, log, err := compileCSIDriver(t, fixture, false, nil)
			if tc.diagnostic == "" {
				require.NoError(t, err, "%s", log)
				return
			}
			require.ErrorContains(t, err, tc.diagnostic, "%s", log)
			if tc.native {
				require.ErrorContains(t, err, "resource.driver")
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestCompiledCSIDriverRequiresLibraryAPI11(t *testing.T) {
	olderCompiler := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	fixture := "testdata/ub/csi-driver/valid"
	_, _, log, err := compileCSIDriver(t, fixture, false, olderCompiler)
	require.Error(t, err)
	diagnostic := err.Error() + log
	require.Contains(t, diagnostic, "required library API 1.1 is newer than implemented API 1.0")
}

func TestCompiledCSIDriverEvaluatedInputs(t *testing.T) {
	fixture := "testdata/ub/csi-driver/configured"
	result, work, log, err := compileCSIDriver(t, fixture, true, nil)
	require.NoError(t, err, "%s", log)
	require.True(t, result.Built)
	for _, tc := range []struct {
		name string
		rule string
	}{
		{"omitted", ""}, {"false", ""}, {"mode", "spec.volume-lifecycle-modes"},
		{"regex", "ignore-labels"}, {"name", "metadata.name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := "config-" + tc.name + ".ub"
			copyStorageClassConfig(t, fixture, work, file)
			pin := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", file)
			pin.Dir = work
			out, err := pin.CombinedOutput()
			require.NoError(t, err, "%s", out)
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, "plan", "-c", file)
			cmd.Dir = work
			out, err = cmd.CombinedOutput()
			if tc.rule == "" {
				require.NoError(t, err, "%s", out)
				return
			}
			require.Error(t, err)
			require.Contains(t, string(out), "resource.driver")
			require.Contains(t, string(out), "constraints[")
			require.Contains(t, string(out), tc.rule)
		})
	}
}

func TestCompiledCSIDriverScenario(t *testing.T) {
	fixture := "testdata/ub/compiled/csi-driver"
	result, work, log, err := compileCSIDriver(t, fixture, true, nil)
	require.NoError(t, err, "%s", log)
	require.True(t, result.Built)
	for _, file := range []string{"config.ub", "config-update.ub"} {
		copyStorageClassConfig(t, fixture, work, file)
		for _, command := range []string{"pin", "plan"} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, command, "-c", file)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}

func compileCSIDriver(
	t *testing.T, fixture string, build bool, descriptor *libraryapi.Descriptor,
) (*compile.Result, string, string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/storage-v1-csi-driver")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err = filepath.Abs(fixture)
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "csi-driver-test", Version: "v0", CLIVersion: "v0.13.0-a.11",
		GoVersion: "1.26.2", Build: build,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log, LibraryAPIDescriptor: descriptor,
	})
	return result, work, log.String(), err
}
