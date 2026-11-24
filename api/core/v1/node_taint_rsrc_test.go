package v1

import (
	"bytes"
	"context"
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
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

const (
	nodeTaintFixtures    = "testdata/ub/node-taint"
	nodeTaintNameRule    = "object.metadata.name must be a DNS subdomain"
	nodeTaintLengthRule  = "object.spec.taints must contain at least one taint"
	nodeTaintUniqueRule  = "object.spec.taints must have unique keys"
	nodeTaintManagerRule = "field-manager must contain a non-whitespace character"
)

func TestCompiledNodeTaintInputs(t *testing.T) {
	for _, tc := range []struct {
		fixture     string
		diagnostics []string
	}{
		{"valid", nil},
		{"identity", []string{
			":7:19: type: missing required field \"name\"",
			":18:27: type: type mismatch: expected string, got null",
			":29:35: type: unknown field \"namespace\"",
			":40:21: type: unknown field \"generate-name\"",
		}},
		{"shape", []string{
			":5:37: resolve: missing required input \"object\"",
			":7:15: type: missing required field \"spec\"",
			":10:51: type: missing required field \"taints\"",
			":13:61: type: type mismatch: expected list(object(",
			":16:62: type: type mismatch: expected object(",
			":21:30: type: missing required field \"key\"",
			":25:62: type: missing required field \"value\"",
			":28:62: type: missing required field \"effect\"",
			":33:37: type: type mismatch: expected string, got null",
			":39:49: type: type mismatch: expected string, got null",
			":45:61: type: type mismatch: expected string, got null",
			":51:37: type: type mismatch: expected string, got boolean",
			":57:49: type: type mismatch: expected string, got integer",
			":63:61: type: type mismatch: expected string, got boolean",
		}},
		{"rules", []string{nodeTaintNameRule, nodeTaintLengthRule, nodeTaintUniqueRule,
			nodeTaintManagerRule}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			err := compileNodeTaintFixture(t, tc.fixture, nil)
			if tc.diagnostics == nil {
				require.NoError(t, err)
				return
			}
			for _, diagnostic := range tc.diagnostics {
				require.ErrorContains(t, err, diagnostic)
			}
			if tc.fixture == "rules" {
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestNodeTaintCompilerCompatibility(t *testing.T) {
	old := libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	err := compileNodeTaintFixture(t, "valid", &old)
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
}

func compileNodeTaintFixture(
	t *testing.T, fixture string, api *libraryapi.Descriptor,
) error {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	var log bytes.Buffer
	_, err = compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(nodeTaintFixtures, fixture, "factory.ub"),
		OutDir:      coreCompileDirectory(t), StackName: "node-taint-test", Version: "v0.0.0",
		CLIVersion: "v0.13.0", GoVersion: "1.26.2", LibraryAPIDescriptor: api,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	if err != nil && log.Len() > 0 {
		t.Log(log.String())
	}
	return err
}

func nodeTaintLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: unobinSource(t)})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	typ := schema.Resources["node-taint"]
	lib.Constraints = map[string][]lang.ConstraintSpec{"resource.node-taint": typ.Constraints}
	lib.Defaults = map[string][]lang.DefaultSpec{"resource.node-taint": typ.Defaults}
	return lib
}

func nodeTaintExecutor(
	t *testing.T, lib *runtime.Library, fixture string, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(nodeTaintFixtures, fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": lib}
	store, err := local.NewStore(t.TempDir(), "node-taint", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Store: store, Inputs: inputs,
		Factory: state.FactoryInfo{Name: "node-taint", Version: "v0", ContentRevision: "c0"},
	}
}

type nodeTaintRequest struct {
	method, name, manager, force string
	taints                       []svc.NodeTaint
}

type nodeTaintRuntimeAPI struct {
	*httptest.Server
	mu                sync.Mutex
	nodes             map[string]*corev1.Node
	requests          []nodeTaintRequest
	patchStatus       *metav1.Status
	dropPatchedTaints bool
}

func newNodeTaintRuntimeAPI(t *testing.T) *nodeTaintRuntimeAPI {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	a := &nodeTaintRuntimeAPI{nodes: map[string]*corev1.Node{}}
	for _, name := range []string{"node", "other"} {
		a.nodes[name] = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		}
	}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		require.True(t, strings.HasPrefix(req.URL.Path, "/api/v1/nodes/"), req.URL.Path)
		name := strings.TrimPrefix(req.URL.Path, "/api/v1/nodes/")
		call := nodeTaintRequest{method: req.Method, name: name}
		defer func() { a.requests = append(a.requests, call) }()
		w.Header().Set("Content-Type", "application/json")
		live := a.nodes[name]
		if live == nil {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
				Status:   "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
			}))
			return
		}
		if req.Method == http.MethodPatch {
			call.manager, call.force = req.URL.Query().Get("fieldManager"), req.URL.Query().Get("force")
			var patch struct {
				Metadata metav1.ObjectMeta
				Spec     svc.NodeTaintSpec
			}
			require.NoError(t, json.NewDecoder(req.Body).Decode(&patch))
			require.Equal(t, name, patch.Metadata.Name)
			require.Equal(t, live.UID, patch.Metadata.UID)
			call.taints = patch.Spec.Taints
			if a.patchStatus != nil {
				w.WriteHeader(int(a.patchStatus.Code))
				require.NoError(t, json.NewEncoder(w).Encode(a.patchStatus))
				return
			}
			live.Spec.Taints = nil
			if !a.dropPatchedTaints {
				for _, taint := range patch.Spec.Taints {
					live.Spec.Taints = append(live.Spec.Taints, corev1.Taint{
						Key: taint.Key, Value: taint.Value, Effect: corev1.TaintEffect(taint.Effect),
					})
				}
			}
		} else {
			require.Equal(t, http.MethodGet, req.Method)
		}
		require.NoError(t, json.NewEncoder(w).Encode(live))
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *nodeTaintRuntimeAPI) drain() []nodeTaintRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.requests
	a.requests = nil
	return calls
}

