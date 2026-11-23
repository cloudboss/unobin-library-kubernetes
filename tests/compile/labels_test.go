package compile_test

import (
	"encoding/json"
	"net/http"
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
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/dynamic"
)

const (
	labelsFixtures   = "testdata/ub/labels"
	labelsAddress    = "resource.selection"
	labelsConstraint = labelsAddress + ": schema: constraints["
	labelsLiteral    = labelsAddress + ": constraints["
	labelsCollection = "/api/v1/namespaces/default/configmaps"
)

func TestCompiledLabelsLiterals(t *testing.T) {
	for _, tc := range []struct{ fixture, diagnostic string }{
		{"valid-core", ""}, {"valid-leading-slash", ""}, {"valid-group", ""},
		{"missing-version", `missing required input "api-version"`},
		{"null-version", "expected string, got null"},
		{"missing-kind", `missing required input "kind"`},
		{"null-kind", "expected string, got null"},
		{"missing-metadata", `missing required input "metadata"`},
		{"null-metadata", "got null"},
		{"missing-name", `missing required field "name"`},
		{"null-name", "expected string, got null"},
		{"missing-labels", `missing required input "labels"`},
		{"null-labels", "got null"},
		{"wrong-label", "expected string, got integer"},
		{"invalid-version", labelsLiteral + "0]"},
		{"empty-version", labelsLiteral + "0]"},
		{"empty-kind", labelsLiteral + "1]"},
		{"empty-name", labelsLiteral + "2]"},
		{"dot-name", labelsLiteral + "2]"},
		{"dot-dot-name", labelsLiteral + "2]"},
		{"slash-name", labelsLiteral + "2]"},
		{"percent-name", labelsLiteral + "2]"},
		{"empty-manager", labelsLiteral + "3]"},
		{"ascii-space-manager", labelsLiteral + "3]"},
		{"unicode-space-manager", labelsLiteral + "3]"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			_, err := compileLabels(t, filepath.Join("literals", tc.fixture), false)
			if tc.diagnostic == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.diagnostic)
			}
		})
	}
}

