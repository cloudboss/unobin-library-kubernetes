package compile_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestCompiledNamespaceInfoScenario(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-namespace-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "scenario-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	scenario := filepath.Join(root, "tests/compile/testdata/ub/compiled/namespace-lookup")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(scenario, "factory.ub"), OutDir: work,
		StackName: "namespace-info-scenario", Version: "v0.0.0", CLIVersion: "v0.13.0-a.11",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	requests := map[string]int{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests[req.Method+" "+req.URL.RequestURI()]++
		require.Equal(t, http.MethodGet, req.Method)
		require.Empty(t, req.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path == "/api/v1/namespaces/unobin-it-namespace-info-absent" {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
				Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
			}))
			return
		}
		require.Contains(t, []string{"/api/v1/namespaces/default", "/api/v1/namespaces/kube-public"},
			req.URL.Path)
		obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "kube-public", UID: "empty", ResourceVersion: "1",
		}}
		if req.URL.Path == "/api/v1/namespaces/default" {
			obj.Name, obj.UID = "default", "stable"
			obj.Annotations = map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "all"}
			obj.Labels = map[string]string{"kubernetes.io/metadata.name": "default"}
			obj.Spec.Finalizers = []corev1.FinalizerName{"z.example/final", "a.example/final"}
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	}))
	defer server.Close()
	kubeconfig := filepath.Join(work, "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "fixture",
		Clusters:       map[string]*clientcmdapi.Cluster{"fixture": {Server: server.URL}},
		Contexts: map[string]*clientcmdapi.Context{
			"fixture": {Cluster: "fixture", Namespace: "other"},
		},
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)
	for _, name := range []string{"config.ub", "config-update.ub"} {
		body, err := os.ReadFile(filepath.Join(scenario, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, name), body, 0o600))
		for _, args := range [][]string{
			{"pin", "-c", name}, {"validate", "-c", name},
			{"plan", "-c", name, "-o", "plan.ubp"}, {"apply", "plan.ubp"},
		} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
			cmd.Dir = work
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
		body, err = os.ReadFile(filepath.Join(work, "plan.ubp"))
		require.NoError(t, err)
		plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
			return encrypters.Noop{}, nil
		})
		require.NoError(t, err)
		value := "first"
		if name == "config-update.ub" {
			value = "updated"
		}
		observed := map[string]map[string]any{}
		for _, step := range plan.Steps {
			if step.Kind == runtime.NodeDataSource {
				observed[step.Address] = step.ObservedOutputs
			}
		}
		metadata := func(name, uid, version string, annotations, labels map[string]any) map[string]any {
			return map[string]any{"name": name, "uid": uid, "resource-version": version,
				"generation": int64(0), "annotations": annotations, "labels": labels}
		}
		require.Equal(t, map[string]map[string]any{
			"data-source.existing": {
				"metadata": metadata("default", "stable", "1",
					map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "all"},
					map[string]any{"kubernetes.io/metadata.name": "default"}),
				"spec": map[string]any{"finalizers": []any{"z.example/final", "a.example/final"}},
			},
			"data-source.empty": {
				"metadata": metadata("kube-public", "empty", "1", map[string]any{}, map[string]any{}),
				"spec":     map[string]any{"finalizers": []any{}},
			},
			"data-source.missing": {
				"metadata": metadata("unobin-it-namespace-info-absent", "", "",
					map[string]any{"lookup": value}, map[string]any{}), "spec": nil,
			},
		}, observed)
	}
	expected := map[string]int{
		"GET /api/v1/namespaces/default":                         4,
		"GET /api/v1/namespaces/kube-public":                     4,
		"GET /api/v1/namespaces/unobin-it-namespace-info-absent": 4,
	}
	mu.Lock()
	require.Equal(t, expected, requests)
	mu.Unlock()
	for _, args := range [][]string{
		{"plan", "--destroy", "-c", "config-update.ub", "-o", "destroy.ubp"},
		{"apply", "destroy.ubp"},
	} {
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	mu.Lock()
	require.Equal(t, expected, requests)
	mu.Unlock()
}
