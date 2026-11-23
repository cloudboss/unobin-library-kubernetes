package v1_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	node "github.com/cloudboss/unobin-library-kubernetes/api/node/v1"
	"github.com/cloudboss/unobin-library-kubernetes/config"
)

var runtimeClassSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/node/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}

	resource := schema.Resources["runtime-class"]
	resource.EvaluatedInputs = resource.Inputs
	return schema, nil
})

func runtimeClassLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := runtimeClassSchema()
	require.NoError(t, err)
	lib := node.Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.runtime-class": schema.Resources["runtime-class"].Constraints,
	}
	return lib
}

func classExecutor(
	t *testing.T, fixture, dir string, lib *runtime.Library, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub", fixture))
	require.NoError(t, err)
	source, err := syntax.ParseSource(filepath.Base(fixture), body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"node": lib}
	store, err := local.NewStore(dir, "runtime-class", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(source.Factory.Body, libs), SyntaxSource: &source.Factory.Body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "runtime-class", Version: "v0", ContentRevision: "c0"},
	}
}

func saveAndApply(t *testing.T, e *runtime.Executor, plan *runtime.Plan) error {
	t.Helper()
	encoded, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), saved)
	return err
}

func classStep(t *testing.T, plan *runtime.Plan, decision runtime.Decision) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			require.Equal(t, decision, step.Decision)
			return step
		}
	}
	t.Fatal("plan omitted resource.subject")
	return nil
}

func assertRuntimeClassConstraint(t *testing.T, err error, rule string) {
	t.Helper()
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, rule)
}

func runtimeClassStateAPI(
	t *testing.T, onCreate func(*nodev1.RuntimeClass),
) (*config.Configuration, func() []request, map[string]*nodev1.RuntimeClass) {
	t.Helper()
	objects := map[string]*nodev1.RuntimeClass{}
	nextUID := 0
	c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		switch r.Method {
		case http.MethodPost:
			obj := &nodev1.RuntimeClass{}
			if err := json.NewDecoder(r.Body).Decode(obj); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if obj.Name == "" {
				obj.Name = obj.GenerateName + "actual"
			}
			if _, exists := objects[obj.Name]; exists {
				fail(t, w, 409, metav1.StatusReasonAlreadyExists)
				return
			}
			nextUID++
			obj.UID = types.UID(fmt.Sprintf("uid-%d", nextUID))
			if onCreate != nil {
				onCreate(obj)
			}
			objects[obj.Name] = obj
			respond(t, w, obj)
		case http.MethodGet:
			obj := objects[name]
			if obj == nil {
				fail(t, w, 404, metav1.StatusReasonNotFound)
				return
			}
			respond(t, w, obj)
		case http.MethodPatch:
			if err := patchClass(r, objects[name]); err != nil {
				t.Error(err)
				fail(t, w, 422, metav1.StatusReasonInvalid)
				return
			}
			respond(t, w, objects[name])
		case http.MethodDelete:
			obj := objects[name]
			if obj == nil {
				fail(t, w, 404, metav1.StatusReasonNotFound)
				return
			}
			var options metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if !assert.Equal(t, &metav1.Preconditions{UID: &obj.UID}, options.Preconditions) {
				fail(t, w, 409, metav1.StatusReasonConflict)
				return
			}
			delete(objects, name)
			respond(t, w, metav1.Status{Status: "Success"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(405)
		}
	})
	return c, calls, objects
}

func runtimeClassInputs(c *config.Configuration) map[string]any {
	return map[string]any{
		"server": *c.Server, "metadata": map[string]any{"name": "example"}, "handler": "run-1",
		"token": nil, "kubeconfig": nil, "context": nil, "ignore-labels": nil, "ignore-annotations": nil,
	}
}

func planRuntimeClass(
	t *testing.T, e *runtime.Executor, decision runtime.Decision,
) (*runtime.Plan, *runtime.PlanStep) {
	t.Helper()
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	return p, classStep(t, p, decision)
}

