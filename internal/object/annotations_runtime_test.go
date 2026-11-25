package object_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/dynamic"
)

const (
	annotationsAddress    = "resource.selection"
	annotationsBinding    = "resource.annotations"
	annotationsConstraint = annotationsAddress + ": schema: constraints["
	annotationsPresence   = "0] (at-least-one-of [annotations, template-annotations])"
	annotationCollection  = "/api/v1/namespaces/default/configmaps"
	annotationFixtures    = "testdata/ub/annotations"
)

func TestAnnotationsPlans(t *testing.T) {
	api := newAnnotationsRuntimeAPI(t, "")
	exec := annotationExecutor(t, "known", api)
	ctx := t.Context()
	top := map[string]any{"managed": "top"}
	template := map[string]any{"managed": "template"}
	exec.Inputs["top"], exec.Inputs["template"] = top, template
	plan := func(decision runtime.Decision) (*runtime.PlanFile, *runtime.PlanStep) {
		t.Helper()
		saved := savedAnnotationPlan(t, exec)
		step := annotationStep(t, saved)
		require.Equal(t, decision, step.Decision)
		return saved, step
	}
	apply := func(saved *runtime.PlanFile, calls []annotationCall) *runtime.ExecResult {
		t.Helper()
		result, err := exec.ApplyPlan(ctx, saved)
		require.NoError(t, err)
		require.Equal(t, calls, api.takeCalls())
		return result
	}
	getExample := annotationGet("example")
	applyExample := annotationApply("example", "team manager", "false", top, template)
	saved, _ := plan(runtime.DecisionCreate)
	require.Empty(t, api.takeCalls())
	result := apply(saved, []annotationCall{getExample, applyExample, getExample})
	require.Equal(t, map[string]any{"top": top, "template": template}, result.Outputs)
	snapshot, err := exec.Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find(annotationsAddress)
	require.NotNil(t, entry)
	require.Equal(t, map[string]any{
		"server": api.server, "api-version": "v1", "kind": "ConfigMap", "name": "example",
		"namespace": "default", "uid": "uid-example",
		"annotations": top, "template-annotations": template,
	}, entry.Outputs)

	saved, _ = plan(runtime.DecisionNoOp)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())
	apply(saved, nil)
	for _, tc := range []struct {
		input string
		value any
		rule  string
	}{
		{"api-version", "v1/extra/version", "1]"},
		{"kind", "", "2]"},
		{"name", "bad/name", "3]"},
		{"manager", "\u2003\u0085", "4]"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			before := exec.Inputs[tc.input]
			t.Cleanup(func() { exec.Inputs[tc.input] = before })
			exec.Inputs[tc.input] = tc.value
			_, err := exec.Plan(ctx)
			require.ErrorContains(t, err, annotationsConstraint+tc.rule)
			require.Equal(t, []annotationCall{getExample}, api.takeCalls())
		})
	}
	exec.Inputs["top"], exec.Inputs["template"] = nil, nil
	_, err = exec.Plan(ctx)
	require.ErrorContains(t, err, annotationsConstraint+annotationsPresence)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())
	exec.Inputs["top"], exec.Inputs["template"] = top, template

	exec.Inputs["configured-namespace"] = "another"
	api.setObject("example", "uid-example",
		map[string]any{"managed": "top", "incident": "external"}, template)
	plan(runtime.DecisionNoOp)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())

	for _, tc := range []struct {
		top, template map[string]any
	}{
		{map[string]any{"managed": "changed"}, template},
		{top, map[string]any{"managed": "changed"}},
	} {
		api.setObject("example", "uid-example", tc.top, tc.template)
		saved, step := plan(runtime.DecisionUpdate)
		require.True(t, step.Drift())
		require.Equal(t, map[string]any{
			"server": api.server, "api-version": "v1", "kind": "ConfigMap", "name": "example",
			"namespace": "default", "uid": "uid-example",
			"annotations": tc.top, "template-annotations": tc.template,
		}, step.ObservedOutputs)
		require.Equal(t, []annotationCall{getExample}, api.takeCalls())
		apply(saved, []annotationCall{getExample, applyExample, getExample})
		plan(runtime.DecisionNoOp)
		require.Equal(t, []annotationCall{getExample}, api.takeCalls())
	}

	refreshTop := map[string]any{"managed": "refreshed"}
	refreshTemplate := map[string]any{"managed": "refreshed template"}
	api.setObject("example", "uid-example", refreshTop, refreshTemplate)
	refresh, err := exec.Refresh(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, refresh.Refreshed)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())
	snapshot, err = exec.Store.Current()
	require.NoError(t, err)
	require.Equal(t, refreshTop, snapshot.Find(annotationsAddress).Outputs["annotations"])
	require.Equal(t, refreshTemplate,
		snapshot.Find(annotationsAddress).Outputs["template-annotations"])
	plan(runtime.DecisionNoOp)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())

	exec.Inputs["manager"], exec.Inputs["force"] = "new manager", true
	api.setAdmissionRewrite(true)
	saved, _ = plan(runtime.DecisionUpdate)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())
	newApply := annotationApply("example", "new manager", "true", top, template)
	result = apply(saved, []annotationCall{getExample, newApply, getExample})
	require.Equal(t, map[string]any{
		"top":      map[string]any{"managed": "admitted"},
		"template": map[string]any{"managed": "admitted template"},
	}, result.Outputs)
	api.setAdmissionRewrite(false)
	plan(runtime.DecisionNoOp)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())

	for _, tc := range []struct {
		input, value, reason string
	}{
		{"api-version", "/v1", "api-version"},
		{"kind", "Namespace", "kind"},
		{"namespace", "team", "metadata.namespace"},
		{"server", "http://127.0.0.1:1", "server"},
	} {
		t.Run("replacement/"+tc.input, func(t *testing.T) {
			previous := exec.Inputs[tc.input]
			t.Cleanup(func() { exec.Inputs[tc.input] = previous })
			exec.Inputs[tc.input] = tc.value
			step := annotationStep(t, savedAnnotationPlan(t, exec))
			require.Equal(t, runtime.DecisionReplace, step.Decision)
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
			require.Equal(t, []annotationCall{getExample}, api.takeCalls())
		})
	}

	exec.Inputs["name"] = "next"
	saved, replaced := plan(runtime.DecisionReplace)
	require.Equal(t, []string{"metadata.name"}, replaced.ReplacementReasons)
	require.Equal(t, new(api.server+"/uid-example"), replaced.ExpectedStableID)
	require.Equal(t, []annotationCall{getExample}, api.takeCalls())
	getNext := annotationGet("next")
	apply(saved, []annotationCall{
		getExample, getExample,
		annotationApply("example", "new manager", "true", map[string]any{}, map[string]any{}),
		getNext, annotationApply("next", "new manager", "true", top, template), getNext,
	})
	require.Equal(t, annotationObject("example", "uid-example", nil, nil), api.object("example"))
	require.Equal(t, annotationObject("next", "uid-next", top, template), api.object("next"))
}

