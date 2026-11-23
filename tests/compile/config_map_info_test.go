package compile_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCompiledConfigMapInfoFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-config-map-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	fixtures, err := filepath.Glob("testdata/ub/config-map-info/*/factory.ub")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)
	for _, fixture := range fixtures {
		name := filepath.Base(filepath.Dir(fixture))
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			factory, err := filepath.Abs(fixture)
			require.NoError(t, err)
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: factory, OutDir: work, StackName: "config-map-info-test", Version: "v0.0.0",
				CLIVersion: "v0.13.0-a.11", GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: name == "valid", Stdout: &log, Stderr: &log,
			})
			if !strings.HasPrefix(name, "valid") && name != "optional-name" {
				require.Error(t, err, "%s", log.String())
				if strings.HasPrefix(name, "invalid-") {
					require.Contains(t, err.Error()+log.String(), "constraints[")
				}
				return
			}
			require.NoError(t, err, "%s", log.String())
			if name != "valid" {
				return
			}
			require.True(t, result.Built)
			body, err := os.ReadFile(filepath.Join(filepath.Dir(fixture), "config.ub"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
			for _, args := range [][]string{{"pin", "-c", "config.ub"}, {"validate", "-c", "config.ub"}} {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
				cmd.Dir = work
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
		})
	}
}

func TestCompiledConfigMapInfoInputs(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-config-map-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "evaluated-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err := filepath.Abs("testdata/ub/config-map-info-inputs")
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "config-map-info-inputs", Version: "v0.0.0", CLIVersion: "v0.13.0-a.11",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	for _, name := range []string{
		"valid", "invalid-name", "invalid-annotation", "invalid-label-key", "invalid-label-value",
	} {
		t.Run(name, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"},
					Data:       map[string]string{"value": "visible-data"},
					BinaryData: map[string][]byte{"bytes": {0, 255}},
				}))
			}))
			t.Cleanup(server.Close)
			body, err := os.ReadFile(filepath.Join(fixture, name+".ub"))
			require.NoError(t, err)
			body = bytes.ReplaceAll(body, []byte("CONFIG_MAP_INFO_SERVER"), []byte(server.URL))
			require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", "config.ub")
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			cmd = exec.CommandContext(t.Context(), result.BinaryPath,
				"plan", "-c", "config.ub", "-o", "plan.ubp")
			cmd.Dir = work
			out, err = cmd.CombinedOutput()
			if name != "valid" {
				require.Error(t, err, "%s", out)
				require.Contains(t, string(out), "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err, "%s", out)
			require.Equal(t, []string{"GET /api/v1/namespaces/default/configmaps/cm"}, requests)
			cmd = exec.CommandContext(t.Context(), result.BinaryPath, "apply", "plan.ubp")
			cmd.Dir = work
			out, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			cmd = exec.CommandContext(t.Context(), result.BinaryPath, "output", "-c", "config.ub")
			cmd.Dir = work
			out, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Contains(t, string(out), "visible-data")
			require.Contains(t, string(out), "AP8=")
			require.Equal(t, []string{
				"GET /api/v1/namespaces/default/configmaps/cm",
				"GET /api/v1/namespaces/default/configmaps/cm",
			}, requests)
		})
	}
}

func TestCompiledConfigMapInfoScenario(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-config-map-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "scenario-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	scenario := filepath.Join(root, "tests/integration/scenarios/core-v1-config-map-info")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(scenario, "factory.ub"), OutDir: work,
		StackName: "config-map-info-scenario", Version: "v0.0.0", CLIVersion: "v0.13.0-a.11",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	for _, name := range []string{"config.ub", "config-update.ub"} {
		body, err := os.ReadFile(filepath.Join(scenario, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, name), body, 0o600))
		for _, args := range [][]string{{"pin", "-c", name}, {"validate", "-c", name}} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}
