package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledPriorityClassInputs(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		rule    string
		native  bool
	}{
		{"valid", "", false},
		{"missing-metadata", "metadata", false},
		{"null-metadata", "metadata", false},
		{"missing-value", "value", false},
		{"null-value", "value", false},
		{"string-value", "expected integer", false},
		{"namespace", "namespace", false},
		{"missing-identity", "exactly one", true},
		{"both-identities", "exactly one", true},
		{"empty-name", "metadata.name", true},
		{"wrong-case-name", "metadata.name", true},
		{"long-name", "metadata.name", true},
		{"empty-prefix", "metadata.generate-name", true},
		{"wrong-case-prefix", "metadata.generate-name", true},
		{"long-prefix", "metadata.generate-name", true},
		{"long-dash-prefix", "metadata.generate-name", true},
		{"label-key", "metadata.labels keys", true},
		{"label-prefix-bound", "metadata.labels keys", true},
		{"label-name-bound", "metadata.labels keys", true},
		{"label-value", "metadata.labels values", true},
		{"label-value-bound", "metadata.labels values", true},
		{"label-type", "expected string, got boolean", false},
		{"label-null", "expected string, got null", false},
		{"annotation-key", "metadata.annotations keys", true},
		{"annotation-bound", "metadata.annotations keys", true},
		{"annotation-null", "expected string, got null", false},
		{"annotation-regex", "ignore-annotations", true},
		{"label-regex", "ignore-labels", true},
		{"label-regex-null", "expected string, got null", false},
		{"empty-policy", "preemption-policy", true},
		{"wrong-case-policy", "preemption-policy", true},
		{"unknown-policy", "preemption-policy", true},
		{"partial-metadata", "metadata.name", true},
		{"partial-ignore", "ignore-labels", true},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			fixture := filepath.Join("testdata/ub/priority-class", tc.fixture)
			_, _, log, err := compilePriorityClass(t, fixture, false, nil)
			if tc.rule == "" {
				require.NoError(t, err, "%s", log)
				return
			}
			require.ErrorContains(t, err, tc.rule, "%s", log)
			if tc.native {
				require.ErrorContains(t, err, "resource.class")
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestCompiledPriorityClassEvaluatedInputs(t *testing.T) {
	const fixture = "testdata/ub/priority-class/configured"
	result, work, log, err := compilePriorityClass(t, fixture, true, nil)
	require.NoError(t, err, "%s", log)
	for _, tc := range []struct {
		name string
		rule string
	}{
		{"valid", ""}, {"name", "metadata.name"}, {"policy", "preemption-policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := "config-" + tc.name + ".ub"
			copyPriorityClassConfig(t, fixture, work, config)
			runPriorityClassCommand(t, work, result.BinaryPath, "pin", config)
			plan := exec.CommandContext(t.Context(), result.BinaryPath, "plan", "-c", config)
			plan.Dir = work
			out, err := plan.CombinedOutput()
			if tc.rule == "" {
				require.NoError(t, err, "%s", out)
				return
			}
			require.Error(t, err)
			for _, message := range []string{"constraints[", "resource.class", tc.rule} {
				require.Contains(t, string(out), message)
			}
		})
	}
}

func compilePriorityClass(
	t *testing.T, fixture string, build bool, descriptor *libraryapi.Descriptor,
) (*compile.Result, string, string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	factory, err := filepath.Abs(filepath.Join(fixture, "factory.ub"))
	require.NoError(t, err)
	work := t.TempDir()
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work,
		StackName: "priority-class", Version: "v0", CLIVersion: "v0.13.0",
		GoVersion: "1.26.2", Build: build, LibraryAPIDescriptor: descriptor,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	return result, work, log.String(), err
}

func copyPriorityClassConfig(t *testing.T, fixture, work, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fixture, name))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, name), body, 0o600))
}

func runPriorityClassCommand(t *testing.T, work, binary, command, config string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, command, "-c", config)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestCompiledPriorityClassScenario(t *testing.T) {
	const fixture = "../integration/scenarios/scheduling-v1"
	result, work, log, err := compilePriorityClass(t, fixture, true, nil)
	require.NoError(t, err, "%s", log)
	for _, name := range []string{
		"config.ub", "config-update.ub", "config-api.ub", "config-api-update.ub",
	} {
		copyPriorityClassConfig(t, fixture, work, name)
		for _, command := range []string{"pin", "validate", "plan"} {
			runPriorityClassCommand(t, work, result.BinaryPath, command, name)
		}
	}
}

func TestCompiledPriorityClassLifecycleFixtures(t *testing.T) {
	const root = "../../internal/api/scheduling/v1/testdata/ub"
	for _, path := range []string{root, filepath.Join(root, "deferred")} {
		_, _, log, err := compilePriorityClass(t, path, false, nil)
		require.NoError(t, err, "%s", log)
	}
}

func TestCompiledPriorityClassRejectsOlderLibraryAPI(t *testing.T) {
	descriptor := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	const fixture = "testdata/ub/priority-class/valid"
	_, _, log, err := compilePriorityClass(t, fixture, false, descriptor)
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer, "%s", log)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
	require.Equal(t, libraryapi.Version{Major: 1}, newer.Implemented)
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Message, "/api/scheduling/v1 requires API 1.1")
	require.Contains(t, diagnostics[0].Hint, "The library recommends Unobin v0.13.0")
}