func TestAnnotationsSavedNoOpAndHistoricalDestroy(t *testing.T) {
	api := newAnnotationsRuntimeAPI(t, "")
	exec := annotationExecutor(t, "known", api)
	ctx := t.Context()
	top := map[string]any{"managed": "first"}
	exec.Inputs["top"] = top
	get := annotationGet("example")
	_, err := exec.ApplyPlan(ctx, savedAnnotationPlan(t, exec))
	require.NoError(t, err)
	require.Equal(t, []annotationCall{
		get, annotationApply("example", "team manager", "false", top, nil), get,
	}, api.takeCalls())

	unchanged := savedAnnotationPlan(t, exec)
	require.Equal(t, runtime.DecisionNoOp, annotationStep(t, unchanged).Decision)
	require.Equal(t, []annotationCall{get}, api.takeCalls())
	lib := exec.Libraries["dynamic"]
	raw, err := os.ReadFile("testdata/annotations-tightened-constraints.json")
	require.NoError(t, err)
	var tighter []lang.ConstraintSpec
	require.NoError(t, json.Unmarshal(raw, &tighter))
	lib.Constraints[annotationsBinding] = append(lib.Constraints[annotationsBinding], tighter...)
	_, err = exec.ApplyPlan(ctx, unchanged)
	require.ErrorContains(t, err, annotationsConstraint+"5] (predicate)")
	require.Empty(t, api.takeCalls())
	_, err = exec.Plan(ctx)
	require.ErrorContains(t, err, annotationsConstraint+"5] (predicate)")
	require.Equal(t, []annotationCall{get}, api.takeCalls())

	exec.Destroy = true
	destroy := savedAnnotationPlan(t, exec)
	require.Equal(t, runtime.DecisionDestroy, annotationStep(t, destroy).Decision)
	require.Equal(t, []annotationCall{get}, api.takeCalls())
	_, err = exec.ApplyPlan(ctx, destroy)
	require.NoError(t, err)
	require.Equal(t, []annotationCall{
		get, get, annotationApply("example", "team manager", "false", map[string]any{}, nil),
	}, api.takeCalls())
	require.Equal(t, annotationObject("example", "uid-example", nil, nil), api.object("example"))
}

