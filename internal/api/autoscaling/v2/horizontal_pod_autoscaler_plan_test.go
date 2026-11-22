package v2

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func hpaResourceRegistration() runtime.ResourceRegistration {
	base := runtime.MakeResource[
		HorizontalPodAutoscalerResource, *HorizontalPodAutoscalerResourceOutput,
		*config.Configuration,
	]((&HorizontalPodAutoscalerResource{}).ResourceDefinition())
	return HorizontalPodAutoscalerRegistration[HorizontalPodAutoscalerResource,
		*HorizontalPodAutoscalerResourceOutput](base, base.Read, base.KnownReplacementReasons)
}

func TestHPAStateBackedComputedValues(t *testing.T) {
	var stored *autoscalingv2.HorizontalPodAutoscaler
	var calls, credentials []string
	var patches [][]hpaPatchOperation
	creates := 0
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		credentials = append(credentials, req.Header.Get("Authorization"))
		switch req.Method {
		case "POST":
			stored = &autoscalingv2.HorizontalPodAutoscaler{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			creates++
			stored.UID = types.UID(fmt.Sprintf("uid-%d", creates))
			if stored.Name == "" {
				stored.Name = stored.GenerateName + "actual"
			}
			if len(stored.Spec.Metrics) == 0 {
				stored.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: "Resource",
					Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{Type: "Utilization", AverageUtilization: new(int32(83))}}}}
			}
			if stored.Spec.Behavior.ScaleUp == nil {
				stored.Spec.Behavior.ScaleUp = &autoscalingv2.HPAScalingRules{SelectPolicy: new(
					autoscalingv2.MaxChangePolicySelect), StabilizationWindowSeconds: new(int32(0)),
					Policies: []autoscalingv2.HPAScalingPolicy{{Type: "Pods", Value: 4, PeriodSeconds: 15}}}
			}
		case "GET":
			if stored == nil {
				hpaFailure(t, w, 404)
				return
			}
		case "PATCH":
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			var ops []hpaPatchOperation
			require.NoError(t, json.Unmarshal(body, &ops))
			patches = append(patches, ops)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			body, err = json.Marshal(stored)
			require.NoError(t, err)
			body, err = patch.Apply(body)
			require.NoError(t, err)
			stored = &autoscalingv2.HorizontalPodAutoscaler{}
			require.NoError(t, json.Unmarshal(body, stored))
		case "DELETE":
			stored = nil
			hpaRespond(t, w, metav1.Status{Status: "Success"})
			return
		default:
			t.Errorf("unexpected %s", req.Method)
		}
		hpaRespond(t, w, stored)
	})
	body, err := os.ReadFile("testdata/ub/hpa-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	constraints := hpaConstraintSpecs(t)
	dir := t.TempDir()
	var lastExecutor *runtime.Executor
	inputs := map[string]any{"server": *c.Server, "name": "test", "token": "first", "prefix": nil,
		"namespace": nil, "labels": nil, "metric": nil, "behavior": nil, "cpu": nil, "max": 4}
	plan := func(want runtime.Decision, apply bool) {
		t.Helper()
		libs := map[string]*runtime.Library{"hpa": {Name: "kubernetes-autoscaling-v2",
			Configuration: config.LibraryConfiguration(),
			Constraints: map[string][]lang.ConstraintSpec{
				"resource.horizontal-pod-autoscaler": constraints},
			Resources: map[string]runtime.ResourceRegistration{
				"horizontal-pod-autoscaler": hpaResourceRegistration()}}}
		store, err := local.NewStore(dir, "hpa", "test", encrypters.Noop{})
		require.NoError(t, err)
		executor := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs, Store: store,
			Factory: state.FactoryInfo{Name: "hpa", Version: "v0", ContentRevision: "c0"}}
		lastExecutor = executor
		p, err := executor.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.hpa" {
				require.Equal(t, want, step.Decision)
				found = true
			}
		}
		require.True(t, found)
		if apply {
			body, err := runtime.EncodePlan(p)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(body)
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), file)
			require.NoError(t, err)
		}
	}
	plan(runtime.DecisionCreate, true)
	plan(runtime.DecisionNoOp, false)
	beforeCalls := len(calls)
	inputs["name"] = nil
	_, err = lastExecutor.Plan(t.Context())
	require.ErrorContains(t, err, "exactly-one-of")
	require.Equal(t, []string{"GET"}, calls[beforeCalls:])
	inputs["name"] = "test"
	// Valid saved NoOp plans still apply without a mutation.
	plan(runtime.DecisionNoOp, true)

	legacyLib := lastExecutor.Libraries["hpa"]
	currentReg := legacyLib.Resources["horizontal-pod-autoscaler"]
	for _, decision := range []runtime.Decision{runtime.DecisionNoOp, runtime.DecisionReplace} {
		for _, tc := range []struct {
			field string
			value any
			want  string
		}{
			{"name", nil, "exactly-one-of"},
			{"metric", []any{map[string]any{"type": "Other"}}, "metric type"},
			{"behavior", map[string]any{"scale-up": []any{map[string]any{"policy": []any{}}}},
				"policy must not be empty"},
		} {
			inputs[tc.field] = tc.value
			if decision == runtime.DecisionReplace {
				inputs["name"] = "replacement"
				if tc.field == "name" {
					inputs["prefix"] = "conflicting-"
				}
			}
			legacyLib.Resources["horizontal-pod-autoscaler"] = hpaLegacyInputEquality{currentReg}
			legacyLib.Constraints = nil
			legacyPlan, err := lastExecutor.Plan(t.Context())
			require.NoError(t, err)
			for _, step := range legacyPlan.Steps {
				if step.Address == "resource.hpa" {
					require.Equal(t, decision, step.Decision)
				}
			}
			legacyBytes, err := runtime.EncodePlan(legacyPlan)
			require.NoError(t, err)
			legacyFile, err := runtime.DecodePlan(legacyBytes)
			require.NoError(t, err)
			legacyLib.Resources["horizontal-pod-autoscaler"] = currentReg
			legacyLib.Constraints = map[string][]lang.ConstraintSpec{
				"resource.horizontal-pod-autoscaler": constraints}
			_, err = lastExecutor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.hpa: schema: constraints[")
			require.ErrorContains(t, err, tc.want)
			beforeApply := len(calls)
			_, err = lastExecutor.ApplyPlan(t.Context(), legacyFile)
			require.ErrorContains(t, err, "resource.hpa: schema: constraints[")
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, calls[beforeApply:])
			inputs["prefix"] = nil
			inputs[tc.field] = nil
			inputs["name"] = "test"
		}
	}
	stored.ResourceVersion = "later"
	stored.Generation = 25
	stored.Status.DesiredReplicas = 25
	plan(runtime.DecisionNoOp, false)
	inputs["token"] = "current"
	plan(runtime.DecisionNoOp, false)
	require.Contains(t, credentials, "Bearer current")
	metric := []any{map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu",
		"target": map[string]any{"type": "Utilization", "average-utilization": 83}}}}
	inputs["metric"] = metric
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []hpaPatchOperation{{Op: "test", Path: "/metadata/uid", Value: "uid-1"}},
		patches[len(patches)-1])
	plan(runtime.DecisionNoOp, false)
	inputs["metric"] = nil
	inputs["name"] = "test"
	inputs["labels"] = map[string]any{"app": "hpa"}
	plan(runtime.DecisionUpdate, true)
	require.EqualValues(t, 83, *stored.Spec.Metrics[0].Resource.Target.AverageUtilization)
	// Each plan opens a new store and creates a new registration after persisted omission.
	inputs["name"] = "test"
	inputs["metric"] = metric
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []hpaPatchOperation{{Op: "test", Path: "/metadata/uid", Value: "uid-1"}},
		patches[len(patches)-1])
	inputs["cpu"] = 90
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []hpaPatchOperation{{Op: "test", Path: "/metadata/uid", Value: "uid-1"}},
		patches[len(patches)-1])
	inputs["cpu"] = nil
	plan(runtime.DecisionUpdate, true)
	inputs["metric"] = []any{}
	plan(runtime.DecisionUpdate, true)
	require.Empty(t, stored.Spec.Metrics)
	inputs["metric"] = nil
	plan(runtime.DecisionNoOp, true)
	inputs["metric"] = metric
	plan(runtime.DecisionUpdate, true)
	require.EqualValues(t, 83, *stored.Spec.Metrics[0].Resource.Target.AverageUtilization)
	behavior := map[string]any{"scale-up": []any{map[string]any{"select-policy": "Max",
		"stabilization-window-seconds": 0, "policy": []any{map[string]any{"type": "Pods", "value": 4,
			"period-seconds": 15}}}}}
	inputs["behavior"] = behavior
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []hpaPatchOperation{{Op: "test", Path: "/metadata/uid", Value: "uid-1"}},
		patches[len(patches)-1])
	inputs["behavior"] = nil
	inputs["max"] = 6
	plan(runtime.DecisionUpdate, true)
	inputs["behavior"] = behavior
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []hpaPatchOperation{{Op: "test", Path: "/metadata/uid", Value: "uid-1"}},
		patches[len(patches)-1])
	behavior["scale-up"] = []any{}
	plan(runtime.DecisionUpdate, true)
	require.Empty(t, stored.Spec.Behavior.ScaleUp.Policies)
	inputs["name"] = "different"
	plan(runtime.DecisionReplace, true)
	require.Equal(t, types.UID("uid-2"), stored.UID)
	inputs["prefix"] = "conflicting-"
	_, err = (&HorizontalPodAutoscalerResource{Object: HPAObject{Metadata: HPAMetadata{Name: new(
		"different"), GenerateName: new("conflicting-")}, Spec: validHPA().Object.Spec}}).Update(
		t.Context(), c, hpaPrior{Outputs: savedHPA(*c.Server)})
	require.Error(t, err)
	inputs["prefix"] = nil
	stored = nil
	before := len(calls)
	plan(runtime.DecisionCreate, false)
	require.Equal(t, []string{"GET"}, calls[before:])
}

