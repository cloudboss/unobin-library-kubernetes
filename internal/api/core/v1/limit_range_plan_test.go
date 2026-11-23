package v1

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
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
const limitRangeSeedRead = "GET /api/v1/namespaces/default/configmaps/limit-seed"

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

func TestLimitRangeGeneratedFactoryInputs(t *testing.T) {
	binary := compileLimitRangeFactory(t)
	for _, tt := range []struct {
		name, target, knownType, chosenType, quantity, shape, choice string
		planErr, applyErr                                            string
	}{
		{name: "evaluated Pod requests", target: "subject", knownType: "Pod", quantity: "1",
			shape: "policy", planErr: "schema: constraints[8] (predicate)"},
		{name: "known Pod and pending quantity", target: "subject", knownType: "Pod",
			shape: "policy", planErr: "schema: constraints[8] (predicate)"},
		{name: "invalid metadata and pending quantity", target: "BAD", knownType: "Container",
			shape: "policy", planErr: "schema: constraints[1] (predicate)"},
		{name: "deferred Pod", target: "subject", chosenType: "Pod",
			shape: "policy", applyErr: "schema: constraints[8] (predicate)"},
		{name: "deferred Container", target: "subject", chosenType: "Container", shape: "policy"},
		{name: "null evaluated object", shape: "direct", choice: "null-object",
			planErr: `field "object": required but is null`},
		{name: "missing evaluated metadata", shape: "direct", choice: "missing-metadata",
			planErr: `field "object.metadata": required but not provided`},
		{name: "null evaluated metadata", shape: "direct", choice: "null-metadata",
			planErr: `field "object.metadata": required but is null`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := startLimitRangePlanAPI(t)
			work := limitRangeWorkDir(t)
			run := limitRangeFactoryRunner(t, binary, work)
			configureLimitRangeFactory(t, run, work, map[string]string{
				"LIMIT_SERVER": *api.config.Server, "LIMIT_NAME": tt.target,
				"LIMIT_KNOWN_TYPE": tt.knownType, "LIMIT_CHOSEN_TYPE": tt.chosenType,
				"LIMIT_KNOWN_QUANTITY": tt.quantity, "LIMIT_SHAPE": tt.shape, "LIMIT_CHOICE": tt.choice,
			})
			out, err := run("plan", "-c", "config.ub", "-o", "plan.ubp")
			require.Empty(t, api.requests())
			if tt.planErr != "" {
				require.Error(t, err)
				require.Contains(t, out, "resource.subject: "+tt.planErr)
				if strings.Contains(tt.planErr, "constraints[8]") {
					require.Contains(t, out, "object.spec.limit[0]")
				}
				return
			}
			require.NoError(t, err, "%s", out)
			saved := readLimitRangePlan(t, work)
			require.Equal(t, runtime.DecisionCreate, limitRangeStep(t, saved).Decision)
			require.Equal(t, map[string][]string{"object": {
				"data-source.live.data['limit-type']",
				"data-source.live.data[...]",
			}}, limitRangeStep(t, saved).UnresolvedInputs)
			out, err = run("apply", "plan.ubp")
			want := []string{
				"POST " + limitRangePath, "GET " + limitRangePath + "/limit-seed", limitRangeSeedRead,
			}
			if tt.applyErr != "" {
				require.Error(t, err)
				require.Contains(t, out, "resource.subject: "+tt.applyErr)
				require.Contains(t, out, "object.spec.limit[0]")
				require.Nil(t, api.get("subject"))
			} else {
				require.NoError(t, err, "%s", out)
				want = append(want, "POST "+limitRangePath, "GET "+limitRangeSubject,
					"GET "+limitRangePath+"/limit-seed")
				subject := api.get("subject")
				require.NotNil(t, subject)
				require.Equal(t, &LimitRangeSpecOutput{Limit: []LimitRangeItemOutput{{
					Type: "Container", Default: map[string]string{},
					DefaultRequest: map[string]string{"cpu": "500m"}, Max: map[string]string{},
					Min: map[string]string{}, MaxLimitRequestRatio: map[string]string{},
				}}}, projectLimitRangeSpec(subject.Spec))
			}
			require.Equal(t, want, api.requests())
		})
	}

	t.Run("deferred required field before replacement", func(t *testing.T) {
		api := startLimitRangePlanAPI(t)
		work := limitRangeWorkDir(t)
		run := limitRangeFactoryRunner(t, binary, work)
		values := map[string]string{
			"LIMIT_SERVER": *api.config.Server, "LIMIT_NAME": "subject",
			"LIMIT_KNOWN_TYPE": "Container", "LIMIT_CHOSEN_TYPE": "Container",
			"LIMIT_KNOWN_QUANTITY": "1", "LIMIT_SHAPE": "policy", "LIMIT_CHOICE": "unused",
		}
		configureLimitRangeFactory(t, run, work, values)
		out, err := run("plan", "-c", "config.ub", "-o", "plan.ubp")
		require.NoError(t, err, "%s", out)
		out, err = run("apply", "plan.ubp")
		require.NoError(t, err, "%s", out)
		prior := api.get("subject")
		require.NotNil(t, prior)

		values["LIMIT_NAME"] = "replacement"
		values["LIMIT_SHAPE"] = "from-seed"
		values["LIMIT_CHOICE"] = "missing-metadata"
		configureLimitRangeFactory(t, run, work, values)
		out, err = run("plan", "-c", "config.ub", "-o", "plan.ubp")
		require.NoError(t, err, "%s", out)
		step := limitRangeStep(t, readLimitRangePlan(t, work))
		require.Equal(t, runtime.DecisionReplace, step.Decision)
		require.Equal(t, map[string][]string{
			"object": {"data-source.live.data['choice']"},
		}, step.UnresolvedInputs)
		before := len(api.requests())
		out, err = run("apply", "plan.ubp")
		require.Error(t, err)
		require.Contains(t, out, `resource.subject: field "object.metadata": required but not provided`)
		require.Equal(t, []string{
			"PATCH " + limitRangePath + "/limit-seed", "GET " + limitRangePath + "/limit-seed",
			limitRangeSeedRead,
		}, api.requests()[before:])
		require.Equal(t, prior, api.get("subject"))
		require.Nil(t, api.get("replacement"))

		values["LIMIT_NAME"], values["LIMIT_SHAPE"] = "subject", "policy"
		configureLimitRangeFactory(t, run, work, values)
		out, err = run("plan", "-c", "config.ub", "-o", "plan.ubp")
		require.NoError(t, err, "%s", out)
		retry := limitRangeStep(t, readLimitRangePlan(t, work))
		require.Equal(t, runtime.DecisionNoOp, retry.Decision)
		require.Equal(t, step.PriorInputs, retry.PriorInputs)
		require.Equal(t, step.PriorOutputs, retry.PriorOutputs)
	})
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

func compileLimitRangeFactory(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: "testdata/ub/limit-range/native/factory.ub",
		OutDir:      limitRangeWorkDir(t), Build: true, StackName: "limit-range", Version: "v0.0.0",
		CLIVersion: "v0.13.0-a.10", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	return result.BinaryPath
}

func configureLimitRangeFactory(
	t *testing.T, run func(...string) (string, error), work string, values map[string]string,
) {
	t.Helper()
	raw, err := os.ReadFile("testdata/ub/limit-range/native/config.ub")
	require.NoError(t, err)
	for k, v := range values {
		raw = bytes.ReplaceAll(raw, []byte(k), []byte(v))
	}
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), raw, 0o600))
	out, err := run("pin", "-c", "config.ub")
	require.NoError(t, err, "%s", out)
}

func limitRangeFactoryRunner(t *testing.T, binary, work string) func(...string) (string, error) {
	t.Helper()
	return func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = work
		body, err := cmd.CombinedOutput()
		return string(body), err
	}
}

func readLimitRangePlan(t *testing.T, work string) *runtime.PlanFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(raw, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
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
