package object_test

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestEnvPlansAndRefresh(t *testing.T) {
	api := newEnvAPI(t, "", false)
	exec := newEnvExecutor(t, api, "known")
	ctx := t.Context()
	get := api.get("example")
	apply := api.apply("example", "manager", "false")
	desired := []any{map[string]any{"name": "MANAGED", "value": "desired"}}
	plan := func(decision runtime.Decision, calls ...string) *runtime.PlanFile {
		t.Helper()
		saved := saveEnvPlan(t, exec)
		require.Equal(t, decision, envPlanStep(t, saved).Decision)
		require.Equal(t, calls, api.take())
		return saved
	}
	run := func(saved *runtime.PlanFile, calls ...string) {
		t.Helper()
		_, err := exec.ApplyPlan(ctx, saved)
		require.NoError(t, err)
		require.Equal(t, calls, api.take())
	}
	run(plan(runtime.DecisionCreate), get, apply, get)
	snapshot, err := exec.Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find(envAddress)
	require.Equal(t, "uid-example", entry.Outputs["uid"])
	observed := func(value string) []any {
		return []any{map[string]any{"name": "MANAGED", "value": value, "value-from": nil}}
	}
	require.Equal(t, observed("desired"), entry.Outputs["env"])
	require.Equal(t, map[string]any{"observed": observed("desired")}, snapshot.Outputs)

	run(plan(runtime.DecisionNoOp, get))

	for _, namespace := range []string{"", "default"} {
		exec.Inputs["namespace"], exec.Inputs["kind"] = namespace, "DEPLOYMENT"
		run(plan(runtime.DecisionNoOp, get))
	}

	for _, edit := range []struct {
		input, value, reason string
	}{
		{"namespace", "different", "metadata.namespace"},
		{"kind", "ReplicaSet", "kind"},
		{"version", "apps/v2", "api-version"},
	} {
		before := exec.Inputs[edit.input]
		exec.Inputs[edit.input] = edit.value
		saved := plan(runtime.DecisionReplace, get)
		require.Equal(t, []string{edit.reason}, envPlanStep(t, saved).ReplacementReasons)
		exec.Inputs[edit.input] = before
	}

	outside := map[string]any{"name": "EXTERNAL", "value": "changed"}
	api.set("example", append([]any{outside}, desired...))
	plan(runtime.DecisionNoOp, get)
	api.set("example", []any{outside})
	drift := plan(runtime.DecisionUpdate, get)
	require.True(t, envPlanStep(t, drift).Drift())
	run(drift, get, apply, get)
	plan(runtime.DecisionNoOp, get)

	changed := []any{map[string]any{"name": "MANAGED", "value": "drifted"}}
	api.set("example", changed)
	drift = plan(runtime.DecisionUpdate, get)
	require.True(t, envPlanStep(t, drift).Drift())
	_, err = exec.Refresh(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{get}, api.take())
	snapshot, err = exec.Store.Current()
	require.NoError(t, err)
	require.Equal(t, observed("drifted"), snapshot.Find(envAddress).Outputs["env"])
	plan(runtime.DecisionNoOp, get)

	api.rewrite = true
	exec.Inputs["manager"], exec.Inputs["force"] = "new-manager", true
	run(plan(runtime.DecisionUpdate, get), get, api.apply("example", "new-manager", "true"), get)
	api.rewrite = false
	snapshot, err = exec.Store.Current()
	require.NoError(t, err)
	entry = snapshot.Find(envAddress)
	require.Equal(t, desired, entry.Inputs["env"])
	require.Equal(t, observed("admitted"), entry.Outputs["env"])
	plan(runtime.DecisionNoOp, get)

	exec.Inputs["name"] = "next"
	getNext := api.get("next")
	run(plan(runtime.DecisionReplace, get), get, get, api.apply("example", "new-manager", "true"),
		getNext, api.apply("next", "new-manager", "true"), getNext)
	require.Equal(t, []any{}, api.env("example"))
	require.Equal(t, desired, api.env("next"))
	snapshot, err = exec.Store.Current()
	require.NoError(t, err)
	require.Equal(t, "uid-next", snapshot.Find(envAddress).Outputs["uid"])

	for _, missing := range []string{"container", "workload"} {
		api.missing = missing
		reattach := plan(runtime.DecisionCreate, getNext)
		_, err = exec.ApplyPlan(ctx, reattach)
		require.ErrorContains(t, err, "get existing Kubernetes Deployment next")
		require.Equal(t, []string{getNext}, api.take())
	}
	api.missing = ""
	api.uid = "different"
	_, err = exec.Plan(ctx)
	require.ErrorContains(t, err, "UID changed from uid-next to different")
	require.Equal(t, []string{getNext}, api.take())
}