func hpaConstraintSpecs(t *testing.T) []lang.ConstraintSpec {
	t.Helper()
	runtimeDir, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/autoscaling/v2"),
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root},
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(
			runtimeDir))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	constraints := schema.Resources["horizontal-pod-autoscaler"].Constraints
	require.NotEmpty(t, constraints)
	return constraints
}

func TestHPAPendingConstraintPlanApply(t *testing.T) {
	stored := map[string]*autoscalingv2.HorizontalPodAutoscaler{}
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		var live *autoscalingv2.HorizontalPodAutoscaler
		switch req.Method {
		case "POST":
			live = &autoscalingv2.HorizontalPodAutoscaler{}
			require.NoError(t, json.NewDecoder(
				req.Body).Decode(live))
			if live.Name == "" {
				live.Name = live.GenerateName + "actual"
			}
			live.UID = types.UID(
				live.Name + "-uid")
			stored[live.Name] = live
		case "GET":
			live = stored[filepath.Base(req.URL.Path)]
		default:
			t.Errorf("unexpected %s", req.Method)
		}
		require.NotNil(t, live)
		hpaRespond(t, w, live)
	})
	body, err := os.ReadFile("testdata/ub/hpa-pending/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"hpa": {Name: "kubernetes-autoscaling-v2",
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"horizontal-pod-autoscaler": hpaResourceRegistration()},
		Constraints: map[string][]lang.ConstraintSpec{
			"resource.horizontal-pod-autoscaler": hpaConstraintSpecs(t)}}}
	store, err := local.NewStore(t.TempDir(), "pending", "test", encrypters.Noop{})
	require.NoError(t, err)
	executor := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: map[string]any{
			"server": *c.Server}, Store: store, Factory: state.FactoryInfo{Name: "pending",
			Version: "v0", ContentRevision: "c0"}}
	p, err := executor.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, calls)
	found := false
	for _, step := range p.Steps {
		if step.Address == "resource.dependent" {
			require.Contains(t,
				step.UnresolvedInputs, "object")
			found = true
		}
	}
	require.True(t, found)
	body, err = runtime.EncodePlan(p)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(
		body)
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET", "POST", "GET", "GET"}, calls)
	require.Contains(t, stored, "unobin-hpa-pending-actual-dependent")
	p, err = executor.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range p.Steps {
		if strings.HasPrefix(step.Address, "resource.") {
			require.Equal(t,
				runtime.DecisionNoOp, step.Decision)
		}
	}
	body, err = runtime.EncodePlan(p)
	require.NoError(t, err)
	file, err = runtime.DecodePlan(
		body)
	require.NoError(t, err)
	before := len(calls)
	_, err = executor.ApplyPlan(t.Context(), file)
	require.NoError(t,
		err)
	require.Empty(t, calls[before:])
}

