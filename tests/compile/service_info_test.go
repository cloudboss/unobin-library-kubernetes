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

func TestCompiledServiceInfoFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-service-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	fixtures, err := filepath.Glob("testdata/ub/service-info/*/factory.ub")
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
				FactoryPath: factory, OutDir: work, StackName: "service-info-test", Version: "v0.0.0",
				CLIVersion: "v0.13.0-a.7", GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: name == "valid", Stdout: &log, Stderr: &log,
			})
			if name != "valid" && name != "optional-name" {
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

func TestCompiledServiceInfoInputs(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-service-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "evaluated-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err := filepath.Abs("testdata/ub/service-info-inputs")
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "service-info-inputs", Version: "v0.0.0", CLIVersion: "v0.13.0-a.7",
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
				require.NoError(t, json.NewEncoder(w).Encode(&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default"},
				}))
			}))
			t.Cleanup(server.Close)
			body, err := os.ReadFile(filepath.Join(fixture, name+".ub"))
			require.NoError(t, err)
			body = bytes.ReplaceAll(body, []byte("SERVICE_INFO_SERVER"), []byte(server.URL))
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
			require.Equal(t, []string{"GET /api/v1/namespaces/default/services/svc"}, requests)
			cmd = exec.CommandContext(t.Context(), result.BinaryPath, "apply", "plan.ubp")
			cmd.Dir = work
			out, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Equal(t, []string{
				"GET /api/v1/namespaces/default/services/svc",
				"GET /api/v1/namespaces/default/services/svc",
			}, requests)
		})
	}
}
