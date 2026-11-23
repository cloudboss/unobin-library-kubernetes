package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledValidatingWebhookConfiguration(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		errors  []string
	}{
		{"valid", nil},
		{"native", []string{
			"resource.no-identity", "resource.conflicting-identity", "exactly-one-of",
			"resource.empty-list", "webhook must contain at least one item",
			"resource.bad-metadata", "invalid metadata.generate-name", "invalid metadata.labels key",
			"invalid metadata.labels value", "invalid metadata.annotations key",
			"invalid ignore-labels expression", "invalid ignore-annotations expression",
			"resource.bad-webhook-values", "failure-policy must be Fail or Ignore",
			"match-policy must be Equivalent or Exact", "invalid side-effects",
			"timeout-seconds must be between 1 and 30", "rule.api-groups must contain at least one item",
			"rule.api-versions must contain at least one item",
			"rule.resources must contain at least one item",
			"rule.operations must contain at least one item", "invalid rule operation",
		}},
		{"structural", []string{
			`missing required input "metadata"`, `missing required input "webhook"`,
			`missing required field "client-config"`, "got null", "got list(object(",
			`missing required field "name"`,
			"expected string, got null",
			`missing required field "api-groups"`,
			"expected list(string), got null",
			"11 errors:",
		}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			_, _, err := compileWebhookFixture(t, tc.fixture, tc.fixture == "valid")
			if tc.errors == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, message := range tc.errors {
				require.Contains(t, err.Error(), message)
			}
		})
	}
}

func TestCompiledValidatingWebhookConfigurationPlan(t *testing.T) {
	result, work, err := compileWebhookFixture(t, "configured", true)
	require.NoError(t, err)
	for _, tc := range []struct {
		config, message string
	}{
		{"valid", ""},
		{"empty-name", "metadata.name must not be empty"},
		{"timeout", "timeout-seconds must be between 1 and 30"},
		{"operation", "invalid rule operation"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			name := "config-" + tc.config + ".ub"
			body, err := os.ReadFile(filepath.Join(
				"testdata/ub/validating-webhook-configuration/configured", name,
			))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(work, name), body, 0o600))
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", name)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			cmd = exec.CommandContext(t.Context(), result.BinaryPath, "plan", "-c", name, "-o", "plan.ubp")
			cmd.Dir = work
			out, err = cmd.CombinedOutput()
			if tc.message == "" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err)
				require.Contains(t, string(out), "resource.example")
				require.Contains(t, string(out), tc.message)
			}
		})
	}
}

func TestCompiledValidatingWebhookConfigurationRequiresAPI11(t *testing.T) {
	prior := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	context, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: prior,
	})
	require.NoError(t, err)
	model, _, err := compile.ReadGoSchemaWithCompatibility(
		"../../api/admissionregistration/v1", context,
	)
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
	require.Nil(t, model)
}

func compileWebhookFixture(t *testing.T, fixture string, build bool) (
	*compile.Result, string, error,
) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/validating-webhook-configuration")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, fixture+"-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	var log bytes.Buffer
	factory := filepath.Join("testdata/ub/validating-webhook-configuration", fixture, "factory.ub")
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work, StackName: "webhook-test", Version: "v0", CLIVersion: "dev",
		GoVersion: "1.26.2", ReplaceUnobin: clusterRoleSDKDir(t),
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            build, Stdout: &log, Stderr: &log,
	})
	if err != nil {
		t.Log(log.String())
	}
	return result, work, err
}
