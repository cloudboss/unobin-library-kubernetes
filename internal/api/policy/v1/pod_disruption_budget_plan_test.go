package v1

import (
	"context"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type pdbSeed struct{ Echo map[string]any }

func (s *pdbSeed) Run(context.Context, *config.Configuration) (any, error) {
	return map[string]any{"value": s.Echo}, nil
}

type pdbPlanner struct {
	t      *testing.T
	libs   map[string]*runtime.Library
	store  *local.Store
	inputs map[string]any
}

func newPDBPlanner(t *testing.T, c *config.Configuration) *pdbPlanner {
	t.Helper()
	lib := &runtime.Library{Name: "kubernetes-policy-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{"pod-disruption-budget": runtime.MakeResource[
			PodDisruptionBudgetResource, *PodDisruptionBudgetResourceOutput, *config.Configuration,
		]((&PodDisruptionBudgetResource{}).ResourceDefinition())},
		Actions: map[string]runtime.ActionRegistration{
			"seed": runtime.MakeAction[pdbSeed, any, *config.Configuration](),
		},
	}
	addNativePDBConstraints(t, lib)
	store, err := local.NewStore(t.TempDir(), "pdb", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &pdbPlanner{t: t, libs: map[string]*runtime.Library{"policy": lib}, store: store,
		inputs: map[string]any{"server": *c.Server,
			"metadata": map[string]any{"generate-name": "pdb-"},
			"spec":     map[string]any{"min-available": "+01", "selector": map[string]any{}},
		},
	}
}

func (p *pdbPlanner) executor(fixture string) *runtime.Executor {
	p.t.Helper()
	body, err := os.ReadFile("testdata/ub/" + fixture + "/factory.ub")
	require.NoError(p.t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(p.t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, p.libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: p.libs, Inputs: p.inputs, Store: p.store,
		Factory: state.FactoryInfo{Name: "pdb", Version: "v0", ContentRevision: "c0"},
	}
}

func (p *pdbPlanner) plan(fixture string, want runtime.Decision) (
	*runtime.Executor, *runtime.PlanFile,
) {
	p.t.Helper()
	exec := p.executor(fixture)
	plan, err := exec.Plan(p.t.Context())
	require.NoError(p.t, err)
	found := false
	for _, step := range plan.Steps {
		if step.Address == "resource.budget" {
			require.Equal(p.t, want, step.Decision)
			found = true
		}
	}
	require.True(p.t, found)
	body, err := runtime.EncodePlan(plan)
	require.NoError(p.t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(p.t, err)
	return exec, file
}

func TestPDBStatePlanning(t *testing.T) {
	s := &pdbServer{}
	p := newPDBPlanner(t, pdbAPI(t, s))
	exec, file := p.plan("state", runtime.DecisionCreate)
	_, err := exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	p.plan("state", runtime.DecisionNoOp)
	s.object.Status = policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 99}
	s.object.Generation, s.object.ResourceVersion = 42, "controller"
	s.object.CreationTimestamp = metav1.Now()
	p.plan("state", runtime.DecisionNoOp)
	metadata := p.inputs["metadata"].(map[string]any)
	metadata["labels"] = map[string]any{"managed": "new"}
	exec, file = p.plan("state", runtime.DecisionUpdate)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.Equal(t, "uid-1", string(s.object.UID))
	p.plan("state", runtime.DecisionNoOp)
	spec := p.inputs["spec"].(map[string]any)
	spec["min-available"] = "1"
	p.plan("state", runtime.DecisionReplace)
	spec["min-available"] = "+01"
	s.object.Spec.MinAvailable.IntVal = 2
	p.plan("state", runtime.DecisionReplace)
	s.object.Spec.MinAvailable.IntVal = 1
	for _, field := range []string{"name", "generate-name", "namespace"} {
		old := metadata[field]
		metadata[field] = "different"
		if field == "name" {
			delete(metadata, "generate-name")
		}
		p.plan("state", runtime.DecisionReplace)
		if old == nil {
			delete(metadata, field)
		} else {
			metadata[field] = old
		}
		metadata["generate-name"] = "pdb-"
	}
}

func TestPDBNativePlanAndSavedApply(t *testing.T) {
	s := &pdbServer{}
	p := newPDBPlanner(t, pdbAPI(t, s))
	exec, file := p.plan("state", runtime.DecisionCreate)
	_, err := exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	metadata := p.inputs["metadata"].(map[string]any)
	metadata["generate-name"] = ""
	_, err = p.executor("state").Plan(t.Context())
	require.ErrorContains(t, err, "resource.budget: schema: constraints")
	require.ErrorContains(t, err, "metadata.generate-name")
	metadata["generate-name"] = "pdb-"
	exec, file = p.plan("state", runtime.DecisionNoOp)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	exec, file = p.plan("state", runtime.DecisionNoOp)
	for i := range file.Steps {
		if file.Steps[i].Address == "resource.budget" {
			file.Steps[i].Inputs["metadata"].(map[string]any)["generate-name"] = ""
		}
	}
	file.Inputs["metadata"].(map[string]any)["generate-name"] = ""
	p.inputs["metadata"] = map[string]any{"generate-name": ""}
	before := len(s.calls)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err, "resource.budget: schema: constraints")
	require.Equal(t, before, len(s.calls))
	// Historical state may contain inputs rejected by a newer library version.
	p.libs["policy"].Constraints["resource.pod-disruption-budget"] = []lang.ConstraintSpec{
		{Kind: "predicate", When: "true", Require: "false"},
	}
	exec = p.executor("state")
	exec.Destroy = true
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err = runtime.DecodePlan(body)
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.Nil(t, s.object)
}