type hpaLegacyInputEquality struct{ runtime.ResourceRegistration }

func (r hpaLegacyInputEquality) InputsEqual(any, map[string]any) (bool, error) { return true, nil }
func (r hpaLegacyInputEquality) KnownInputsEqual(any, map[string]any, map[string][]string) (
	bool, error) {
	return true, nil
}

func TestHPAInvalidResolvedDeferredInputs(t *testing.T) {
	for _, rule := range []string{"identity", "metric", "policy"} {
		for _, valid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", rule, valid), func(t *testing.T) {
				stored := map[string]*autoscalingv2.HorizontalPodAutoscaler{}
				posts := 0
				c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
					var live *autoscalingv2.HorizontalPodAutoscaler
					switch req.Method {
					case "POST":
						posts++
						live = &autoscalingv2.HorizontalPodAutoscaler{}
						require.NoError(t, json.NewDecoder(req.Body).Decode(live))
						if posts == 1 {
							live.Name = "seed"
							if rule == "identity" && !valid {
								live.Name = "BAD"
							}
							if rule == "metric" {
								live.Name = "Resource"
								if !valid {
									live.Name = "Other"
								}
							}
							if rule == "policy" {
								policies := []autoscalingv2.HPAScalingPolicy{}
								if valid {
									policies = []autoscalingv2.HPAScalingPolicy{{
										Type: "Pods", PeriodSeconds: 15, Value: 1}}
								}
								live.Spec.Behavior.ScaleUp = &autoscalingv2.HPAScalingRules{Policies: policies}
							}
						}
						live.UID = types.UID(live.Name + "-uid")
						stored[live.Name] = live
					case "GET":
						live = stored[filepath.Base(req.URL.Path)]
					default:
						t.Errorf("unexpected mutation %s", req.Method)
					}
					require.NotNil(t, live)
					hpaRespond(t, w, live)
				})
				body, err := os.ReadFile("testdata/ub/hpa-deferred-" + rule + "/factory.ub")
				require.NoError(t, err)
				parsed, err := syntax.ParseSource("factory.ub", body)
				require.NoError(t, err)
				libs := map[string]*runtime.Library{"hpa": {Name: "kubernetes-autoscaling-v2",
					Configuration: config.LibraryConfiguration(),
					Resources: map[string]runtime.ResourceRegistration{
						"horizontal-pod-autoscaler": hpaResourceRegistration()},
					Constraints: map[string][]lang.ConstraintSpec{
						"resource.horizontal-pod-autoscaler": hpaConstraintSpecs(t)}}}
				store, err := local.NewStore(t.TempDir(), "deferred", "test", encrypters.Noop{})
				require.NoError(t, err)
				executor := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
					SyntaxSource: &parsed.Factory.Body, Libraries: libs,
					Inputs: map[string]any{"server": *c.Server}, Store: store,
					Factory: state.FactoryInfo{Name: "deferred", Version: "v0", ContentRevision: "c0"}}
				plan, err := executor.Plan(t.Context())
				require.NoError(t, err)
				require.Zero(t, posts)
				for _, step := range plan.Steps {
					if step.Address == "resource.dependent" {
						require.Contains(t, step.UnresolvedInputs, "object")
					}
				}
				body, err = runtime.EncodePlan(plan)
				require.NoError(t, err)
				file, err := runtime.DecodePlan(body)
				require.NoError(t, err)
				_, err = executor.ApplyPlan(t.Context(), file)
				if valid {
					require.NoError(t, err)
					require.Equal(t, 2, posts)
				} else {
					require.ErrorContains(t, err, "resource.dependent: schema: constraints[")
					require.Equal(t, 1, posts)
				}
			})
		}
	}
}