func TestRuntimeClassPlanReplacement(t *testing.T) {
	c, calls, objects := runtimeClassStateAPI(t, nil)
	inputs := runtimeClassInputs(c)
	kubeconfig, err := filepath.Abs("../../../client/testdata/contexts.yaml")
	require.NoError(t, err)
	t.Setenv("KUBECONFIG", kubeconfig)
	dir, lib := t.TempDir(), runtimeClassLibrary(t)
	executor := func(values map[string]any) *runtime.Executor {
		return classExecutor(t, "factory.ub", dir, lib, values)
	}
	first := executor(inputs)
	created, _ := planRuntimeClass(t, first, runtime.DecisionCreate)
	require.NoError(t, saveAndApply(t, first, created))
	require.Equal(t, types.UID("uid-1"), objects["example"].UID)

	objects["example"].Generation, objects["example"].ResourceVersion = 8, "new-version"
	noopInputs := maps.Clone(inputs)
	noopInputs["metadata"] = map[string]any{"name": "example", "labels": map[string]any{}}
	noopInputs["token"] = "current"
	before := len(calls())
	noop := executor(noopInputs)
	noopPlan, step := planRuntimeClass(t, noop, runtime.DecisionNoOp)
	require.False(t, step.Drift())
	require.NoError(t, saveAndApply(t, noop, noopPlan))
	require.Equal(t, []request{{method: "GET", path: examplePath, token: "Bearer current"}},
		calls()[before:])

	for _, tc := range []struct {
		name, field, reason string
		value               any
	}{
		{"handler", "handler", "handler", "run-2"},
		{"name", "metadata", "metadata", map[string]any{"name": "renamed"}},
		{"switch to generated", "metadata", "metadata", map[string]any{"generate-name": "next-"}},
		{"server", "server", "server", "https://127.0.0.1:1"},
		{"kubeconfig", "kubeconfig", "kubeconfig", kubeconfig},
		{"context", "context", "current-context", "prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replacement := maps.Clone(inputs)
			replacement[tc.field] = tc.value
			_, step := planRuntimeClass(t, executor(replacement), runtime.DecisionReplace)
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
		})
	}

	objects["example"].Handler = "drifted"
	_, step = planRuntimeClass(t, executor(inputs), runtime.DecisionReplace)
	require.Equal(t, []string{"handler"}, step.ReplacementReasons)
	require.Equal(t, "drifted", step.ObservedOutputs["handler"])
	objects["example"].Handler = "run-1"

	inputs["handler"] = "run-2"
	e := executor(inputs)
	p, step := planRuntimeClass(t, e, runtime.DecisionReplace)
	require.Equal(t, new(*c.Server+"/uid-1"), step.ExpectedStableID)
	before = len(calls())
	require.NoError(t, saveAndApply(t, e, p))
	got := calls()[before:]
	require.Equal(t, []request{
		{method: "GET", path: examplePath, token: "Bearer dev-token"},
		{method: "DELETE", path: examplePath, token: "Bearer dev-token", body: got[1].body},
		{method: "POST", path: runtimeClassPath, token: "Bearer dev-token", body: got[2].body},
		{method: "GET", path: examplePath, token: "Bearer dev-token"},
	}, got)
	require.Equal(t, types.UID("uid-2"), objects["example"].UID)
	require.Equal(t, "run-2", objects["example"].Handler)
}