func TestCompiledLabelsPlans(t *testing.T) {
	binary, err := compileLabels(t, "known", true)
	require.NoError(t, err)
	api := newLabelsPlanAPI(t, "")
	run := labelsRun(t, binary, "known", api.server,
		"LOOKUP_VERSION", "v1", "LOOKUP_KIND", "ConfigMap", "LOOKUP_NAME", "example",
		"FIELD_MANAGER", "team manager", "'FORCE'", "false", "CONFIGURED_NAMESPACE", "other")
	plan := func(want runtime.Decision) *runtime.PlanStep {
		run.ok("plan", "-c", "config.ub", "-o", "saved.ubp")
		step := labelsPlanStep(t, run.plan("saved.ubp"))
		require.Equal(t, want, step.Decision)
		return step
	}
	plan(runtime.DecisionCreate)
	require.Empty(t, api.take())
	run.ok("apply", "saved.ubp")
	require.Equal(t, labelApplyCalls("example", "team+manager", false), api.take())
	entry := run.state().Find(labelsAddress)
	require.NotNil(t, entry)
	require.Equal(t, map[string]any{
		"server": api.server, "api-version": "v1", "kind": "ConfigMap", "name": "example",
		"namespace": "default", "uid": "uid-example",
		"labels": map[string]any{"managed": "desired"},
	}, entry.Outputs)
	require.Equal(t, map[string]any{"observed": map[string]any{"managed": "desired"}},
		run.state().Outputs)

	plan(runtime.DecisionNoOp)
	require.Equal(t, []string{labelGet("example")}, api.take())
	run.ok("apply", "saved.ubp")
	require.Empty(t, api.take())
	for _, tc := range []struct{ version, kind, name, manager, rule string }{
		{"v1/extra/version", "ConfigMap", "example", "team manager", "0"},
		{"v1", "", "example", "team manager", "1"},
		{"v1", "ConfigMap", "bad/name", "team manager", "2"},
		{"v1", "ConfigMap", ".", "team manager", "2"},
		{"v1", "ConfigMap", "..", "team manager", "2"},
		{"v1", "ConfigMap", "example", " ", "3"},
	} {
		labelsConfigure(run, "known", api.server,
			"LOOKUP_VERSION", tc.version, "LOOKUP_KIND", tc.kind, "LOOKUP_NAME", tc.name,
			"FIELD_MANAGER", tc.manager, "'FORCE'", "false", "CONFIGURED_NAMESPACE", "other")
		out := run.fail("plan", "-c", "config.ub", "-o", "invalid.ubp")
		require.Contains(t, out, labelsConstraint+tc.rule+"]")
		require.Equal(t, []string{labelGet("example")}, api.take())
	}

	labelsConfigure(run, "known", api.server,
		"LOOKUP_VERSION", "v1", "LOOKUP_KIND", "ConfigMap", "LOOKUP_NAME", "example",
		"FIELD_MANAGER", "team manager", "'FORCE'", "false", "CONFIGURED_NAMESPACE", "changed")
	api.setLabels("example", map[string]string{"managed": "desired", "external": "changed"})
	plan(runtime.DecisionNoOp)
	require.Equal(t, []string{labelGet("example")}, api.take())
	api.setLabels("example", map[string]string{"external": "changed"})
	require.True(t, plan(runtime.DecisionUpdate).Drift())
	require.Equal(t, []string{labelGet("example")}, api.take())
	run.ok("apply", "saved.ubp")
	require.Equal(t, labelApplyCalls("example", "team+manager", false), api.take())
	plan(runtime.DecisionNoOp)
	require.Equal(t, []string{labelGet("example")}, api.take())

	api.setLabels("example", map[string]string{"managed": "drifted"})
	require.True(t, plan(runtime.DecisionUpdate).Drift())
	require.Equal(t, []string{labelGet("example")}, api.take())
	run.ok("refresh", "-c", "config.ub")
	require.Equal(t, []string{labelGet("example")}, api.take())
	require.Equal(t, map[string]any{"managed": "drifted"},
		run.state().Find(labelsAddress).Outputs["labels"])
	plan(runtime.DecisionNoOp)
	require.Equal(t, []string{labelGet("example")}, api.take())

	labelsConfigure(run, "known", api.server,
		"LOOKUP_VERSION", "v1", "LOOKUP_KIND", "ConfigMap", "LOOKUP_NAME", "example",
		"FIELD_MANAGER", "new manager", "'FORCE'", "false", "CONFIGURED_NAMESPACE", "other")
	plan(runtime.DecisionUpdate)
	require.Equal(t, []string{labelGet("example")}, api.take())
	run.ok("apply", "saved.ubp")
	require.Equal(t, labelApplyCalls("example", "new+manager", false), api.take())
	labelsConfigure(run, "known", api.server,
		"LOOKUP_VERSION", "v1", "LOOKUP_KIND", "ConfigMap", "LOOKUP_NAME", "example",
		"FIELD_MANAGER", "new manager", "'FORCE'", "true", "CONFIGURED_NAMESPACE", "other")
	plan(runtime.DecisionUpdate)
	require.Equal(t, []string{labelGet("example")}, api.take())
	api.setAdmissionRewrite(true)
	run.ok("apply", "saved.ubp")
	require.Equal(t, labelApplyCalls("example", "new+manager", true), api.take())
	api.setAdmissionRewrite(false)
	entry = run.state().Find(labelsAddress)
	require.Equal(t, map[string]any{"managed": "desired"}, entry.Inputs["labels"])
	require.Equal(t, map[string]any{"managed": "admitted"}, entry.Outputs["labels"])
	plan(runtime.DecisionNoOp)
	require.Equal(t, []string{labelGet("example")}, api.take())

	labelsConfigure(run, "known", api.server,
		"LOOKUP_VERSION", "v1", "LOOKUP_KIND", "ConfigMap", "LOOKUP_NAME", "next",
		"FIELD_MANAGER", "new manager", "'FORCE'", "true", "CONFIGURED_NAMESPACE", "other")
	plan(runtime.DecisionReplace)
	require.Equal(t, []string{labelGet("example")}, api.take())
	run.ok("apply", "saved.ubp")
	want := []string{labelGet("example"), labelGet("example"),
		labelPatch("example", "new+manager", true)}
	want = append(want, labelApplyCalls("next", "new+manager", true)...)
	require.Equal(t, want, api.take())
	require.Equal(t, map[string]string{}, api.labelsFor("example"))
	require.Equal(t, map[string]string{"managed": "desired"}, api.labelsFor("next"))
	require.Equal(t, "uid-next", run.state().Find(labelsAddress).Outputs["uid"])

	api.remove("next")
	plan(runtime.DecisionCreate)
	require.Equal(t, []string{labelGet("next")}, api.take())
	out := run.fail("apply", "saved.ubp")
	require.Contains(t, out, "get existing Kubernetes ConfigMap next")
	require.Equal(t, []string{labelGet("next")}, api.take())
}

