package object_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/dynamic"
)

const (
	envBinding    = "resource.env"
	envAddress    = "resource.selection"
	envConstraint = envAddress + ": schema: constraints["

	envRuntimeWorkloadPath = "/apis/apps/v1/namespaces/default/deployments"
)

func newEnvExecutor(t *testing.T, api *envRuntimeAPI, fixture string) *runtime.Executor {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	lib := dynamic.Library()
	abs, err := filepath.Abs("../../dynamic")
	require.NoError(t, err)
	lib.Schema, _, err = goschema.Read(abs)
	require.NoError(t, err)
	env := lib.Schema.Resources["env"]
	lib.Constraints = map[string][]lang.ConstraintSpec{envBinding: env.Constraints}
	lib.Defaults = map[string][]lang.DefaultSpec{envBinding: env.Defaults}
	libs := map[string]*runtime.Library{"dynamic": lib, "core": core.Library()}
	store, err := local.NewStore(dir, "env", "test", encrypters.Noop{})
	require.NoError(t, err)
	exec := &runtime.Executor{
		Libraries: libs, Store: store, Inputs: map[string]any{
			"server": api.server, "version": "apps/v1", "kind": "deployment", "name": "example",
			"namespace": nil, "container": "main", "init-container": nil,
			"manager": "manager", "force": false,
			"env": []any{map[string]any{"name": "MANAGED", "value": "desired"}},
		},
	}
	setEnvFactory(t, exec, fixture)
	return exec
}

func setEnvFactory(t *testing.T, exec *runtime.Executor, fixture string) {
	t.Helper()
	path := filepath.Join("testdata", "ub", "env", fixture, "factory.ub")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	syntax, _, err := compile.ParseFactorySyntaxSource(path, source)
	require.NoError(t, err)
	exec.SyntaxSource = &syntax.Factory.Body
	exec.DAG = runtime.BuildSyntaxDAG(syntax.Factory.Body, exec.Libraries)
}

func saveEnvPlan(t *testing.T, exec *runtime.Executor) *runtime.PlanFile {
	t.Helper()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return saved
}

func envPlanStep(t *testing.T, plan *runtime.PlanFile) *runtime.PlanStep {
	t.Helper()
	for i := range plan.Steps {
		if plan.Steps[i].Address == envAddress {
			return &plan.Steps[i]
		}
	}
	t.Fatalf("missing %s in plan", envAddress)
	return nil
}

type envRuntimeAPI struct {
	mu                            sync.Mutex
	server, seedUID, missing, uid string
	root, rewrite                 bool
	objects                       map[string][]any
	calls                         []string
}

func newEnvAPI(t *testing.T, seedUID string, root bool) *envRuntimeAPI {
	t.Helper()
	a := &envRuntimeAPI{
		seedUID: seedUID, root: root, objects: map[string][]any{"example": {}, "next": {}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		status, body := a.respond(t, req)
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode Kubernetes response: %v", err)
		}
	}))
	a.server = server.URL
	t.Cleanup(server.Close)
	return a
}

func (a *envRuntimeAPI) respond(t *testing.T, req *http.Request) (int, any) {
	switch req.URL.Path {
	case "/api":
		return http.StatusOK, metav1.APIVersions{Versions: []string{"v1"}}
	case "/apis":
		version := metav1.GroupVersionForDiscovery{GroupVersion: "apps/v1", Version: "v1"}
		return http.StatusOK, metav1.APIGroupList{Groups: []metav1.APIGroup{{
			Name: "apps", Versions: []metav1.GroupVersionForDiscovery{version},
			PreferredVersion: version,
		}}}
	case "/api/v1":
		return http.StatusOK, metav1.APIResourceList{GroupVersion: "v1",
			APIResources: []metav1.APIResource{{Name: "configmaps", Kind: "ConfigMap",
				Namespaced: true}}}
	case "/apis/apps/v1":
		return http.StatusOK, metav1.APIResourceList{GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment",
				Namespaced: !a.root}}}
	}
	a.calls = append(a.calls, req.Method+" "+req.URL.RequestURI())
	if strings.Contains(req.URL.Path, "/configmaps") {
		return http.StatusOK, map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{
				"name": "seed", "namespace": "default", "uid": a.seedUID,
			},
		}
	}
	name := filepath.Base(req.URL.Path)
	env, exists := a.objects[name]
	if !exists || a.missing == "workload" {
		err := apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"},
			name)
		return http.StatusNotFound, err.ErrStatus
	}
	uid := a.uid
	if uid == "" {
		uid = "uid-" + name
	}
	metadata := map[string]any{"name": name, "uid": uid}
	if !a.root {
		metadata["namespace"] = "default"
	}
	if req.Method == http.MethodPatch {
		var apply map[string]any
		if err := json.NewDecoder(req.Body).Decode(&apply); err != nil {
			t.Errorf("decode apply: %v", err)
			return http.StatusBadRequest, nil
		}
		if applyUID, _, _ := unstructured.NestedString(apply, "metadata", "uid"); applyUID != uid {
			t.Errorf("apply UID %q, want %q", applyUID, uid)
		}
		list, found, err := unstructured.NestedSlice(apply, "spec", "template", "spec", "containers")
		if err != nil || !found || len(list) != 1 {
			t.Errorf("apply has invalid containers %v: %v", list, err)
			return http.StatusBadRequest, nil
		}
		env = list[0].(map[string]any)["env"].([]any)
		for _, v := range env {
			fields := v.(map[string]any)
			if fields["value"] == "" {
				delete(fields, "value")
			}
			if a.rewrite {
				fields["value"] = "admitted"
			}
		}
		a.objects[name] = env
	}
	container := map[string]any{"name": "main", "env": env}
	if a.missing == "container" {
		container["name"] = "other"
	}
	return http.StatusOK, map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": metadata,
		"spec": map[string]any{"template": map[string]any{
			"spec": map[string]any{"containers": []any{container}},
		}},
	}
}

func (a *envRuntimeAPI) set(name string, env []any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objects[name] = env
}

func (a *envRuntimeAPI) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func (a *envRuntimeAPI) path(name string) string {
	if a.root {
		return "/apis/apps/v1/deployments/" + name
	}
	return envRuntimeWorkloadPath + "/" + name
}

func (a *envRuntimeAPI) get(name string) string { return "GET " + a.path(name) }

func (a *envRuntimeAPI) apply(name, manager, force string) string {
	return "PATCH " + a.path(name) + "?fieldManager=" + manager + "&force=" + force
}

func (a *envRuntimeAPI) env(name string) []any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.objects[name]
}
