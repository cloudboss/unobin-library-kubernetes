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

func TestCompiledDaemonSetFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/apps-v1-daemon-set")
	require.NoError(t, os.MkdirAll(base, 0700))
	cases, err := os.ReadDir("testdata/ub/daemon-set")
	require.NoError(t, err)
	for _, entry := range cases {
		if entry.Name() == "configured" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			dir, err := os.MkdirTemp(base, "compile-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
			fixture, err := filepath.Abs(filepath.Join("testdata/ub/daemon-set", entry.Name(),
				"factory.ub"))
			require.NoError(t, err)
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{FactoryPath: fixture, OutDir: dir,
				StackName: "daemonset", Version: "v0", CLIVersion: "dev", ReplaceUnobin: clusterRoleSDKDir(t),
				GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root}, Build: entry.Name() == "full",
				Stdout: &log, Stderr: &log})
			if !strings.HasPrefix(entry.Name(), "invalid-") {
				require.NoError(t, err, "%s", log.String())
			} else {
				require.Error(t, err, "%s", log.String())
			}
		})
	}
}

func TestCompiledDaemonSetConfiguredConstraints(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/apps-v1-daemon-set")
	require.NoError(t, os.MkdirAll(base, 0700))
	dir, err := os.MkdirTemp(base, "configured-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	fixture := filepath.Join(root, "tests/compile/testdata/ub/daemon-set/configured")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: dir, StackName: "daemonset",
		Version:    "v0",
		CLIVersion: "dev", ReplaceUnobin: clusterRoleSDKDir(t), GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{"valid", "threshold", "nested"} {
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
			require.Contains(t, string(out), "resource.workload")
		}
	}
}

func TestCompiledDaemonSetScenario(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	scenario := filepath.Join(root, "tests/compile/testdata/ub/compiled/daemon-set")
	outputRoot := filepath.Join(root, "_output", "daemon-set-schema")
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	directory, err := os.MkdirTemp(outputRoot, "integration-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	var log bytes.Buffer
	err = compile.Run(compile.Options{
		ReplaceUnobin: strings.TrimSpace(string(out)),
		FactoryPath:   scenario, OutDir: directory, StackName: "daemon-set-integration",
		GoVersion: compile.GoMajorMinor(), Version: "v0.0.0", CLIVersion: "dev",
		ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{
		"config.ub", "config-update.ub", "config-api.ub", "config-api-update.ub",
	} {
		t.Run(name, func(t *testing.T) {
			configDir := filepath.Join(directory, name+"-inputs")
			require.NoError(t, os.Mkdir(configDir, 0o700))
			data, err := os.ReadFile(filepath.Join(scenario, name))
			require.NoError(t, err)
			configPath := filepath.Join(configDir, "config.ub")
			require.NoError(t, os.WriteFile(configPath, data, 0o600))
			for _, command := range []string{"pin", "validate"} {
				cmd := exec.Command(filepath.Join(directory, "daemon-set-integration"),
					command, "-c", configPath)
				cmd.Dir = directory
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
		})
	}
}
