package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScenarioLifecycle(t *testing.T) {
	initial := []string{"compile", "pin", "pin", "plan", "apply", "verify-applied"}
	updated := append(append([]string{}, initial...), "plan-update", "apply-update", "verify-updated")
	tests := []struct {
		name string
		fail string
		want []string
	}{
		{name: "success", want: append(append([]string{}, updated...),
			"destroy-plan:update/config.ub", "destroy-apply", "verify-destroyed")},
		{name: "compile failure", fail: "compile", want: []string{"compile"}},
		{name: "pin failure", fail: "pin", want: []string{"compile", "pin"}},
		{name: "plan failure", fail: "plan", want: []string{"compile", "pin", "pin", "plan"}},
		{name: "partial apply", fail: "apply", want: []string{
			"compile", "pin", "pin", "plan", "apply", "destroy-plan:config.ub", "destroy-apply",
		}},
		{name: "verification failure", fail: "verify-applied", want: append(
			append([]string{}, initial...), "destroy-plan:config.ub", "destroy-apply")},
		{name: "update plan failure", fail: "plan-update", want: append(
			append([]string{}, initial...), "plan-update", "destroy-plan:config.ub", "destroy-apply")},
		{name: "update apply failure", fail: "apply-update", want: append(
			append([]string{}, initial...), "plan-update", "apply-update",
			"destroy-plan:update/config.ub", "destroy-apply")},
		{name: "destroy failure", fail: "destroy-apply", want: append(
			append([]string{}, updated...), "destroy-plan:update/config.ub", "destroy-apply")},
		{name: "destroy plan failure", fail: "destroy-plan:update/config.ub", want: append(
			append([]string{}, updated...), "destroy-plan:update/config.ub")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			driver := filepath.Join(root, "tests", "integration", "run")
			require.NoError(t, os.MkdirAll(filepath.Dir(driver), 0o700))
			body, err := os.ReadFile("run")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(driver, body, 0o700))
			require.NoError(t, os.CopyFS(filepath.Join(root, "bin"), os.DirFS("testdata/bin")))
			require.NoError(t, os.CopyFS(
				filepath.Join(root, "tests", "integration", "scenarios", "runner"),
				os.DirFS("testdata/ub/runner")))
			trace := filepath.Join(root, "trace")
			command := exec.Command(driver, "kind")
			command.Env = append(os.Environ(),
				"PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"UNOBIN="+filepath.Join(root, "bin", "unobin"),
				"FAKE_FACTORY="+filepath.Join(root, "bin", "factory"),
				"KUBECONFIG="+filepath.Join(root, "kubeconfig"), "SCENARIO=runner",
				"TRACE="+trace, "FAIL_STEP="+tt.fail)
			output, err := command.CombinedOutput()
			if tt.fail == "" {
				require.NoError(t, err, "%s", output)
			} else {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError, "%s", output)
				expectedStatus := 42
				if strings.HasPrefix(tt.fail, "destroy") {
					expectedStatus = 1
				}
				require.Equal(t, expectedStatus, exitError.ExitCode(), "%s", output)
			}
			body, err = os.ReadFile(trace)
			require.NoError(t, err, "%s", output)
			require.Equal(t, tt.want, strings.Split(strings.TrimSpace(string(body)), "\n"))
			runs, err := filepath.Glob(filepath.Join(root, "_output", "integration", "runner-*"))
			require.NoError(t, err)
			if tt.fail == "" {
				require.Empty(t, runs)
			} else {
				require.NotEmpty(t, runs)
				if tt.fail != "compile" {
					require.FileExists(t, filepath.Join(runs[0], "runner"))
				}
			}
		})
	}
}

func TestScenarioSelection(t *testing.T) {
	tests := []struct {
		name     string
		tier     string
		config   string
		scenario string
		want     string
	}{
		{name: "live opt in", tier: "live", want: "UNOBIN_KUBERNETES_LIVE=1"},
		{name: "missing kubeconfig", tier: "kind", want: "KUBECONFIG must select"},
		{name: "unknown scenario", tier: "kind", config: "unused", scenario: "absent",
			want: "unknown scenario"},
		{name: "invalid scenario", tier: "kind", config: "unused", scenario: "../configuration",
			want: "invalid SCENARIO"},
		{name: "invalid tier", tier: "invalid", want: "usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := exec.Command("./run", tt.tier)
			command.Env = append(os.Environ(), "UNOBIN_KUBERNETES_LIVE=0",
				"KUBECONFIG="+tt.config, "SCENARIO="+tt.scenario)
			output, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), tt.want)
		})
	}
}