func TestPDBNativeDeferredApply(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "valid"}[valid], func(t *testing.T) {
			s := &pdbServer{}
			p := newPDBPlanner(t, pdbAPI(t, s))
			if !valid {
				p.inputs["metadata"] = map[string]any{"generate-name": ""}
			}
			exec, file := p.plan("deferred", runtime.DecisionCreate)
			require.Empty(t, s.calls)
			_, err := exec.ApplyPlan(t.Context(), file)
			if valid {
				require.NoError(t, err)
				require.Equal(t, 1, s.creates)
			} else {
				require.ErrorContains(t, err, "resource.budget: schema: constraints")
				require.Empty(t, s.calls)
			}
		})
	}
}

func TestPDBNativeDeferredMutation(t *testing.T) {
	for _, decision := range []runtime.Decision{runtime.DecisionUpdate, runtime.DecisionReplace} {
		t.Run(string(decision), func(t *testing.T) {
			s := &pdbServer{}
			p := newPDBPlanner(t, pdbAPI(t, s))
			exec, file := p.plan("state", runtime.DecisionCreate)
			_, err := exec.ApplyPlan(t.Context(), file)
			require.NoError(t, err)
			p.inputs["metadata"] = map[string]any{"generate-name": "", "labels": map[string]any{"x": "y"}}
			if decision == runtime.DecisionReplace {
				p.inputs["spec"].(map[string]any)["min-available"] = "2"
			}
			exec, file = p.plan("deferred", runtime.DecisionReplace)
			before := len(s.calls)
			_, err = exec.ApplyPlan(t.Context(), file)
			require.ErrorContains(t, err, "resource.budget: schema: constraints")
			require.Equal(t, before, len(s.calls))
			require.Equal(t, 1, s.creates)
			require.Equal(t, "uid-1", string(s.object.UID))
		})
	}
}

func TestPDBNativeOtherwiseNoOpAndHistoricalDestroy(t *testing.T) {
	s := &pdbServer{}
	p := newPDBPlanner(t, pdbAPI(t, s))
	native := p.libs["policy"].Constraints
	p.libs["policy"].Constraints = nil
	p.inputs["metadata"] = map[string]any{}
	exec, file := p.plan("state", runtime.DecisionCreate)
	_, err := exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	p.plan("state", runtime.DecisionNoOp)
	p.libs["policy"].Constraints = native
	before := len(s.calls)
	_, err = p.executor("state").Plan(t.Context())
	require.ErrorContains(t, err, "resource.budget: schema: constraints[0] (exactly-one-of")
	require.Equal(t, []string{"GET"}, s.calls[before:])
	exec = p.executor("state")
	exec.Destroy = true
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err = runtime.DecodePlan(body)
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.Nil(t, s.object)
}

func TestPDBSelectorNormalizationAndReplacement(t *testing.T) {
	s := &pdbServer{}
	p := newPDBPlanner(t, pdbAPI(t, s))
	expressions := []any{map[string]any{
		"key": "app", "operator": "In", "values": []any{"z", "a", "z"},
	}}
	p.inputs["spec"].(map[string]any)["selector"] = map[string]any{"match-expressions": expressions}
	exec, file := p.plan("state", runtime.DecisionCreate)
	_, err := exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	p.plan("state", runtime.DecisionNoOp)
	expressions[0].(map[string]any)["values"] = []any{"a", "z"}
	p.plan("state", runtime.DecisionNoOp)
	expressions[0].(map[string]any)["key"] = "changed"
	p.plan("state", runtime.DecisionReplace)
	expressions[0].(map[string]any)["key"] = "app"
	s.object.Spec.Selector.MatchExpressions[0].Key = "remote"
	p.plan("state", runtime.DecisionReplace)
}

func TestPDBNativeEvaluatedRules(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"name", func(in map[string]any) { in["metadata"] = map[string]any{"name": "BAD"} }},
		{"label key", func(in map[string]any) {
			in["metadata"].(map[string]any)["labels"] = map[string]any{"bad key": ""}
		}},
		{"label value", func(in map[string]any) {
			in["metadata"].(map[string]any)["labels"] = map[string]any{"key": "bad value"}
		}},
		{"annotation", func(in map[string]any) {
			in["metadata"].(map[string]any)["annotations"] = map[string]any{"bad key": ""}
		}},
		{"minimum", func(in map[string]any) { in["spec"].(map[string]any)["min-available"] = "101%" }},
		{"maximum", func(in map[string]any) { in["spec"].(map[string]any)["max-unavailable"] = "1.5" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &pdbServer{}
			p := newPDBPlanner(t, pdbAPI(t, s))
			test.mutate(p.inputs)
			_, err := p.executor("state").Plan(t.Context())
			require.ErrorContains(t, err, "resource.budget: schema: constraints")
			require.Empty(t, s.calls)
		})
	}
}

func TestPDBValidDeferredExistingInputs(t *testing.T) {
	s := &pdbServer{}
	p := newPDBPlanner(t, pdbAPI(t, s))
	exec, file := p.plan("state", runtime.DecisionCreate)
	_, err := exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	exec, file = p.plan("deferred", runtime.DecisionReplace)
	before := len(s.calls)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	for _, method := range s.calls[before:] {
		require.Equal(t, "GET", method)
	}
	require.Equal(t, 1, s.creates)
	p.inputs["metadata"].(map[string]any)["labels"] = map[string]any{"managed": "value"}
	exec, file = p.plan("deferred", runtime.DecisionReplace)
	before = len(s.calls)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	for _, method := range s.calls[before:] {
		require.Contains(t, []string{"GET", "PATCH"}, method)
	}
	require.Equal(t, 1, s.creates)
	require.Equal(t, "uid-1", string(s.object.UID))
	require.Equal(t, map[string]string{"managed": "value"}, s.object.Labels)
}
