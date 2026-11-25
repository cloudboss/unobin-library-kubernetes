package dynamic

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
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	labelsFixtures   = "testdata/ub/labels"
	labelsAddress    = "resource.selection"
	labelsConstraint = labelsAddress + ": schema: constraints["
	labelsCollection = "/api/v1/namespaces/default/configmaps"
)

func TestLabelsSavedNoOpAndHistoricalDestroyWithTightenedRule(t *testing.T) {
	path := filepath.Join(labelsFixtures, "known", "factory.ub")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	syntax, _, err := compile.ParseFactorySyntaxSource(path, source)
	require.NoError(t, err)
	lib := Library()
	libs := map[string]*runtime.Library{"dynamic": lib}
	store, err := local.NewStore(dynamicCompileDirectory(t), "labels", "test", encrypters.Noop{})
	require.NoError(t, err)
	api := newLabelsPlanAPI(t, "")
	t.Setenv("KUBECONFIG", filepath.Join(dynamicCompileDirectory(t), "missing"))
	exec := &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(syntax.Factory.Body, libs), SyntaxSource: &syntax.Factory.Body,
		Libraries: libs, Store: store, Inputs: map[string]any{
			"server": api.server, "configured-namespace": "other", "api-version": "v1",
			"kind": "ConfigMap", "name": "example", "field-manager": "team manager",
			"force-conflicts": false,
		},
	}
	savedPlan := func(decision runtime.Decision) *runtime.PlanFile {
		t.Helper()
		result, err := exec.Plan(t.Context())
		require.NoError(t, err)
		encoded, err := runtime.EncodePlan(result)
		require.NoError(t, err)
		plan, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		require.Equal(t, decision, labelsPlanStep(t, plan).Decision)
		return plan
	}
	_, err = exec.ApplyPlan(t.Context(), savedPlan(runtime.DecisionCreate))
	require.NoError(t, err)
	require.Equal(t, labelApplyCalls("example", "team+manager", false), api.take())
	unchanged := savedPlan(runtime.DecisionNoOp)
	require.Equal(t, []string{labelGet("example")}, api.take())
	rules, err := os.ReadFile("testdata/labels-tightened-constraints.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(rules, &lib.Constraints))
	_, err = exec.ApplyPlan(t.Context(), unchanged)
	require.ErrorContains(t, err, labelsConstraint+"0] (predicate)")
	require.Empty(t, api.take())
	_, err = exec.Plan(t.Context())
	require.ErrorContains(t, err, labelsConstraint+"0] (predicate)")
	require.Equal(t, []string{labelGet("example")}, api.take())
	exec.Destroy = true
	destroy := savedPlan(runtime.DecisionDestroy)
	require.Equal(t, []string{labelGet("example")}, api.take())
	_, err = exec.ApplyPlan(t.Context(), destroy)
	require.NoError(t, err)
	require.Equal(t, []string{labelGet("example"), labelGet("example"),
		labelPatch("example", "team+manager", false)}, api.take())
	require.Equal(t, map[string]string{}, api.labelsFor("example"))
}

func labelsPlanStep(t *testing.T, plan *runtime.PlanFile) *runtime.PlanStep {
	t.Helper()
	for i := range plan.Steps {
		if plan.Steps[i].Address == labelsAddress {
			return &plan.Steps[i]
		}
	}
	t.Fatalf("missing %s in plan", labelsAddress)
	return nil
}

type labelsPlanAPI struct {
	server, seedUID string
	take            func() []string
	mu              sync.Mutex
	objects         map[string]map[string]string
	rewrite         bool
}

func newLabelsPlanAPI(t *testing.T, seedUID string) *labelsPlanAPI {
	t.Helper()
	api := &labelsPlanAPI{
		objects: map[string]map[string]string{"example": {}, "next": {}}, seedUID: seedUID,
	}
	server, requests := newLabelsHTTP(t, func(req *http.Request) (int, any) {
		api.mu.Lock()
		defer api.mu.Unlock()
		name := filepath.Base(req.URL.Path)
		uid := "uid-" + name
		if req.Method == http.MethodPost {
			name = "seed"
			api.objects[name] = map[string]string{}
		}
		if name == "seed" {
			uid = api.seedUID
		}
		labels, exists := api.objects[name]
		if !exists {
			err := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
			return http.StatusNotFound, err.ErrStatus
		}
		if req.Method == http.MethodPatch {
			var apply struct {
				Metadata struct {
					UID, Name, Namespace string
					Labels               map[string]string
				}
			}
			if err := json.NewDecoder(req.Body).Decode(&apply); err != nil {
				t.Errorf("decode labels apply: %v", err)
			}
			if apply.Metadata.UID != uid || apply.Metadata.Name != name ||
				apply.Metadata.Namespace != "default" {
				t.Errorf("apply identity = %+v, want %s/default/%s", apply.Metadata, uid, name)
			}
			labels = apply.Metadata.Labels
			if api.rewrite && labels["managed"] != "" {
				labels["managed"] = "admitted"
			}
			api.objects[name] = labels
		}
		return http.StatusOK, map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{
				"name": name, "namespace": "default", "uid": uid, "labels": labels,
				"resourceVersion": "42",
			},
		}
	})
	api.server = server
	api.take = func() []string {
		var calls []string
		for _, call := range requests() {
			if strings.Contains(call, labelsCollection) {
				calls = append(calls, call)
			}
		}
		return calls
	}
	return api
}

func (a *labelsPlanAPI) labelsFor(name string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.objects[name]
}

func labelGet(name string) string { return "GET " + labelsCollection + "/" + name }

func labelPatch(name, manager string, force bool) string {
	forceParam := "false"
	if force {
		forceParam = "true"
	}
	return "PATCH " + labelsCollection + "/" + name + "?fieldManager=" + manager +
		"&force=" + forceParam
}

func labelApplyCalls(name, manager string, force bool) []string {
	return []string{labelGet(name), labelPatch(name, manager, force), labelGet(name)}
}

func newLabelsHTTP(
	t *testing.T, handle func(*http.Request) (int, any),
) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		status, body := http.StatusOK, any(nil)
		switch req.URL.Path {
		case "/api":
			body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			body = metav1.APIGroupList{}
		case "/api/v1":
			body = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			}}
		default:
			mu.Lock()
			calls = append(calls, req.Method+" "+req.URL.RequestURI())
			mu.Unlock()
			status, body = handle(req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("write Kubernetes response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		result := calls
		calls = nil
		return result
	}
}