func (a *nodeTaintRuntimeAPI) alter(name string, change func(*corev1.Node)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	change(a.nodes[name])
}

func nodeTaintGet(name string) nodeTaintRequest {
	return nodeTaintRequest{method: http.MethodGet, name: name}
}

func nodeTaintPatch(name, manager, force string, taints []svc.NodeTaint) nodeTaintRequest {
	return nodeTaintRequest{method: http.MethodPatch, name: name,
		manager: manager, force: force, taints: taints}
}

func nodeTaintInputs(server, name string, taints []svc.NodeTaint) map[string]any {
	return map[string]any{
		"server": server, "manager": nil, "force": nil,
		"object": map[string]any{
			"metadata": map[string]any{"name": name},
			"spec":     map[string]any{"taints": nodeTaintValues(taints)},
		},
	}
}

func nodeTaintValues(taints []svc.NodeTaint) []any {
	out := make([]any, 0, len(taints))
	for _, taint := range taints {
		out = append(out, map[string]any{
			"key": taint.Key, "value": taint.Value, "effect": taint.Effect,
		})
	}
	return out
}

func nodeTaintPlanStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.attachment" {
			return step
		}
	}
	t.Fatal("plan lacks node-taint attachment")
	return nil
}

func planNodeTaint(
	t *testing.T, e *runtime.Executor, decision runtime.Decision,
) (*runtime.Plan, *runtime.PlanStep) {
	t.Helper()
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	step := nodeTaintPlanStep(t, plan)
	require.Equal(t, decision, step.Decision)
	return plan, step
}

func requireNodeTaintRule(t *testing.T, err error, rule string) {
	t.Helper()
	require.ErrorContains(t, err, "resource.attachment: schema: constraints[")
	require.ErrorContains(t, err, rule)
}

