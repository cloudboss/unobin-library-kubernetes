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

func TestCompiledPersistentVolumeClaimFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-persistent-volume-claim")
	require.NoError(t, os.MkdirAll(base, 0700))
	cases, err := os.ReadDir("testdata/ub/persistent-volume-claim")
	require.NoError(t, err)
	for _, entry := range cases {
		if entry.Name() == "configured" || entry.Name() == "data-source" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			dir, err := os.MkdirTemp(base, "compile-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
			fixture, err := filepath.Abs(filepath.Join("testdata/ub/persistent-volume-claim", entry.Name(),
				"factory.ub"))
			require.NoError(t, err)
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{FactoryPath: fixture, OutDir: dir,
				StackName: "claim", Version: "v0", CLIVersion: "dev", ReplaceUnobin: clusterRoleSDKDir(t),
				GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root}, Build: entry.Name() == "valid",
				Stdout: &log, Stderr: &log})
			if entry.Name() == "valid" {
				require.NoError(t, err, "%s", log.String())
			} else {
				require.Error(t, err, "%s", log.String())
				switch entry.Name() {
				case "missing-identity", "empty-name", "both-identities", "label-key", "label-value",
					"annotation-key", "bad-access", "bad-mode":
					require.ErrorContains(t, err, "constraints[")
				case "missing-object":
					require.ErrorContains(t, err, `missing required input "object"`)
				default:
					require.Contains(t, err.Error(), "type:")
				}
			}
		})
	}
}

func TestCompiledPersistentVolumeClaimScenario(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-persistent-volume-claim")
	require.NoError(t, os.MkdirAll(base, 0700))
	dir, err := os.MkdirTemp(base, "scenario-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	scenario := filepath.Join(root, "tests/compile/testdata/ub/compiled/persistent-volume-claim")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(scenario, "factory.ub"),
		OutDir:      dir, StackName: "claim", Version: "v0", CLIVersion: "dev",
		ReplaceUnobin: clusterRoleSDKDir(t),
		GoVersion:     "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{"config.ub", "config-update.ub"} {
		body, err := os.ReadFile(filepath.Join(scenario, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0600))
		for _, args := range [][]string{{"pin", "-c", name}, {"validate", "-c", name}} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}

func TestCompiledPersistentVolumeClaimConfiguredConstraints(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-persistent-volume-claim")
	require.NoError(t, os.MkdirAll(base, 0700))
	dir, err := os.MkdirTemp(base, "configured-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	fixture := filepath.Join(root, "tests/compile/testdata/ub/persistent-volume-claim/configured")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: dir, StackName: "claim", Version: "v0",
		CLIVersion: "dev", ReplaceUnobin: clusterRoleSDKDir(t), GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{"valid", "empty-name", "bad-mode"} {
		file := "config-" + name + ".ub"
		body, err := os.ReadFile(filepath.Join(fixture, file))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), body, 0600))
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", file)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		cmd = exec.CommandContext(t.Context(), result.BinaryPath, "plan", "-c", file, "-o", name+".ubp")
		cmd.Dir = dir
		out, err = cmd.CombinedOutput()
		if name == "valid" {
			require.NoError(t, err, "%s", out)
		} else {
			require.Error(t, err)
			require.Contains(t, string(out), "constraints[")
			require.Contains(t, string(out), "resource.claim")
		}
	}
}