func TestAnnotationsDeferredPresenceProtectsReplacement(t *testing.T) {
	for _, uid := range []string{"null-seed", "valid-seed"} {
		t.Run(uid, func(t *testing.T) {
			api := newAnnotationsRuntimeAPI(t, uid)
			exec := annotationExecutor(t, "known", api)
			ctx := t.Context()
			exec.Inputs["manager"] = "Terraform"
			getExample := annotationGet("example")
			top := map[string]any{"managed": "first"}
			exec.Inputs["top"] = top
			_, err := exec.ApplyPlan(ctx, savedAnnotationPlan(t, exec))
			require.NoError(t, err)
			require.Equal(t, []annotationCall{
				getExample, annotationApply("example", "Terraform", "false", top, nil), getExample,
			}, api.takeCalls())

			setAnnotationFactory(t, exec, "pending")
			exec.Inputs = map[string]any{"server": api.server, "name": "next"}
			saved := savedAnnotationPlan(t, exec)
			step := annotationStep(t, saved)
			require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
			require.Equal(t, map[string][]string{
				"annotations": {"resource.seed.object.metadata.uid"},
			}, step.UnresolvedInputs)
			require.Equal(t, []annotationCall{getExample}, api.takeCalls())
			_, err = exec.ApplyPlan(ctx, saved)
			want := []annotationCall{
				{
					Method: "POST", Path: annotationCollection,
					Body: map[string]any{
						"apiVersion": "v1", "kind": "ConfigMap", "immutable": false,
						"metadata": map[string]any{
							"name": "seed", "namespace": "default", "creationTimestamp": nil,
						},
					},
				},
				annotationGet("seed"),
			}
			if uid == "valid-seed" {
				require.NoError(t, err)
				getNext := annotationGet("next")
				want = append(want, getExample, getExample,
					annotationApply("example", "Terraform", "false", map[string]any{}, nil),
					getNext, annotationApply("next", "Terraform", "false", nil, nil), getNext,
					annotationGet("seed"),
				)
				require.Equal(t, annotationObject("example", "uid-example", nil, nil),
					api.object("example"))
				snapshot, err := exec.Store.Current()
				require.NoError(t, err)
				require.Equal(t, "uid-next", snapshot.Find(annotationsAddress).Outputs["uid"])
				require.Equal(t, map[string]any{}, snapshot.Find(annotationsAddress).Outputs["annotations"])
			} else {
				require.ErrorContains(t, err, annotationsConstraint+annotationsPresence)
				require.Equal(t, annotationObject("example", "uid-example", top, nil),
					api.object("example"))
			}
			require.Equal(t, want, api.takeCalls())
		})
	}
}