func TestNodeTaintEvaluatedNativeInputs(t *testing.T) {
	lib := nodeTaintLibrary(t)
	api := newNodeTaintRuntimeAPI(t)
	valid := []svc.NodeTaint{{Key: "example.com/a", Value: "", Effect: "NoSchedule"}}
	for _, tc := range []struct {
		test, name, rule string
		manager          *string
		taints           []svc.NodeTaint
	}{
		{"valid", "node.example", "", new("  manager  "), valid},
		{"max-name", strings.Repeat("a", 253), "", nil, valid},
		{"empty-name", "", nodeTaintNameRule, nil, valid},
		{"dns-name", "bad_name", nodeTaintNameRule, nil, valid},
		{"long-name", strings.Repeat("a", 254), nodeTaintNameRule, nil, valid},
		{"empty-list", "node", nodeTaintLengthRule, nil, nil},
		{"duplicate-keys", "node", nodeTaintUniqueRule, nil, []svc.NodeTaint{
			{Key: "a", Value: "one", Effect: "NoExecute"},
			{Key: "a", Value: "two", Effect: "NoSchedule"},
		}},
		{"empty-manager", "node", nodeTaintManagerRule, new(""), valid},
		{"blank-ascii-manager", "node", nodeTaintManagerRule, new(" \t\n"), valid},
		{"blank-unicode-manager", "node", nodeTaintManagerRule, new("\u00a0\u0085\u2003\u3000"), valid},
	} {
		t.Run(tc.test, func(t *testing.T) {
			inputs := nodeTaintInputs(api.URL, tc.name, tc.taints)
			if tc.manager != nil {
				inputs["manager"] = *tc.manager
			}
			e := nodeTaintExecutor(t, lib, "runtime", inputs)
			if tc.rule == "" {
				_, _ = planNodeTaint(t, e, runtime.DecisionCreate)
			} else {
				_, err := e.Plan(t.Context())
				requireNodeTaintRule(t, err, tc.rule)
			}
			require.Empty(t, api.drain())
		})
	}
}

func TestNodeTaintRuntimeLifecycle(t *testing.T) {
	lib := nodeTaintLibrary(t)
	api := newNodeTaintRuntimeAPI(t)
	early := []svc.NodeTaint{
		{Key: "example.com/b", Value: "", Effect: "PreferNoSchedule"},
		{Key: "example.com/a", Value: "first", Effect: "NoSchedule"},
	}
	e := nodeTaintExecutor(t, lib, "runtime-omitted", nodeTaintInputs(api.URL, "node", early))
	create, _ := planNodeTaint(t, e, runtime.DecisionCreate)
	require.Empty(t, api.drain())
	created, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, create))
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"name": "node", "uid": "uid-node", "taints": nodeTaintValues(early),
	}, created.Outputs)
	require.Equal(t, []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintPatch("node", "Terraform", "false", early),
		nodeTaintGet("node"),
	}, api.drain())

	store := e.Store
	for _, tt := range []struct {
		fixture        string
		manager, force any
	}{
		{"runtime-omitted", nil, nil},
		{"runtime", nil, nil},
		{"runtime", "Terraform", false},
	} {
		inputs := nodeTaintInputs(api.URL, "node", early)
		inputs["manager"], inputs["force"] = tt.manager, tt.force
		e = nodeTaintExecutor(t, lib, tt.fixture, inputs)
		e.Store = store
		steady, _ := planNodeTaint(t, e, runtime.DecisionNoOp)
		require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
		result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, steady))
		require.NoError(t, err)
		require.Equal(t, created.Outputs, result.Outputs)
		require.Empty(t, api.drain())
	}

	for _, tc := range []struct {
		name    string
		taints  []svc.NodeTaint
		manager *string
		force   *bool
	}{
		{"value", []svc.NodeTaint{early[0], {Key: early[1].Key, Value: "new",
			Effect: early[1].Effect}}, nil, nil},
		{"effect", []svc.NodeTaint{{Key: early[0].Key, Effect: "NoExecute"}, early[1]}, nil, nil},
		{"order", []svc.NodeTaint{early[1], early[0]}, nil, nil},
		{"length", []svc.NodeTaint{early[0]}, nil, nil},
		{"manager", early, new("different"), nil},
		{"force", early, nil, new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.Inputs = nodeTaintInputs(api.URL, "node", tc.taints)
			if tc.manager != nil {
				e.Inputs["manager"] = *tc.manager
			}
			if tc.force != nil {
				e.Inputs["force"] = *tc.force
			}
			_, step := planNodeTaint(t, e, runtime.DecisionUpdate)
			require.Empty(t, step.ReplacementReasons)
			require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
		})
	}

	external := []svc.NodeTaint{
		{Key: early[1].Key, Value: "external", Effect: "NoExecute"},
		{Key: early[1].Key, Effect: "NoSchedule"},
	}
	api.alter("node", func(live *corev1.Node) {
		live.Spec.Taints = []corev1.Taint{
			{Key: external[0].Key, Value: external[0].Value, Effect: corev1.TaintEffectNoExecute},
			{Key: external[1].Key, Effect: corev1.TaintEffectNoSchedule},
		}
	})
	e.Inputs = nodeTaintInputs(api.URL, "node", early)
	_, drift := planNodeTaint(t, e, runtime.DecisionUpdate)
	observed := map[string]any{
		"server": api.URL, "api-version": "v1", "kind": "Node", "object": map[string]any{
			"metadata": map[string]any{"name": "node", "uid": "uid-node"},
			"spec":     map[string]any{"taints": nodeTaintValues(external)},
		},
	}
	require.Equal(t, observed, drift.ObservedOutputs)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	api.alter("node", func(live *corev1.Node) { live.Spec.Taints = nil })
	repair, drift := planNodeTaint(t, e, runtime.DecisionUpdate)
	observed["object"].(map[string]any)["spec"] = map[string]any{"taints": []any{}}
	require.Equal(t, observed, drift.ObservedOutputs)
	require.Empty(t, drift.ReplacementReasons)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	updated, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, repair))
	require.NoError(t, err)
	require.Equal(t, created.Outputs, updated.Outputs)
	require.Equal(t, []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintPatch("node", "Terraform", "false", early),
		nodeTaintGet("node"),
	}, api.drain())

	e.Inputs["manager"], e.Inputs["force"] = "old-owner", true
	transition, _ := planNodeTaint(t, e, runtime.DecisionUpdate)
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, transition))
	require.NoError(t, err)
	require.Equal(t, []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintGet("node"),
		nodeTaintPatch("node", "old-owner", "true", early), nodeTaintGet("node"),
	}, api.drain())

	e.Inputs = nodeTaintInputs(api.URL, "other", early)
	replacing, replacement := planNodeTaint(t, e, runtime.DecisionReplace)
	require.Equal(t, []string{"object.metadata.name"}, replacement.ReplacementReasons)
	require.Equal(t, new(api.URL+"/uid-node"), replacement.ExpectedStableID)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	replaced, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, replacing))
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"name": "other", "uid": "uid-other", "taints": nodeTaintValues(early),
	}, replaced.Outputs)
	require.Equal(t, []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintGet("node"),
		nodeTaintPatch("node", "old-owner", "true", []svc.NodeTaint{}),
		nodeTaintGet("other"), nodeTaintPatch("other", "Terraform", "false", early),
		nodeTaintGet("other"),
	}, api.drain())
	api.alter("node", func(live *corev1.Node) {
		require.Equal(t, types.UID("uid-node"), live.UID)
		require.Empty(t, live.Spec.Taints)
	})
}