func TestRuntimeClassPlanMetadataFilters(t *testing.T) {
	c, calls, objects := runtimeClassStateAPI(t, func(obj *nodev1.RuntimeClass) {
		obj.Annotations["example.org/controller"] = "annotation"
		obj.Labels["example.org/controller"] = "label"
		obj.Labels["other.org/controller"] = "already-filtered"
	})
	inputs := runtimeClassInputs(c)
	managed := map[string]any{"example.org/managed": "written"}
	inputs["metadata"] = map[string]any{
		"name": "example", "annotations": managed, "labels": managed,
	}
	inputs["ignore-labels"] = []any{"^other[.]org/"}
	dir, lib := t.TempDir(), runtimeClassLibrary(t)
	executor := func() *runtime.Executor {
		return classExecutor(t, "factory.ub", dir, lib, inputs)
	}
	apply := func(d runtime.Decision) {
		t.Helper()
		e := executor()
		p, _ := planRuntimeClass(t, e, d)
		require.NoError(t, saveAndApply(t, e, p))
	}
	assertOutputs := func(labels, annotations map[string]any) {
		t.Helper()
		_, step := planRuntimeClass(t, executor(), runtime.DecisionNoOp)
		want := map[string]any{
			"server": *c.Server, "api-version": "node.k8s.io/v1", "kind": "RuntimeClass",
			"handler": "run-1", "metadata": map[string]any{
				"name": "example", "uid": "uid-1", "generate-name": "",
				"labels": labels, "annotations": annotations,
			},
		}
		require.Equal(t, want, step.PriorOutputs)
		require.Equal(t, want, step.ObservedOutputs)
	}

	apply(runtime.DecisionCreate)
	assertOutputs(
		map[string]any{"example.org/controller": "label", "example.org/managed": "written"},
		map[string]any{"example.org/controller": "annotation", "example.org/managed": "written"},
	)
	inputs["ignore-labels"] = []any{"^other[.]org/", "^example[.]org/"}
	inputs["ignore-annotations"] = []any{"^example[.]org/"}
	before := len(calls())
	apply(runtime.DecisionUpdate)
	get := request{method: "GET", path: examplePath}
	require.Equal(t, []request{get, get}, calls()[before:])
	require.Equal(t, map[string]string{
		"example.org/controller": "label", "example.org/managed": "written",
		"other.org/controller": "already-filtered",
	}, objects["example"].Labels)
	require.Equal(t, map[string]string{
		"example.org/controller": "annotation", "example.org/managed": "written",
	}, objects["example"].Annotations)
	assertOutputs(managed, managed)

	inputs["ignore-labels"] = []any{"^example[.]org/", "^other[.]org/", "^example[.]org/"}
	_, step := planRuntimeClass(t, executor(), runtime.DecisionNoOp)
	require.False(t, step.Drift())

	inputs["metadata"] = map[string]any{"name": "example"}
	before = len(calls())
	e := executor()
	p, _ := planRuntimeClass(t, e, runtime.DecisionUpdate)
	objects["example"].Labels["other.org/late"] = "added-after-plan"
	require.NoError(t, saveAndApply(t, e, p))
	require.Equal(t, []request{get, get, {
		method: "PATCH", path: examplePath,
		body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
			`{"op":"remove","path":"/metadata/annotations/example.org~1managed"},` +
			`{"op":"remove","path":"/metadata/labels/example.org~1managed"}]`,
	}, get}, calls()[before:])
	assertOutputs(map[string]any{}, map[string]any{})

	inputs["ignore-annotations"] = nil
	before = len(calls())
	apply(runtime.DecisionUpdate)
	require.Equal(t, []request{get, get, {
		method: "PATCH", path: examplePath,
		body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
			`{"op":"remove","path":"/metadata/annotations/example.org~1controller"}]`,
	}, get}, calls()[before:])
	require.Equal(t, map[string]string{
		"example.org/controller": "label", "other.org/controller": "already-filtered",
		"other.org/late": "added-after-plan",
	}, objects["example"].Labels)
	require.Empty(t, objects["example"].Annotations)
	assertOutputs(map[string]any{}, map[string]any{})
}

func TestRuntimeClassSavedWholeMetadataPatch(t *testing.T) {
	c, calls, objects := runtimeClassStateAPI(t, func(obj *nodev1.RuntimeClass) {
		obj.Annotations = map[string]string{"operator.example/legacy": "external"}
	})
	inputs := runtimeClassInputs(c)
	dir, lib := t.TempDir(), runtimeClassLibrary(t)
	executor := func() *runtime.Executor {
		return classExecutor(t, "factory.ub", dir, lib, inputs)
	}
	e := executor()
	p, _ := planRuntimeClass(t, e, runtime.DecisionCreate)
	require.NoError(t, saveAndApply(t, e, p))
	require.Nil(t, objects["example"].Labels)
	require.Equal(t, map[string]string{
		"operator.example/legacy": "external",
	}, objects["example"].Annotations)

	inputs["ignore-annotations"] = []any{"^operator[.]example/"}
	inputs["metadata"] = map[string]any{
		"name":        "example",
		"annotations": map[string]any{"note": "new"},
		"labels":      map[string]any{"label": ""},
	}
	before := len(calls())
	e = executor()
	p, _ = planRuntimeClass(t, e, runtime.DecisionUpdate)
	require.NoError(t, saveAndApply(t, e, p))
	get := request{method: "GET", path: examplePath}
	require.Equal(t, []request{get, get, {
		method: "PATCH", path: examplePath,
		body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
			`{"op":"add","path":"/metadata/annotations","value":{"note":"new"}},` +
			`{"op":"add","path":"/metadata/labels","value":{"label":""}}]`,
	}, get}, calls()[before:])
	obj := objects["example"]
	require.Equal(t, map[string]string{"note": "new"}, obj.Annotations)
	require.Equal(t, map[string]string{"label": ""}, obj.Labels)
	require.Equal(t, types.UID("uid-1"), obj.UID)

	_, step := planRuntimeClass(t, executor(), runtime.DecisionNoOp)
	want := map[string]any{
		"server": *c.Server, "api-version": "node.k8s.io/v1", "kind": "RuntimeClass",
		"handler": "run-1", "metadata": map[string]any{
			"name": "example", "uid": "uid-1", "generate-name": "",
			"annotations": map[string]any{"note": "new"},
			"labels":      map[string]any{"label": ""},
		},
	}
	require.Equal(t, want, step.PriorOutputs)
	require.Equal(t, want, step.ObservedOutputs)
}