func TestEnvRootNamespaceEditKeepsObject(t *testing.T) {
	api := newEnvAPI(t, "", true)
	exec := newEnvExecutor(t, api, "known")
	ctx := t.Context()
	exec.Inputs["namespace"] = "ignored"
	api.rewrite = true
	_, err := exec.ApplyPlan(ctx, saveEnvPlan(t, exec))
	require.NoError(t, err)
	api.rewrite = false
	get, patch := api.get("example"), api.apply("example", "manager", "false")
	require.Equal(t, []string{get, patch, get}, api.take())
	extra := map[string]any{"name": "SAME_MANAGER", "value": "outside selection"}
	live := []any{extra, map[string]any{"name": "MANAGED", "value": "admitted"}}
	api.set("example", live)
	exec.Inputs["namespace"] = "also-ignored"
	saved := saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionUpdate, envPlanStep(t, saved).Decision)
	require.Equal(t, []string{get}, api.take())
	_, err = exec.ApplyPlan(ctx, saved)
	require.NoError(t, err)
	require.Equal(t, []string{get}, api.take())
	require.Equal(t, live, api.env("example"))
	snapshot, err := exec.Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find(envAddress)
	require.Equal(t, "also-ignored", entry.Inputs["metadata"].(map[string]any)["namespace"])
	require.Equal(t, "uid-example", entry.Outputs["uid"])
	require.Equal(t, "", entry.Outputs["namespace"])
	require.Equal(t, []any{map[string]any{
		"name": "MANAGED", "value": "admitted", "value-from": nil,
	}}, entry.Outputs["env"])

	exec.Inputs["namespace"] = "still-ignored"
	saved = saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionUpdate, envPlanStep(t, saved).Decision)
	require.Equal(t, []string{get}, api.take())
	api.set("example", []any{map[string]any{"name": "MANAGED", "value": "drifted"}})
	_, err = exec.ApplyPlan(ctx, saved)
	require.NoError(t, err)
	require.Equal(t, []string{get, patch, get}, api.take())
	require.Equal(t, []any{map[string]any{"name": "MANAGED", "value": "desired"}},
		api.env("example"))

	exec.Inputs["namespace"], exec.Inputs["force"] = "ignored-again", true
	saved = saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionUpdate, envPlanStep(t, saved).Decision)
	require.Equal(t, []string{get}, api.take())
	_, err = exec.ApplyPlan(ctx, saved)
	require.NoError(t, err)
	require.Equal(t, []string{get, api.apply("example", "manager", "true"), get}, api.take())
}

func TestEnvClusterBindingReplacement(t *testing.T) {
	original := newEnvAPI(t, "", false)
	next := newEnvAPI(t, "", false)
	exec := newEnvExecutor(t, original, "known")
	ctx := t.Context()
	get := original.get("example")
	patch := original.apply("example", "manager", "false")
	_, err := exec.ApplyPlan(ctx, saveEnvPlan(t, exec))
	require.NoError(t, err)
	require.Equal(t, []string{get, patch, get}, original.take())
	require.Empty(t, next.take())

	exec.Inputs["server"] = next.server
	saved := saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionReplace, envPlanStep(t, saved).Decision)
	require.Equal(t, []string{get}, original.take())
	require.Empty(t, next.take())
	_, err = exec.ApplyPlan(ctx, saved)
	require.NoError(t, err)
	require.Equal(t, []string{get, get, patch}, original.take())
	require.Equal(t, []string{get, patch, get}, next.take())
	require.Equal(t, []any{}, original.env("example"))
	require.Equal(t, []any{map[string]any{"name": "MANAGED", "value": "desired"}},
		next.env("example"))

	exec.Inputs["name"] = "missing"
	saved = saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionReplace, envPlanStep(t, saved).Decision)
	require.Equal(t, []string{get}, next.take())
	_, err = exec.ApplyPlan(ctx, saved)
	require.ErrorContains(t, err, "get existing Kubernetes Deployment missing")
	require.Equal(t, []string{get, get, patch, next.get("missing")}, next.take())
	require.Equal(t, []any{}, next.env("example"))
}