func TestNodeTaintReplaceTargetBinding(t *testing.T) {
	reg := Library().Resources["node-taint"]
	taints := []svc.NodeTaint{{Key: "example.com/a", Effect: "NoExecute"}}
	object := nodeTaintInputs("unused", "node", taints)["object"]
	inputs := map[string]any{"object": object}
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		t.Run(field, func(t *testing.T) {
			c := &config.Configuration{}
			require.NoError(t, runtime.Decode(c, map[string]any{field: "changed"}))
			require.Equal(t, []string{field}, replacementReasons(t, reg, inputs, inputs,
				nil, nil, map[string]any{field: "saved"}, c))
		})
	}
}

func TestNodeTaintMissingNodeLifecycle(t *testing.T) {
	lib := nodeTaintLibrary(t)
	api := newNodeTaintRuntimeAPI(t)
	taints := []svc.NodeTaint{{Key: "example.com/a", Value: "first", Effect: "NoExecute"}}
	e := nodeTaintExecutor(t, lib, "runtime", nodeTaintInputs(api.URL, "node", taints))
	create, _ := planNodeTaint(t, e, runtime.DecisionCreate)
	created, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, create))
	require.NoError(t, err)
	require.Equal(t, []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintPatch("node", "Terraform", "false", taints),
		nodeTaintGet("node"),
	}, api.drain())
	api.alter("node", func(_ *corev1.Node) { delete(api.nodes, "node") })
	steady, _ := planNodeTaint(t, e, runtime.DecisionNoOp)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	noOp, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, steady))
	require.NoError(t, err)
	require.Equal(t, created.Outputs, noOp.Outputs)
	require.Empty(t, api.drain())

	taints[0].Value = "next"
	e.Inputs = nodeTaintInputs(api.URL, "node", taints)
	update, _ := planNodeTaint(t, e, runtime.DecisionUpdate)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, update))
	require.ErrorContains(t, err, "must exist before applying taints")
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	current, err := e.Store.Current()
	require.NoError(t, err)
	require.Equal(t, "uid-node", current.Outputs["uid"])

	e.Destroy = true
	destroy, _ := planNodeTaint(t, e, runtime.DecisionDestroy)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	deleted, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, destroy))
	require.NoError(t, err)
	require.Empty(t, deleted.Outputs)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node"), nodeTaintGet("node")}, api.drain())
	current, err = e.Store.Current()
	require.NoError(t, err)
	require.Empty(t, current.Entries)
}

