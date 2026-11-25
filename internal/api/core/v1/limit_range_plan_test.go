package v1

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const limitRangePath = "/api/v1/namespaces/default/limitranges"
const limitRangeSubject = limitRangePath + "/subject"

func TestLimitRangeReplacementPlans(t *testing.T) {
	executor, api, dir := limitRangeExecutor(t)
	firstConfig := filepath.Join(dir, "first.kubeconfig")
	secondConfig := filepath.Join(dir, "second.kubeconfig")
	conf := clientcmdapi.Config{
		CurrentContext: "first",
		Contexts: map[string]*clientcmdapi.Context{
			"first":  {Cluster: "test", AuthInfo: "test"},
			"second": {Cluster: "test", AuthInfo: "test"},
		},
		Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: *api.config.Server}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {}},
	}
	require.NoError(t, clientcmd.WriteToFile(conf, firstConfig))
	require.NoError(t, clientcmd.WriteToFile(conf, secondConfig))
	executor.Inputs["kubeconfig"] = firstConfig
	executor.Inputs["context"] = "first"
	executor.Inputs["object"] = limitRangeInput(map[string]any{"generate-name": "subject-"}, nil)
	applyLimitRange(t, executor)
	require.Equal(t, []string{
		"POST " + limitRangePath, "GET " + limitRangePath + "/subject-actual",
	}, api.requests())
	require.Equal(t, types.UID("initial-uid"), api.get("subject-actual").UID)

	base := maps.Clone(executor.Inputs)
	for _, tt := range []struct {
		name, field string
		value       any
		decision    runtime.Decision
	}{
		{"unchanged generated", "object", limitRangeInput(
			map[string]any{"generate-name": "subject-"}, nil), runtime.DecisionNoOp},
		{"explicit default namespace", "object", limitRangeInput(
			map[string]any{"generate-name": "subject-", "namespace": "default"}, nil),
			runtime.DecisionNoOp},
		{"generated prefix", "object", limitRangeInput(
			map[string]any{"generate-name": "other-"}, nil), runtime.DecisionReplace},
		{"namespace", "object", limitRangeInput(
			map[string]any{"generate-name": "subject-", "namespace": "custom"}, nil),
			runtime.DecisionReplace},
		{"server", "server", "https://different.invalid", runtime.DecisionReplace},
		{"kubeconfig", "kubeconfig", secondConfig, runtime.DecisionReplace},
		{"context", "context", "second", runtime.DecisionReplace},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executor.Inputs = maps.Clone(base)
			executor.Inputs[tt.field] = tt.value
			before := len(api.requests())
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, tt.decision, limitRangeStep(t, pvcSavedPlan(t, plan)).Decision)
			require.Equal(t, []string{"GET " + limitRangePath + "/subject-actual"},
				api.requests()[before:])
		})
	}
}

func TestLimitRangeReplacementQuantityPreflightAndRecreation(t *testing.T) {
	executor, api, _ := limitRangeExecutor(t)
	executor.Inputs["object"] = limitRangeInput(map[string]any{"name": "subject"}, nil)
	applyLimitRange(t, executor)
	prior := api.get("subject")
	step := limitRangeInput(map[string]any{"name": "renamed"}, map[string]any{
		"limit": []any{map[string]any{"min": map[string]any{"cpu": "bad-quantity"}}},
	})
	executor.Inputs["object"] = step
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	saved := pvcSavedPlan(t, plan)
	require.Equal(t, runtime.DecisionReplace, limitRangeStep(t, saved).Decision)
	before := len(api.requests())
	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "object.spec.limit[0].min")
	require.ErrorContains(t, err, "quantity cpu=")
	require.Empty(t, api.requests()[before:])
	require.Equal(t, prior, api.get("subject"))
	preserved, err := executor.Store.Current()
	require.NoError(t, err)
	require.Equal(t, snapshot.Find("resource.subject"), preserved.Find("resource.subject"))

	api.mu.Lock()
	delete(api.objects, "subject")
	api.uid = "recreated-uid"
	api.mu.Unlock()
	executor.Inputs["object"] = limitRangeInput(map[string]any{"name": "subject"}, nil)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	saved = pvcSavedPlan(t, plan)
	require.Equal(t, runtime.DecisionCreate, limitRangeStep(t, saved).Decision)
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET " + limitRangeSubject, "POST " + limitRangePath, "GET " + limitRangeSubject,
	}, api.requests()[before:])
	require.Equal(t, types.UID("recreated-uid"), api.get("subject").UID)
}

