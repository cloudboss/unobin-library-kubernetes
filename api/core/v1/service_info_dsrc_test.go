package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func serviceInfoLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.service-info": schema.DataSources["service-info"].Constraints,
	}
	return lib
}

func serviceInfoExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": lib}
	store, err := local.NewStore(t.TempDir(), "service-info", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "service-info", Version: "v0", ContentRevision: "c0"},
	}
}

func serviceInfoSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func TestServiceInfoNativeInputs(t *testing.T) {
	lib := serviceInfoLibrary(t)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		valid    bool
	}{
		{name: "valid", valid: true, metadata: map[string]any{
			"name": "svc", "annotations": map[string]any{"EXAMPLE.COM/Key": "arbitrary value"},
			"labels": map[string]any{"example.com/Key": "value", "empty": ""}}},
		{name: "omitted optional maps", valid: true, metadata: map[string]any{"name": "svc"}},
		{name: "invalid name", metadata: map[string]any{"name": "Bad_Name"}},
		{name: "empty name", metadata: map[string]any{"name": ""}},
		{name: "long name", metadata: map[string]any{"name": strings.Repeat("a", 254)}},
		{name: "invalid annotation", metadata: map[string]any{
			"name": "svc", "annotations": map[string]any{"bad key": "value"}}},
		{name: "long annotation name", metadata: map[string]any{
			"name": "svc", "annotations": map[string]any{strings.Repeat("a", 64): "value"}}},
		{name: "invalid label key", metadata: map[string]any{
			"name": "svc", "labels": map[string]any{"EXAMPLE.COM/Key": "value"}}},
		{name: "invalid label value", metadata: map[string]any{
			"name": "svc", "labels": map[string]any{"app": "bad value"}}},
		{name: "long label value", metadata: map[string]any{
			"name": "svc", "labels": map[string]any{"app": strings.Repeat("a", 64)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
					Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
				}))
			}))
			t.Cleanup(server.Close)
			executor := serviceInfoExecutor(t, lib, "service-info-inputs")
			executor.Inputs = map[string]any{"server": server.URL, "metadata": tc.metadata}
			plan, err := executor.Plan(t.Context())
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"GET /api/v1/namespaces/default/services/svc"}, requests)
			for _, step := range plan.Steps {
				require.Empty(t, step.SensitiveInputs)
				require.Empty(t, step.SensitiveOutputs)
			}
			_, err = executor.ApplyPlan(t.Context(), serviceInfoSavedPlan(t, plan))
			require.NoError(t, err)
			requests = nil
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), serviceInfoSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /api/v1/namespaces/default/services/svc",
				"GET /api/v1/namespaces/default/services/svc",
			}, requests)
			executor.Inputs["metadata"] = map[string]any{"name": "Bad_Name"}
			requests = nil
			_, err = executor.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
			require.Empty(t, requests)
		})
	}
}

type serviceInfoSeed struct{ Metadata svc.ServiceLookupMetadata }
type serviceInfoSeedOutput struct{ Value svc.ServiceLookupMetadata }

func (s *serviceInfoSeed) Run(context.Context, any) (*serviceInfoSeedOutput, error) {
	return &serviceInfoSeedOutput{Value: s.Metadata}, nil
}

func TestServiceInfoDeferredInputs(t *testing.T) {
	for _, name := range []string{"svc", "Bad_Name"} {
		t.Run(name, func(t *testing.T) {
			lib := serviceInfoLibrary(t)
			lib.Actions = map[string]runtime.ActionRegistration{
				"seed": runtime.MakeAction[serviceInfoSeed, *serviceInfoSeedOutput, any](),
			}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				}))
			}))
			t.Cleanup(server.Close)
			executor := serviceInfoExecutor(t, lib, "service-info-deferred")
			executor.Inputs = map[string]any{"server": server.URL,
				"metadata": map[string]any{"name": name}}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, requests)
			_, err = executor.ApplyPlan(t.Context(), serviceInfoSavedPlan(t, plan))
			if name == "Bad_Name" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"GET /api/v1/namespaces/default/services/svc"}, requests)
		})
	}
}

func TestServiceInfoSavedInputs(t *testing.T) {
	lib := serviceInfoLibrary(t)
	rules := lib.Constraints
	lib.Constraints = nil
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "Bad_Name", Namespace: "default"},
		}))
	}))
	t.Cleanup(server.Close)
	executor := serviceInfoExecutor(t, lib, "service-info-inputs")
	executor.Inputs = map[string]any{"server": server.URL,
		"metadata": map[string]any{"name": "Bad_Name"}}
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), serviceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	file := serviceInfoSavedPlan(t, plan)
	lib.Constraints = rules
	requests = nil
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
	require.Empty(t, requests)
	_, err = executor.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
	require.Empty(t, requests)
}

func TestServiceInfoScenarioFactory(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-service-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "scenario-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	scenario := filepath.Join(root, "tests/integration/scenarios/core-v1-service-info")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(scenario, "factory.ub"), OutDir: work,
		StackName: "service-info-scenario", Version: "v0.0.0", CLIVersion: "v0.13.0-a.10",
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
		if req.URL.Path == "/api/v1/namespaces/default/services/unobin-it-service-info-absent" {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
				Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
			}))
			return
		}
		require.Equal(t, "/api/v1/namespaces/default/services/kubernetes", req.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default",
				UID: "stable", ResourceVersion: "1"},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1"},
		}))
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
	}
	expected := map[string]int{
		"GET /api/v1/namespaces/default/services/kubernetes":                    4,
		"GET /api/v1/namespaces/default/services/unobin-it-service-info-absent": 4,
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