func TestHPAHistoricalNativeDestroy(t *testing.T) {
	var calls []string
	live := liveHPA()
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == "DELETE" {
			hpaRespond(t, w, metav1.Status{Status: "Success"})
			return
		}
		hpaRespond(t, w, live)
	})
	body, err := os.ReadFile("testdata/ub/hpa-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	library := &runtime.Library{Name: "kubernetes-autoscaling-v2",
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"horizontal-pod-autoscaler": hpaResourceRegistration()}}
	libs := map[string]*runtime.Library{"hpa": library}
	store, err := local.NewStore(t.TempDir(), "historical", "test", encrypters.Noop{})
	require.NoError(t, err)
	executor := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store,
		Inputs: map[string]any{"server": *c.Server, "token": "current", "name": nil,
			"prefix": nil, "namespace": nil, "labels": nil, "metric": nil, "behavior": nil,
			"cpu": nil, "max": 4},
		Factory: state.FactoryInfo{Name: "historical", Version: "v0", ContentRevision: "c0"}}
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	apply := func(plan *runtime.Plan) {
		t.Helper()
		encoded, err := runtime.EncodePlan(plan)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		_, err = executor.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
	}
	apply(plan)
	library.Constraints = map[string][]lang.ConstraintSpec{
		"resource.horizontal-pod-autoscaler": hpaConstraintSpecs(t)}
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.hpa: schema: constraints[0] (exactly-one-of")
	executor.Destroy = true
	before := len(calls)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	apply(plan)
	require.Equal(t, []string{"GET", "GET", "DELETE"}, calls[before:])
}