func TestLimitRangeHistoricalConstraintsAndSavedNoOp(t *testing.T) {
	executor, api, _ := limitRangeExecutor(t)
	lib := executor.Libraries["core"]
	key := "resource.limit-range"
	native := lib.Constraints[key]
	delete(lib.Constraints, key)
	executor.Inputs["object"] = limitRangeInput(map[string]any{"name": "subject"}, map[string]any{
		"limit": []any{map[string]any{"type": "Pod",
			"default-request": map[string]any{"cpu": "500m"}}},
	})
	applyLimitRange(t, executor)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	saved := pvcSavedPlan(t, plan)
	require.Equal(t, runtime.DecisionNoOp, limitRangeStep(t, saved).Decision)
	before := len(api.requests())
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Empty(t, api.requests()[before:])

	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	saved = pvcSavedPlan(t, plan)
	require.Equal(t, runtime.DecisionNoOp, limitRangeStep(t, saved).Decision)
	lib.Constraints[key] = native
	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	before = len(api.requests())
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "resource.subject: schema: constraints[8] (predicate)")
	require.ErrorContains(t, err, "object.spec.limit[0]")
	require.Empty(t, api.requests()[before:])
	preserved, err := executor.Store.Current()
	require.NoError(t, err)
	require.Equal(t, snapshot.Find("resource.subject"), preserved.Find("resource.subject"))

	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject: schema: constraints[8] (predicate)")
	require.ErrorContains(t, err, "object.spec.limit[0]")
	require.NotNil(t, api.get("subject"))

	executor.Destroy = true
	before = len(api.requests())
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"GET " + limitRangeSubject}, api.requests()[before:])
	saved = pvcSavedPlan(t, plan)
	require.Equal(t, runtime.DecisionDestroy, limitRangeStep(t, saved).Decision)
	before = len(api.requests())
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, []string{"GET " + limitRangeSubject, "DELETE " + limitRangeSubject},
		api.requests()[before:])
	require.Nil(t, api.get("subject"))
}

func TestLimitRangeObservedDefaultsAndVolatileMetadataNoOp(t *testing.T) {
	executor, api, _ := limitRangeExecutor(t)
	executor.Inputs["object"] = limitRangeInput(map[string]any{"name": "subject"}, map[string]any{
		"limit": []any{map[string]any{"type": "Container", "default": map[string]any{"cpu": "0.5"}}},
	})
	applyLimitRange(t, executor)
	prior := api.get("subject")
	require.Equal(t, map[string]string{"cpu": "500m"},
		pvcQuantityStrings(prior.Spec.Limits[0].DefaultRequest))
	api.mu.Lock()
	api.objects["subject"].ResourceVersion = "2"
	api.objects["subject"].Generation = 9
	api.mu.Unlock()
	before := len(api.requests())
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	saved := pvcSavedPlan(t, plan)
	require.Equal(t, runtime.DecisionNoOp, limitRangeStep(t, saved).Decision)
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, []string{"GET " + limitRangeSubject}, api.requests()[before:])
}

func limitRangeInput(metadata, spec map[string]any) map[string]any {
	out := map[string]any{"metadata": metadata}
	if spec != nil {
		out["spec"] = spec
	}
	return out
}