func TestCompiledLabelsDeferredReplacement(t *testing.T) {
	binary, err := compileLabels(t, "pending", true)
	require.NoError(t, err)
	for _, target := range []string{"bad/name", "next"} {
		t.Run(target, func(t *testing.T) {
			api := newLabelsPlanAPI(t, target)
			run := labelsRun(t, binary, "pending", api.server, "'DEFER_NAME'", "false")
			run.ok("plan", "-c", "config.ub", "-o", "initial.ubp")
			run.ok("apply", "initial.ubp")
			initialCalls := []string{"POST " + labelsCollection, labelGet("seed")}
			initialCalls = append(initialCalls, labelApplyCalls("example", "Terraform", false)...)
			require.Equal(t, append(initialCalls, labelGet("seed")), api.take())
			entry := run.state().Find(labelsAddress)
			require.Equal(t, "Terraform", entry.Inputs["field-manager"])
			require.Equal(t, false, entry.Inputs["force-conflicts"])
			run.ok("state", "remove", "-c", "config.ub", "resource.seed")
			labelsConfigure(run, "pending", api.server, "'DEFER_NAME'", "true")
			run.ok("plan", "-c", "config.ub", "-o", "pending.ubp")
			step := labelsPlanStep(t, run.plan("pending.ubp"))
			require.Equal(t, map[string][]string{
				"metadata": {"resource.seed.object.metadata.uid"},
			}, step.UnresolvedInputs)
			require.Equal(t, []string{"metadata.name", "metadata.namespace"},
				step.PendingReplacementReasons)
			require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
			require.Equal(t, []string{labelGet("example")}, api.take())
			want := []string{"POST " + labelsCollection, labelGet("seed")}
			if target == "next" {
				run.ok("apply", "pending.ubp")
				want = append(want, labelGet("example"), labelGet("example"),
					labelPatch("example", "Terraform", false))
				want = append(want, labelApplyCalls("next", "Terraform", false)...)
				want = append(want, labelGet("seed"))
				require.Equal(t, "uid-next", run.state().Find(labelsAddress).Outputs["uid"])
			} else {
				require.Contains(t, run.fail("apply", "pending.ubp"), labelsConstraint+"2]")
				require.Equal(t, map[string]string{"managed": "desired"}, api.labelsFor("example"))
			}
			require.Equal(t, want, api.take())
		})
	}
}

func TestLabelsSavedNoOpAndHistoricalDestroyWithTightenedRule(t *testing.T) {
	path := filepath.Join(labelsFixtures, "known", "factory.ub")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	syntax, _, err := compile.ParseFactorySyntaxSource(path, source)
	require.NoError(t, err)
	lib := dynamic.Library()
	libs := map[string]*runtime.Library{"dynamic": lib}
	store, err := local.NewStore(objectInfoWork(t), "labels", "test", encrypters.Noop{})
	require.NoError(t, err)
	api := newLabelsPlanAPI(t, "")
	t.Setenv("KUBECONFIG", filepath.Join(objectInfoWork(t), "missing"))
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

func compileLabels(t *testing.T, fixture string, build bool) (string, error) {
	t.Helper()
	return compileDynamicFixtureForAPI(t, filepath.Join(labelsFixtures, fixture, "factory.ub"),
		"dynamic-labels", build, nil)
}

func labelsRun(t *testing.T, binary, fixture, server string, pairs ...string) objectInfoRun {
	t.Helper()
	run := objectInfoRun{t: t, binary: binary, work: objectInfoWork(t)}
	t.Setenv("KUBECONFIG", filepath.Join(run.work, "missing"))
	labelsConfigure(run, fixture, server, pairs...)
	return run
}

func labelsConfigure(run objectInfoRun, fixture, server string, pairs ...string) {
	run.t.Helper()
	body, err := os.ReadFile(filepath.Join(labelsFixtures, fixture, "config.ub"))
	require.NoError(run.t, err)
	input := strings.NewReplacer(append([]string{"KUBE_SERVER", server}, pairs...)...).
		Replace(string(body))
	require.NoError(run.t, os.WriteFile(filepath.Join(run.work, "config.ub"), []byte(input), 0o600))
	run.ok("pin", "-c", "config.ub")
}

func labelsPlanStep(t *testing.T, plan *runtime.PlanFile) *runtime.PlanStep {
	t.Helper()
	for i := range plan.Steps {
		if plan.Steps[i].Address == labelsAddress {
			return &plan.Steps[i]
		}
	}
	t.Fatalf("missing %s in %v", labelsAddress, objectInfoStepAddresses(plan.Steps))
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
	server, requests := newObjectInfoHTTP(t, func(req *http.Request) (int, any) {
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

func (a *labelsPlanAPI) setLabels(name string, labels map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objects[name] = labels
}

func (a *labelsPlanAPI) labelsFor(name string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.objects[name]
}

func (a *labelsPlanAPI) setAdmissionRewrite(rewrite bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rewrite = rewrite
}

func (a *labelsPlanAPI) remove(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.objects, name)
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