func TestHPAReplacementConversionPreflight(t *testing.T) {
	constraints := hpaConstraintSpecs(t)
	for _, fixture := range []string{"factory.ub", "deferred.ub"} {
		for _, replacement := range []string{"identity", "configuration"} {
			for _, target := range []string{"Value", "AverageValue"} {
				for _, valid := range []bool{false, true} {
					name := fmt.Sprintf("%s/%s/%s/%t", fixture, replacement, target, valid)
					t.Run(name, func(t *testing.T) {
						stored := map[string]*autoscalingv2.HorizontalPodAutoscaler{}
						var mutations []string
						creates := 0
						quantity := "1"
						handler := func(w http.ResponseWriter, req *http.Request) {
							name := filepath.Base(req.URL.Path)
							live := stored[name]
							switch req.Method {
							case "POST":
								live = &autoscalingv2.HorizontalPodAutoscaler{}
								require.NoError(t, json.NewDecoder(req.Body).Decode(live))
								if live.Name == "" {
									live.Name = quantity
								}
								creates++
								live.UID = types.UID(fmt.Sprintf("uid-%d", creates))
								stored[live.Name] = live
								mutations = append(mutations, "POST "+live.Name)
							case "DELETE":
								delete(stored, name)
								mutations = append(mutations, "DELETE "+name)
								hpaRespond(t, w, metav1.Status{Status: "Success"})
								return
							case "GET":
							default:
								t.Errorf("unexpected %s", req.Method)
							}
							if live == nil {
								hpaFailure(t, w, 404)
								return
							}
							hpaRespond(t, w, live)
						}
						c := hpaAPI(t, handler)
						other := hpaAPI(t, handler)
						body, err := os.ReadFile("testdata/ub/hpa-preflight/factory.ub")
						require.NoError(t, err)
						parsed, err := syntax.ParseSource("factory.ub", body)
						require.NoError(t, err)
						library := &runtime.Library{Name: "kubernetes-autoscaling-v2",
							Configuration: config.LibraryConfiguration(),
							Constraints: map[string][]lang.ConstraintSpec{
								"resource.horizontal-pod-autoscaler": constraints},
							Resources: map[string]runtime.ResourceRegistration{
								"horizontal-pod-autoscaler": hpaResourceRegistration()}}
						libs := map[string]*runtime.Library{"hpa": library, "seed": library}
						dir := t.TempDir()
						store, err := local.NewStore(dir, "preflight", "test", encrypters.Noop{})
						require.NoError(t, err)
						inputs := map[string]any{"server": *c.Server, "seed-server": *c.Server,
							"name": "original", "quantity": "1", "target-type": target}
						executor := &runtime.Executor{
							DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
							SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs,
							Store: store, Factory: state.FactoryInfo{Name: "preflight",
								Version: "v0", ContentRevision: "c0"}}
						apply := func(plan *runtime.Plan) error {
							t.Helper()
							body, err := runtime.EncodePlan(plan)
							require.NoError(t, err)
							path := filepath.Join(dir, "saved-plan.json")
							require.NoError(t, os.WriteFile(path, body, 0o600))
							body, err = os.ReadFile(path)
							require.NoError(t, err)
							file, err := runtime.DecodePlan(body)
							require.NoError(t, err)
							_, err = executor.ApplyPlan(t.Context(), file)
							return err
						}
						plan, err := executor.Plan(t.Context())
						require.NoError(t, err)
						require.NoError(t, apply(plan))
						uid := stored["original"].UID
						if replacement == "identity" {
							inputs["name"] = "replacement"
						} else {
							inputs["server"] = *other.Server
						}
						inputs["quantity"] = "2"
						if !valid {
							inputs["quantity"] = "malformed"
						}
						quantity = inputs["quantity"].(string)
						if fixture == "deferred.ub" {
							body, err = os.ReadFile("testdata/ub/hpa-preflight/deferred.ub")
							require.NoError(t, err)
							parsed, err = syntax.ParseSource("factory.ub", body)
							require.NoError(t, err)
							executor = &runtime.Executor{
								DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
								SyntaxSource: &parsed.Factory.Body, Libraries: libs,
								Inputs: inputs, Store: store, Factory: executor.Factory}
						}
						plan, err = executor.Plan(t.Context())
						require.NoError(t, err)
						found := false
						for _, step := range plan.Steps {
							if step.Address == "resource.hpa" {
								found = true
								require.Equal(t, runtime.DecisionReplace, step.Decision)
								if fixture == "deferred.ub" {
									require.Contains(t, step.UnresolvedInputs, "object")
								}
							}
						}
						require.True(t, found)
						before := len(mutations)
						err = apply(plan)
						if valid {
							require.NoError(t, err)
							live := stored[inputs["name"].(string)]
							require.NotNil(t, live)
							require.NotEqual(t, uid, live.UID)
							want := []string{"DELETE original", "POST " + live.Name}
							if fixture == "deferred.ub" {
								want = append([]string{"POST 2"}, want...)
							}
							require.Equal(t, want, mutations[before:])
						} else {
							require.ErrorContains(t, err, "parse "+target+" target quantity")
							want := []string{}
							if fixture == "deferred.ub" {
								want = []string{"POST malformed"}
							}
							require.Equal(t, want, mutations[before:])
							require.Contains(t, stored, "original")
							require.Equal(t, uid, stored["original"].UID)
						}
					})
				}
			}
		}
	}
}