func TestRuntimeClassSavedNoOpConstraintsAndHistoricalDestroy(t *testing.T) {
	c, calls, objects := runtimeClassStateAPI(t, nil)
	inputs := runtimeClassInputs(c)
	inputs["metadata"] = map[string]any{"generate-name": "example-"}
	inputs["ignore-labels"] = []any{"["}
	dir, lib := t.TempDir(), runtimeClassLibrary(t)
	executor := func() *runtime.Executor {
		return classExecutor(t, "factory.ub", dir, lib, inputs)
	}
	constraints := lib.Constraints
	lib.Constraints = nil
	create := executor()
	plan, _ := planRuntimeClass(t, create, runtime.DecisionCreate)
	require.NoError(t, saveAndApply(t, create, plan))

	noop := executor()
	plan, step := planRuntimeClass(t, noop, runtime.DecisionNoOp)
	require.Equal(t, step.PriorInputs, step.Inputs)
	lib.Constraints = constraints
	before := len(calls())
	assertRuntimeClassConstraint(t, saveAndApply(t, noop, plan), "ignore-labels")
	require.Empty(t, calls()[before:])

	_, err := executor().Plan(t.Context())
	assertRuntimeClassConstraint(t, err, "ignore-labels")
	get := request{method: "GET", path: runtimeClassPath + "/example-actual"}
	require.Equal(t, []request{get}, calls()[before:])

	changed := maps.Clone(inputs)
	changed["ignore-labels"] = nil
	changed["metadata"] = map[string]any{"generate-name": "replacement-"}
	replacement := classExecutor(t, "factory.ub", dir, lib, changed)
	_, step = planRuntimeClass(t, replacement, runtime.DecisionReplace)
	require.Equal(t, []string{"metadata"}, step.ReplacementReasons)

	before = len(calls())
	destroy := executor()
	destroy.Destroy = true
	plan, step = planRuntimeClass(t, destroy, runtime.DecisionDestroy)
	require.Equal(t, []any{"["}, step.PriorInputs["ignore-labels"])
	require.NoError(t, saveAndApply(t, destroy, plan))
	got := calls()[before:]
	require.Equal(t, []request{get, get, {method: "DELETE", path: get.path, body: got[2].body}},
		got)
	require.JSONEq(t, `{
		"kind":"DeleteOptions","apiVersion":"node.k8s.io/v1","preconditions":{"uid":"uid-1"}
	}`, got[2].body)
	require.Empty(t, objects)
}

func TestRuntimeClassEvaluatedStructuralInputs(t *testing.T) {
	c, calls, _ := runtimeClassStateAPI(t, nil)
	lib := runtimeClassLibrary(t)
	for _, tc := range []struct {
		field      string
		value      any
		diagnostic string
	}{
		{"metadata", nil, `field "metadata": required but is null`},
		{"handler", nil, `field "handler": required but is null`},
		{"ignore-labels", []any{nil}, `field "ignore-labels[0]": required but is null`},
		{"ignore-annotations", []any{nil}, `field "ignore-annotations[0]": required but is null`},
		{"metadata", map[string]any{"name": "example", "labels": map[string]any{"key": nil}},
			`field "metadata.labels[\"key\"]": required but is null`},
		{"metadata", map[string]any{"name": "example", "annotations": map[string]any{"key": nil}},
			`field "metadata.annotations[\"key\"]": required but is null`},
	} {
		t.Run(tc.diagnostic, func(t *testing.T) {
			inputs := runtimeClassInputs(c)
			inputs[tc.field] = tc.value
			e := classExecutor(t, "factory.ub", t.TempDir(), lib, inputs)
			_, err := e.Plan(t.Context())
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, calls())
		})
	}
}

func TestRuntimeClassPartiallyKnownConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, fixture string
		options       map[string]any
		rule          string
	}{
		{"handler with pending metadata", "pending-literal/factory.ub",
			map[string]any{"handler": "run!"}, "handler must be a lowercase DNS label"},
		{"key with pending value", "pending-literal/factory.ub",
			map[string]any{"handler": "run-1"}, "metadata.labels keys"},
		{"fixed comprehension key", "pending-comprehension/factory.ub",
			map[string]any{"pending-key": false}, "metadata.labels keys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, _ := runtimeClassStateAPI(t, nil)
			inputs := map[string]any{"server": *c.Server}
			maps.Copy(inputs, tc.options)
			e := classExecutor(t, tc.fixture, t.TempDir(), runtimeClassLibrary(t), inputs)
			_, err := e.Plan(t.Context())
			assertRuntimeClassConstraint(t, err, tc.rule)
			require.Empty(t, calls())
		})
	}
}