func limitRangeExecutor(t *testing.T) (*runtime.Executor, *limitRangePlanAPI, string) {
	t.Helper()
	api := startLimitRangePlanAPI(t)
	raw, err := os.ReadFile("testdata/ub/limit-range/plan/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", raw)
	require.NoError(t, err)
	r := LimitRangeResource{}
	library := &runtime.Library{
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"limit-range": runtime.MakeResource[
				LimitRangeResource, *LimitRangeResourceOutput, *config.Configuration,
			](r.ResourceDefinition()),
		},
	}
	addNativeCoreConstraints(t, library)
	libs := map[string]*runtime.Library{"core": library}
	dir := limitRangeWorkDir(t)
	store, err := local.NewStore(dir, "limit-range", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Inputs: map[string]any{
			"server": *api.config.Server, "kubeconfig": nil, "context": nil,
		},
		Store: store, Factory: state.FactoryInfo{Name: "limit-range", Version: "v0"},
	}, api, dir
}

func applyLimitRange(t *testing.T, executor *runtime.Executor) {
	t.Helper()
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, plan))
	require.NoError(t, err)
}

func limitRangeStep(t *testing.T, plan *runtime.PlanFile) runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			return step
		}
	}
	require.FailNow(t, "plan has no LimitRange subject")
	return runtime.PlanStep{}
}

func limitRangeWorkDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs("../../../../_output/tests/core-v1-limit-range")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(base, 0o700))
	dir, err := os.MkdirTemp(base, "factory-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return dir
}

type limitRangePlanAPI struct {
	config  *config.Configuration
	mu      sync.Mutex
	objects map[string]*corev1.LimitRange
	calls   []string
	uid     types.UID
}

func startLimitRangePlanAPI(t *testing.T) *limitRangePlanAPI {
	t.Helper()
	api := &limitRangePlanAPI{objects: map[string]*corev1.LimitRange{}, uid: "initial-uid"}
	api.config = configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, req.Method+" "+req.URL.Path)
		name := filepath.Base(req.URL.Path)
		live := api.objects[name]
		if req.Method != http.MethodPost && live == nil {
			namespaceStatus(t, w, http.StatusNotFound)
			return
		}
		if strings.Contains(req.URL.Path, "/configmaps/") {
			require.Equal(t, http.MethodGet, req.Method)
			require.Equal(t, "limit-seed", name)
			respond(t, w, &corev1.ConfigMap{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: live.UID},
				Data:       live.Annotations,
			})
			return
		}
		switch req.Method {
		case http.MethodPost:
			live = &corev1.LimitRange{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(live))
			if live.Name == "" {
				live.Name = live.GenerateName + "actual"
			}
			if api.objects[live.Name] != nil {
				namespaceStatus(t, w, http.StatusConflict)
				return
			}
			live.UID = api.uid
			live.Kind, live.APIVersion = "LimitRange", "v1"
			for i := range live.Spec.Limits {
				item := &live.Spec.Limits[i]
				if item.Type == corev1.LimitTypeContainer && item.DefaultRequest == nil {
					item.DefaultRequest = item.Default.DeepCopy()
				}
			}
			api.objects[live.Name] = live
		case http.MethodPatch:
			raw, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(raw)
			require.NoError(t, err)
			old, err := json.Marshal(live)
			require.NoError(t, err)
			body, err := patch.Apply(old)
			require.NoError(t, err)
			live = &corev1.LimitRange{}
			require.NoError(t, json.Unmarshal(body, live))
			api.objects[live.Name] = live
		case http.MethodDelete:
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, &metav1.Preconditions{UID: &live.UID}, options.Preconditions)
			delete(api.objects, name)
			respond(t, w, metav1.Status{Status: metav1.StatusSuccess})
			return
		case http.MethodGet:
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		respond(t, w, live)
	})
	return api
}

func (api *limitRangePlanAPI) requests() []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]string(nil), api.calls...)
}

func (api *limitRangePlanAPI) get(name string) *corev1.LimitRange {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.objects[name].DeepCopy()
}