func TestAnnotationsCompiledLifecycle(t *testing.T) {
	api := newAnnotationsRuntimeAPI(t, "")
	api.setAdmissionRewrite(true)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	source, err := filepath.Abs(filepath.Join(annotationFixtures, "known", "factory.ub"))
	require.NoError(t, err)
	dir := t.TempDir()
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: source, OutDir: dir, StackName: "annotations",
		Version: "v0.0.0", CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	config, err := os.ReadFile(filepath.Join(annotationFixtures, "known", "config.ub"))
	require.NoError(t, err)
	config = bytes.ReplaceAll(config, []byte("KUBE_SERVER"), []byte(api.server))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.ub"), config, 0o600))

	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = dir
		return cmd
	}
	run := func(args ...string) []byte {
		t.Helper()
		output, err := command(args...).CombinedOutput()
		require.NoError(t, err, "%v\n%s", args, output)
		return output
	}
	run("pin", "-c", "config.ub")
	run("plan", "-c", "config.ub", "-o", "saved.ubp")
	require.Empty(t, api.takeCalls())
	run("apply", "saved.ubp")
	get := annotationGet("example")
	require.Equal(t, []annotationCall{get,
		annotationApply("example", "team manager", "false",
			map[string]any{"managed": "top"}, map[string]any{"managed": "template"}), get,
	}, api.takeCalls())
	var snapshot state.Snapshot
	require.NoError(t, json.Unmarshal(run("state", "pull", "-c", "config.ub"), &snapshot))
	require.Equal(t, map[string]any{
		"top":      map[string]any{"managed": "admitted"},
		"template": map[string]any{"managed": "admitted template"},
	}, snapshot.Outputs)

	api.remove("example")
	run("plan", "-c", "config.ub", "-o", "reattach.ubp")
	require.Equal(t, []annotationCall{get}, api.takeCalls())
	body, err := os.ReadFile(filepath.Join(dir, "reattach.ubp"))
	require.NoError(t, err)
	saved, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	step := annotationStep(t, saved)
	require.Equal(t, runtime.DecisionCreate, step.Decision)
	require.Equal(t, "uid-example", step.PriorOutputs["uid"])
	output, err := command("apply", "reattach.ubp").CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "get existing Kubernetes ConfigMap example")
	require.Equal(t, []annotationCall{get}, api.takeCalls())

	const replacementUID = "uid-replacement"
	api.setObject("example", replacementUID, nil, nil)
	api.setAdmissionRewrite(false)
	run("apply", "reattach.ubp")
	top, template := map[string]any{"managed": "top"}, map[string]any{"managed": "template"}
	require.Equal(t, []annotationCall{
		get,
		{
			Method: http.MethodPatch, Path: get.Path, Manager: "team manager", Force: "false",
			Body: annotationObject("example", replacementUID, top, template),
		},
		get,
	}, api.takeCalls())
	require.NoError(t, json.Unmarshal(run("state", "pull", "-c", "config.ub"), &snapshot))
	entry := snapshot.Find(annotationsAddress)
	require.NotNil(t, entry)
	require.Equal(t, map[string]any{
		"server": api.server, "api-version": "v1", "kind": "ConfigMap", "name": "example",
		"namespace": "default", "uid": replacementUID,
		"annotations": top, "template-annotations": template,
	}, entry.Outputs)
	require.Equal(t, map[string]any{"top": top, "template": template}, snapshot.Outputs)
}

func annotationExecutor(
	t *testing.T, fixture string, api *annotationsRuntimeAPI,
) *runtime.Executor {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	lib := dynamic.Library()
	abs, err := filepath.Abs("../../dynamic")
	require.NoError(t, err)
	librarySchema, _, err := goschema.Read(abs)
	require.NoError(t, err)
	lib.Schema = librarySchema
	input := lib.Schema.Resources["annotations"]
	lib.Constraints = map[string][]lang.ConstraintSpec{annotationsBinding: input.Constraints}
	lib.Defaults = map[string][]lang.DefaultSpec{annotationsBinding: input.Defaults}
	libs := map[string]*runtime.Library{"dynamic": lib, "core": core.Library()}
	store, err := local.NewStore(dir, "annotations", "test", encrypters.Noop{})
	require.NoError(t, err)
	exec := &runtime.Executor{
		Libraries: libs, Store: store, Inputs: map[string]any{
			"server": api.server, "configured-namespace": "other", "api-version": "v1",
			"kind": "ConfigMap", "name": "example", "namespace": nil, "manager": "team manager",
			"force": false, "top": nil, "template": nil,
		},
	}
	setAnnotationFactory(t, exec, fixture)
	return exec
}

func setAnnotationFactory(t *testing.T, exec *runtime.Executor, fixture string) {
	t.Helper()
	path := filepath.Join(annotationFixtures, fixture, "factory.ub")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	syntax, _, err := compile.ParseFactorySyntaxSource(path, source)
	require.NoError(t, err)
	exec.SyntaxSource = &syntax.Factory.Body
	exec.DAG = runtime.BuildSyntaxDAG(syntax.Factory.Body, exec.Libraries)
}

func savedAnnotationPlan(t *testing.T, exec *runtime.Executor) *runtime.PlanFile {
	t.Helper()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return saved
}