func TestRuntimeClassSavedDeferredHandler(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		decision     runtime.Decision
	}{
		{"valid create", "-new", runtime.DecisionCreate},
		{"invalid replacement", "!", runtime.DecisionReplace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, objects := runtimeClassStateAPI(t, nil)
			dir, lib := t.TempDir(), runtimeClassLibrary(t)
			isReplacement := tc.decision == runtime.DecisionReplace
			if isReplacement {
				e := classExecutor(t, "factory.ub", dir, lib, runtimeClassInputs(c))
				p, _ := planRuntimeClass(t, e, runtime.DecisionCreate)
				require.NoError(t, saveAndApply(t, e, p))
			}
			e := classExecutor(t, "deferred-handler/factory.ub", dir, lib, map[string]any{
				"server": *c.Server, "name": "next", "suffix": tc.suffix,
			})
			p, step := planRuntimeClass(t, e, tc.decision)
			require.Equal(t, map[string][]string{"handler": {"resource.seed.metadata.uid"}},
				step.UnresolvedInputs)
			before := len(calls())
			err := saveAndApply(t, e, p)
			got := calls()[before:]
			require.NotEmpty(t, got)
			expected := []request{
				{method: "POST", path: runtimeClassPath, body: got[0].body},
				{method: "GET", path: runtimeClassPath + "/seed"},
			}
			if isReplacement {
				require.Equal(t, []string{"metadata"}, step.ReplacementReasons)
				assertRuntimeClassConstraint(t, err, "handler must be a lowercase DNS label")
				require.Equal(t, types.UID("uid-1"), objects["example"].UID)
				require.Equal(t, "run-1", objects["example"].Handler)
				require.NotContains(t, objects, "next")
			} else {
				require.NoError(t, err)
				require.Greater(t, len(got), 2)
				expected = append(expected,
					request{method: "POST", path: runtimeClassPath, body: got[2].body},
					request{method: "GET", path: runtimeClassPath + "/next"},
					request{method: "GET", path: runtimeClassPath + "/seed"})
				require.Equal(t, "uid-1-new", objects["next"].Handler)
			}
			require.Equal(t, expected, got)
		})
	}
}

func TestRuntimeClassSavedDeferredMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, fixture, input, rule string
		decision                   runtime.Decision
	}{
		{"create", "pending-comprehension/factory.ub", "metadata", "metadata.labels keys",
			runtime.DecisionCreate},
		{"update", "deferred-update/factory.ub", "ignore-labels", "ignore-labels",
			runtime.DecisionUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, objects := runtimeClassStateAPI(t, nil)
			lib, dir := runtimeClassLibrary(t), t.TempDir()
			existing := tc.decision == runtime.DecisionUpdate
			if existing {
				initial := classExecutor(t, "factory.ub", dir, lib, runtimeClassInputs(c))
				p, _ := planRuntimeClass(t, initial, runtime.DecisionCreate)
				require.NoError(t, saveAndApply(t, initial, p))
			}
			beforePlan := len(calls())
			e := classExecutor(t, tc.fixture, dir, lib,
				map[string]any{"server": *c.Server, "pending-key": true})
			p, step := planRuntimeClass(t, e, tc.decision)
			require.Equal(t, map[string][]string{tc.input: {"resource.seed.metadata.uid"}},
				step.UnresolvedInputs)
			planned := calls()[beforePlan:]
			if existing {
				require.Equal(t, []request{{method: "GET", path: examplePath}}, planned)
			} else {
				require.Empty(t, planned)
			}
			beforeApply := len(calls())
			assertRuntimeClassConstraint(t, saveAndApply(t, e, p), tc.rule)
			got := calls()[beforeApply:]
			require.NotEmpty(t, got)
			require.Equal(t, []request{
				{method: "POST", path: runtimeClassPath, body: got[0].body},
				{method: "GET", path: runtimeClassPath + "/seed"},
			}, got)
			require.JSONEq(t, `{
				"kind":"RuntimeClass","apiVersion":"node.k8s.io/v1",
				"metadata":{"creationTimestamp":null,"name":"seed"},"handler":"run"
			}`, got[0].body)
			if existing {
				require.Equal(t, types.UID("uid-1"), objects["example"].UID)
				require.Empty(t, objects["example"].Labels)
			} else {
				require.NotContains(t, objects, "example")
			}
		})
	}
}