func TestNodeTaintSavedNoOpAndHistoricalTeardown(t *testing.T) {
	lib := nodeTaintLibrary(t)
	api := newNodeTaintRuntimeAPI(t)
	invalid := []svc.NodeTaint{
		{Key: "example.com/a", Value: "one", Effect: "NoSchedule"},
		{Key: "example.com/a", Value: "two", Effect: "NoExecute"},
	}
	inputs := nodeTaintInputs(api.URL, "node", invalid)
	inputs["manager"], inputs["force"] = "historical", true
	e := nodeTaintExecutor(t, lib, "runtime", inputs)
	rules := lib.Constraints
	lib.Constraints = nil
	old, _ := planNodeTaint(t, e, runtime.DecisionCreate)
	_, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, old))
	require.NoError(t, err)
	require.Equal(t, []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintPatch("node", "historical", "true", invalid),
		nodeTaintGet("node"),
	}, api.drain())
	steady, _ := planNodeTaint(t, e, runtime.DecisionNoOp)
	file := configMapInfoSavedPlan(t, steady)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())

	lib.Constraints = rules
	_, err = e.Plan(t.Context())
	requireNodeTaintRule(t, err, nodeTaintUniqueRule)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	_, err = e.ApplyPlan(t.Context(), file)
	requireNodeTaintRule(t, err, nodeTaintUniqueRule)
	require.Empty(t, api.drain())

	e.Destroy = true
	destroy, _ := planNodeTaint(t, e, runtime.DecisionDestroy)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	api.alter("node", func(_ *corev1.Node) {
		api.patchStatus = &metav1.Status{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
			Status:   "Failure", Reason: metav1.StatusReasonConflict, Code: http.StatusConflict,
			Message: "foreign manager still owns the list",
			Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{
				{Type: metav1.CauseTypeFieldManagerConflict, Field: ".spec.taints"},
			}},
		}
	})
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, destroy))
	require.ErrorContains(t, err, "set force to true")
	clearCalls := []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintGet("node"),
		nodeTaintPatch("node", "historical", "true", []svc.NodeTaint{}),
	}
	require.Equal(t, clearCalls, api.drain())
	prior, err := e.Store.Current()
	require.NoError(t, err)
	require.Equal(t, nodeTaintValues(invalid), prior.Outputs["taints"])
	require.Equal(t, "uid-node", prior.Outputs["uid"])

	api.alter("node", func(_ *corev1.Node) { api.patchStatus = nil })
	destroy, _ = planNodeTaint(t, e, runtime.DecisionDestroy)
	require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
	removed, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, destroy))
	require.NoError(t, err)
	require.Empty(t, removed.Outputs)
	require.Equal(t, clearCalls, api.drain())
	api.alter("node", func(live *corev1.Node) {
		require.Equal(t, types.UID("uid-node"), live.UID)
		require.Empty(t, live.Spec.Taints)
	})
}

func TestNodeTaintEmptyPostWriteIsUnsettled(t *testing.T) {
	lib := nodeTaintLibrary(t)
	api := newNodeTaintRuntimeAPI(t)
	api.alter("node", func(_ *corev1.Node) { api.dropPatchedTaints = true })
	taints := []svc.NodeTaint{{Key: "example.com/a", Effect: "NoExecute"}}
	e := nodeTaintExecutor(t, lib, "runtime", nodeTaintInputs(api.URL, "node", taints))
	create, _ := planNodeTaint(t, e, runtime.DecisionCreate)
	_, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, create))
	require.ErrorContains(t, err, "has no taints after applying")
	require.Equal(t, []nodeTaintRequest{
		nodeTaintGet("node"), nodeTaintPatch("node", "Terraform", "false", taints),
		nodeTaintGet("node"),
	}, api.drain())
	_, _ = planNodeTaint(t, e, runtime.DecisionCreate)
	require.Empty(t, api.drain())
}

type nodeTaintTestAction struct {
	DesiredKey    string
	Revision      string
	DesiredTaints []svc.NodeTaint
	onRun         func()
}

type nodeTaintTestActionOutput struct {
	Key    string
	Taints []svc.NodeTaint
}