func annotationStep(t *testing.T, plan *runtime.PlanFile) *runtime.PlanStep {
	t.Helper()
	for i := range plan.Steps {
		if plan.Steps[i].Address == annotationsAddress {
			return &plan.Steps[i]
		}
	}
	t.Fatalf("missing %s in plan", annotationsAddress)
	return nil
}

type annotationCall struct {
	Method, Path   string
	Manager, Force string
	Body           map[string]any
}

type annotationsRuntimeAPI struct {
	server  string
	mu      sync.Mutex
	objects map[string]map[string]any
	calls   []annotationCall
	rewrite bool
}

func newAnnotationsRuntimeAPI(t *testing.T, seedUID string) *annotationsRuntimeAPI {
	t.Helper()
	a := &annotationsRuntimeAPI{objects: map[string]map[string]any{
		"example": annotationObject("example", "uid-example", nil, nil),
		"next":    annotationObject("next", "uid-next", nil, nil),
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var payload any
		switch req.URL.Path {
		case "/api":
			payload = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			payload = metav1.APIGroupList{}
		case "/api/v1":
			payload = metav1.APIResourceList{
				GroupVersion: "v1", APIResources: []metav1.APIResource{{
					Name: "configmaps", Kind: "ConfigMap", Namespaced: true,
				}},
			}
		default:
			call := annotationCall{
				Method: req.Method, Path: req.URL.Path,
				Manager: req.URL.Query().Get("fieldManager"), Force: req.URL.Query().Get("force"),
			}
			if req.Method == http.MethodPatch || req.Method == http.MethodPost {
				if err := json.NewDecoder(req.Body).Decode(&call.Body); err != nil {
					t.Errorf("decode Kubernetes request: %v", err)
				}
			}
			a.calls = append(a.calls, call)
			name := filepath.Base(req.URL.Path)
			switch req.Method {
			case http.MethodPost:
				name = "seed"
				a.objects[name] = annotationObject(name, seedUID, nil, nil)
			case http.MethodPatch:
				var top, template map[string]any
				metadata, _ := call.Body["metadata"].(map[string]any)
				top, _ = metadata["annotations"].(map[string]any)
				if spec, ok := call.Body["spec"].(map[string]any); ok {
					workload := spec["template"].(map[string]any)
					template, _ = workload["metadata"].(map[string]any)["annotations"].(map[string]any)
				}
				if len(top) == 0 {
					top = nil
				}
				if len(template) == 0 {
					template = nil
				}
				if a.rewrite {
					top = map[string]any{"managed": "admitted"}
					template = map[string]any{"managed": "admitted template"}
				}
				uid := fmt.Sprint(metadata["uid"])
				a.objects[name] = annotationObject(name, uid, top, template)
			}
			if obj, exists := a.objects[name]; exists {
				payload = obj
			} else {
				w.WriteHeader(http.StatusNotFound)
				status := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
				payload = status.ErrStatus
			}
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode Kubernetes response: %v", err)
		}
	}))
	a.server = server.URL
	t.Cleanup(server.Close)
	return a
}

func (a *annotationsRuntimeAPI) takeCalls() []annotationCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func (a *annotationsRuntimeAPI) setObject(name, uid string, top, template map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objects[name] = annotationObject(name, uid, top, template)
}

func (a *annotationsRuntimeAPI) object(name string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.objects[name]
}

func (a *annotationsRuntimeAPI) setAdmissionRewrite(rewrite bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rewrite = rewrite
}

func (a *annotationsRuntimeAPI) remove(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.objects, name)
}

func annotationObject(name, uid string, top, template map[string]any) map[string]any {
	metadata := map[string]any{"name": name, "namespace": "default", "uid": uid}
	body := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata}
	if top != nil {
		metadata["annotations"] = top
	}
	if template != nil {
		body["spec"] = map[string]any{"template": map[string]any{
			"metadata": map[string]any{"annotations": template},
		}}
	}
	return body
}

func annotationGet(name string) annotationCall {
	return annotationCall{Method: "GET", Path: annotationCollection + "/" + name}
}

func annotationApply(name, manager, force string, top, template map[string]any) annotationCall {
	return annotationCall{
		Method: "PATCH", Path: annotationCollection + "/" + name,
		Manager: manager, Force: force, Body: annotationObject(name, "uid-"+name, top, template),
	}
}
