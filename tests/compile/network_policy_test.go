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

func TestNetworkPolicyCompiledFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/networking-v1-network-policy")
	require.NoError(t, os.MkdirAll(base, 0o700))
	fixtures, err := os.ReadDir("testdata/ub/network-policy")
	require.NoError(t, err)
	for _, fixture := range fixtures {
		t.Run(fixture.Name(), func(t *testing.T) {
			work, err := os.MkdirTemp(base, fixture.Name()+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			path, err := filepath.Abs(filepath.Join("testdata/ub/network-policy",
				fixture.Name(), "factory.ub"))
			require.NoError(t, err)
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{FactoryPath: path, OutDir: work,
				ReplaceUnobin: ingressSDKDir(t), StackName: "network-policy-test",
				Version: "v0", CLIVersion: "dev", GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
				Build:            fixture.Name() == "valid", Stdout: &log, Stderr: &log,
			})
			switch fixture.Name() {
			case "valid", "generated", "api-validation", "deferred":
				require.NoError(t, err, "%s", log.String())
			default:
				require.Error(t, err, "%s", log.String())
				if strings.HasPrefix(fixture.Name(), "constraint-") {
					require.Contains(t, err.Error(), "constraints[")
					require.Contains(t, err.Error(), "resource.policy")
				}
			}
		})
	}
}

func TestNetworkPolicyCompiledConfigurationConstraints(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/networking-v1-network-policy")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "configured-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err := filepath.Abs("testdata/ub/network-policy-configured")
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "network-policy-configured", Version: "v0", CLIVersion: "dev",
		ReplaceUnobin: ingressSDKDir(t), GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{"valid", "empty-name", "types", "label", "regex"} {
		t.Run(name, func(t *testing.T) {
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
				require.Contains(t, string(out), "resource.policy: schema: constraints[")
			}
		})
	}
}