func TestEnvDeferredDivisorConversion(t *testing.T) {
	api := newEnvAPI(t, "invalid-quantity", false)
	exec := newEnvExecutor(t, api, "known")
	ctx := t.Context()
	_, err := exec.ApplyPlan(ctx, saveEnvPlan(t, exec))
	require.NoError(t, err)
	get := api.get("example")
	require.Equal(t, []string{get, api.apply("example", "manager", "false"), get}, api.take())

	setEnvFactory(t, exec, "pending")
	exec.Inputs["mode"] = "divisor"
	saved := saveEnvPlan(t, exec)
	step := envPlanStep(t, saved)
	require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
	require.Equal(t, map[string][]string{
		"env": {"resource.seed.object.metadata.uid"},
	}, step.UnresolvedInputs)
	require.Equal(t, []string{get}, api.take())

	_, err = exec.ApplyPlan(ctx, saved)
	require.ErrorContains(t, err, "prepare env[0].value-from.resource-field-ref.divisor")
	require.Equal(t, []string{
		"POST /api/v1/namespaces/default/configmaps", "GET /api/v1/namespaces/default/configmaps/seed",
	}, api.take())
	require.Equal(t, []any{map[string]any{"name": "MANAGED", "value": "desired"}},
		api.env("example"))
}

func TestEnvQuantityPlanEquivalence(t *testing.T) {
	api := newEnvAPI(t, "main", false)
	exec := newEnvExecutor(t, api, "pending")
	ctx := t.Context()
	exec.Inputs["mode"], exec.Inputs["divisor"] = "initial", "0.001"
	_, err := exec.ApplyPlan(ctx, saveEnvPlan(t, exec))
	require.NoError(t, err)
	api.take()
	exec.Inputs["divisor"] = "1m"
	saved := saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionNoOp, envPlanStep(t, saved).Decision)
	exec.Inputs["divisor"] = "2m"
	saved = saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionUpdate, envPlanStep(t, saved).Decision)
	exec.Inputs["divisor"] = "invalid"
	saved = saveEnvPlan(t, exec)
	api.take()
	_, err = exec.ApplyPlan(ctx, saved)
	require.ErrorContains(t, err, "prepare env[0].value-from.resource-field-ref.divisor")
	require.Empty(t, api.take())
}

func TestEnvEvaluatedConstraints(t *testing.T) {
	api := newEnvAPI(t, "", false)
	exec := newEnvExecutor(t, api, "known")
	ctx := t.Context()
	get := api.get("example")
	_, err := exec.ApplyPlan(ctx, saveEnvPlan(t, exec))
	require.NoError(t, err)
	require.Equal(t, []string{get, api.apply("example", "manager", "false"), get}, api.take())

	unchanged := saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionNoOp, envPlanStep(t, unchanged).Decision)
	require.Equal(t, []string{get}, api.take())
	_, err = exec.ApplyPlan(ctx, unchanged)
	require.NoError(t, err)
	require.Empty(t, api.take())

	for _, tc := range []struct {
		input string
		value any
		rule  string
	}{
		{"version", "apps/v1/extra", "0]"},
		{"kind", "Job", "1]"},
		{"name", "..", "2]"},
		{"init-container", "init", "3]"},
		{"container", "", "4]"},
		{"container", nil, "3]"},
		{"env", []any{}, "6]"},
	} {
		t.Run(tc.input+tc.rule, func(t *testing.T) {
			before := exec.Inputs[tc.input]
			exec.Inputs[tc.input] = tc.value
			_, err := exec.Plan(ctx)
			exec.Inputs[tc.input] = before
			require.ErrorContains(t, err, envConstraint+tc.rule)
			require.Equal(t, []string{get}, api.take())
		})
	}
	exec.Inputs["container"], exec.Inputs["init-container"] = nil, "\u2003\u0085"
	_, err = exec.Plan(ctx)
	require.ErrorContains(t, err, envConstraint+"5]")
	require.Equal(t, []string{get}, api.take())
}