func (a *nodeTaintTestAction) Run(context.Context, any) (*nodeTaintTestActionOutput, error) {
	a.onRun()
	return &nodeTaintTestActionOutput{Key: a.DesiredKey, Taints: a.DesiredTaints}, nil
}

func TestNodeTaintDeferredNativeInputs(t *testing.T) {
	lib := nodeTaintLibrary(t)
	for _, tc := range []struct {
		name, fixture, node, key, rule string
		replace, rejectPlan            bool
		list                           []svc.NodeTaint
	}{
		{name: "valid key", fixture: "deferred", node: "node", key: "example.com/second"},
		{name: "deferred duplicate key", fixture: "deferred", node: "node",
			key: "example.com/first", rule: nodeTaintUniqueRule},
		{name: "deferred nonempty list", fixture: "deferred-list", node: "node",
			list: []svc.NodeTaint{{Key: "example.com/list", Value: "", Effect: "NoExecute"}}},
		{name: "deferred empty list", fixture: "deferred-list", node: "node",
			rule: nodeTaintLengthRule},
		{name: "known duplicates with pending value", fixture: "deferred-value", node: "node",
			rule: nodeTaintUniqueRule, rejectPlan: true},
		{name: "pending replacement", fixture: "deferred", node: "other",
			key: "example.com/first", rule: nodeTaintUniqueRule, replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newNodeTaintRuntimeAPI(t)
			first := []svc.NodeTaint{{Key: "example.com/first", Effect: "NoSchedule"}}
			base := nodeTaintExecutor(t, lib, "runtime", nodeTaintInputs(api.URL, "node", first))
			if tc.replace {
				initial, _ := planNodeTaint(t, base, runtime.DecisionCreate)
				_, err := base.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, initial))
				require.NoError(t, err)
				require.Equal(t, []nodeTaintRequest{
					nodeTaintGet("node"), nodeTaintPatch("node", "Terraform", "false", first),
					nodeTaintGet("node"),
				}, api.drain())
			}
			var actions []string
			lib.Actions = map[string]runtime.ActionRegistration{
				"test-taint-input": runtime.MakeActionWith[
					nodeTaintTestAction, *nodeTaintTestActionOutput, any,
				](func() *nodeTaintTestAction {
					return &nodeTaintTestAction{onRun: func() {
						require.Empty(t, api.drain())
						actions = append(actions, "source")
					}}
				}),
			}
			e := nodeTaintExecutor(t, lib, tc.fixture, map[string]any{
				"server": api.URL, "name": tc.node, "key": tc.key, "revision": "one",
				"value": "second", "taints": nodeTaintValues(tc.list),
			})
			e.Store = base.Store
			plan, err := e.Plan(t.Context())
			require.Empty(t, actions)
			if tc.rejectPlan {
				requireNodeTaintRule(t, err, tc.rule)
				require.Empty(t, api.drain())
				return
			}
			require.NoError(t, err)
			step := nodeTaintPlanStep(t, plan)
			require.NotEmpty(t, step.UnresolvedInputs)
			if tc.replace {
				require.Equal(t, runtime.DecisionReplace, step.Decision)
				require.Equal(t, []string{"object.metadata.name"}, step.PendingReplacementReasons)
				require.Equal(t, []nodeTaintRequest{nodeTaintGet("node")}, api.drain())
			} else {
				require.Equal(t, runtime.DecisionCreate, step.Decision)
				require.Empty(t, api.drain())
			}
			file := configMapInfoSavedPlan(t, plan)
			e.Inputs = nil
			_, err = e.ApplyPlan(t.Context(), file)
			require.Equal(t, []string{"source"}, actions)
			if tc.rule != "" {
				requireNodeTaintRule(t, err, tc.rule)
				require.Empty(t, api.drain())
				if tc.replace {
					current, err := e.Store.Current()
					require.NoError(t, err)
					require.Equal(t, "uid-node", current.Outputs["uid"])
				}
				return
			}
			require.NoError(t, err)
			want := tc.list
			if tc.fixture == "deferred" {
				want = append(first, svc.NodeTaint{Key: tc.key, Value: "b", Effect: "NoExecute"})
			}
			require.Equal(t, []nodeTaintRequest{
				nodeTaintGet(tc.node), nodeTaintPatch(tc.node, "Terraform", "false", want),
				nodeTaintGet(tc.node),
			}, api.drain())
		})
	}
}