func TestEnvSavedNoOpAndHistoricalDestroy(t *testing.T) {
	api := newEnvAPI(t, "", false)
	exec := newEnvExecutor(t, api, "known")
	exec.Inputs["env"] = []any{}
	ctx := t.Context()
	lib := exec.Libraries["dynamic"]
	production := lib.Constraints[envBinding]
	lib.Constraints[envBinding] = production[:6]
	get, patch := api.get("example"), api.apply("example", "manager", "false")
	_, err := exec.ApplyPlan(ctx, saveEnvPlan(t, exec))
	require.NoError(t, err)
	require.Equal(t, []string{get, patch, get}, api.take())
	unchanged := saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionNoOp, envPlanStep(t, unchanged).Decision)
	require.Equal(t, []string{get}, api.take())

	lib.Constraints[envBinding] = production
	_, err = exec.ApplyPlan(ctx, unchanged)
	require.ErrorContains(t, err, envConstraint+"6]")
	require.Empty(t, api.take())
	_, err = exec.Plan(ctx)
	require.ErrorContains(t, err, envConstraint+"6]")
	require.Equal(t, []string{get}, api.take())

	exec.Destroy = true
	destroy := saveEnvPlan(t, exec)
	require.Equal(t, runtime.DecisionDestroy, envPlanStep(t, destroy).Decision)
	require.Equal(t, []string{get}, api.take())
	_, err = exec.ApplyPlan(ctx, destroy)
	require.NoError(t, err)
	require.Equal(t, []string{get, get, patch}, api.take())
	require.Equal(t, []any{}, api.env("example"))
}

func TestEnvDeferredSelectorReplacement(t *testing.T) {
	for _, tc := range []struct{ seedUID, diagnostic string }{
		{"\u2003", envConstraint + "4]"},
		{"main", ""},
	} {
		t.Run(tc.seedUID, func(t *testing.T) {
			api := newEnvAPI(t, tc.seedUID, false)
			exec := newEnvExecutor(t, api, "known")
			ctx := t.Context()
			_, err := exec.ApplyPlan(ctx, saveEnvPlan(t, exec))
			require.NoError(t, err)
			get := api.get("example")
			require.Equal(t, []string{get, api.apply("example", "manager", "false"), get},
				api.take())
			setEnvFactory(t, exec, "pending")
			exec.Inputs["mode"], exec.Inputs["divisor"] = "selector", "1m"
			exec.Inputs["kind"] = "Job"
			_, err = exec.Plan(ctx)
			require.ErrorContains(t, err, envConstraint+"1]")
			require.Equal(t, []string{get}, api.take())
			exec.Inputs["kind"] = "deployment"

			saved := saveEnvPlan(t, exec)
			step := envPlanStep(t, saved)
			require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
			require.Equal(t, map[string][]string{
				"container": {"resource.seed.object.metadata.uid"},
			}, step.UnresolvedInputs)
			require.Equal(t, []string{get}, api.take())
			_, err = exec.ApplyPlan(ctx, saved)
			seed := "GET /api/v1/namespaces/default/configmaps/seed"
			calls := []string{"POST /api/v1/namespaces/default/configmaps", seed}
			if tc.diagnostic == "" {
				require.NoError(t, err)
				next := api.get("next")
				calls = append(calls, get, get, api.apply("example", "manager", "false"),
					next, api.apply("next", "Terraform", "false"), next, seed)
				require.Equal(t, []any{}, api.env("example"))
				require.Equal(t, []any{map[string]any{"name": "CPU", "valueFrom": map[string]any{
					"resourceFieldRef": map[string]any{"resource": "limits.cpu", "divisor": "1m"},
				}}}, api.env("next"))
			} else {
				require.ErrorContains(t, err, tc.diagnostic)
				require.Equal(t, []any{map[string]any{"name": "MANAGED", "value": "desired"}},
					api.env("example"))
			}
			require.Equal(t, calls, api.take())
		})
	}
}
