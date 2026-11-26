package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/admissionregistration/v1"
)

const bindingFixtures = "testdata/ub/binding"

func bindingLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	rules := schema.Resources["validating-admission-policy-binding"].Constraints
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.validating-admission-policy-binding": rules,
	}
	return lib
}

func bindingSource(t *testing.T, name string) syntax.FactoryBody {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bindingFixtures, name, "factory.ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", data)
	require.NoError(t, err)
	return source.Factory.Body
}

func TestBindingCompiledInputs(t *testing.T) {
	lib := bindingLibrary(t)
	paths, err := filepath.Glob(filepath.Join(bindingFixtures, "*", "factory.ub"))
	require.NoError(t, err)
	for _, path := range paths {
		name := filepath.Base(filepath.Dir(path))
		if name == "runtime" || name == "deferred" || name == "partial" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			checker := check.NewSyntax(bindingSource(t, name), map[string]*runtime.Library{"admission": lib},
				nil, "")
			refs := checker.References(nil).Errors()
			rules := checker.LiteralConstraints().Errors()
			switch {
			case strings.HasPrefix(name, "valid-"):
				require.Empty(t, refs)
				require.Empty(t, rules)
			case strings.HasPrefix(name, "schema-"):
				require.NotEmpty(t, refs)
			default:
				require.Empty(t, refs)
				require.NotEmpty(t, rules)
				require.Contains(t, checker.LiteralConstraints().Error(), "resource.subject")
				require.Contains(t, checker.LiteralConstraints().Error(), "constraints[")
			}
		})
	}
	require.ErrorContains(t, libraryapi.Check(lib.Compatibility.RequiredAPI, libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}), "newer than implemented API 1.0")
}

type bindingRuntimeAPI struct {
	sync.Mutex
	objects map[string]*admissionv1.ValidatingAdmissionPolicyBinding
	calls   []string
	server  string
}

func newBindingRuntimeAPI(t *testing.T) *bindingRuntimeAPI {
	t.Helper()
	a := &bindingRuntimeAPI{objects: map[string]*admissionv1.ValidatingAdmissionPolicyBinding{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.Lock()
		defer a.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch r.URL.Path {
		case "/api":
			response = metav1.APIVersions{Versions: []string{"v1"}}
		case "/api/v1":
			response = metav1.APIResourceList{GroupVersion: "v1"}
		case "/apis":
			response = metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "admissionregistration.k8s.io",
				Versions: []metav1.GroupVersionForDiscovery{{
					GroupVersion: "admissionregistration.k8s.io/v1", Version: "v1",
				}},
			}}}
		case "/apis/admissionregistration.k8s.io/v1":
			response = metav1.APIResourceList{GroupVersion: "admissionregistration.k8s.io/v1",
				APIResources: []metav1.APIResource{{
					Name: "validatingadmissionpolicybindings", Kind: "ValidatingAdmissionPolicyBinding",
					Verbs: metav1.Verbs{"get", "list", "create", "update", "delete"},
				}},
			}
		}
		if response != nil {
			require.NoError(t, json.NewEncoder(w).Encode(response))
			return
		}
		require.True(t, strings.HasPrefix(r.URL.Path,
			"/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings"), r.URL.Path)
		name := filepath.Base(r.URL.Path)
		switch r.Method {
		case http.MethodPost, http.MethodPut:
			var body admissionv1.ValidatingAdmissionPolicyBinding
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Empty(t, body.Namespace)
			if body.Name == "" {
				body.Name = body.GenerateName + "actual"
			}
			if body.UID == "" {
				body.UID = types.UID("uid-" + body.Name)
			}
			body.APIVersion = "admissionregistration.k8s.io/v1"
			body.Kind = "ValidatingAdmissionPolicyBinding"
			if m := body.Spec.MatchResources; m != nil {
				if m.MatchPolicy == nil {
					m.MatchPolicy = new(admissionv1.Equivalent)
				}
				if m.NamespaceSelector == nil {
					m.NamespaceSelector = &metav1.LabelSelector{}
				}
				if m.ObjectSelector == nil {
					m.ObjectSelector = &metav1.LabelSelector{}
				}
				for i := range m.ResourceRules {
					if m.ResourceRules[i].Scope == nil {
						m.ResourceRules[i].Scope = new(admissionv1.AllScopes)
					}
				}
			}
			a.objects[body.Name], name, response = &body, body.Name, &body
		case http.MethodGet:
			response = a.objects[name]
			if a.objects[name] == nil {
				w.WriteHeader(404)
				response = metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound}
			}
		case http.MethodDelete:
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(r.Body).Decode(&options))
			require.Equal(t, new(a.objects[name].UID), options.Preconditions.UID)
			delete(a.objects, name)
			response = metav1.Status{Status: metav1.StatusSuccess}
		}
		a.calls = append(a.calls, r.Method+" "+name)
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)
	a.server = server.URL
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	return a
}

func (a *bindingRuntimeAPI) drain() []string {
	a.Lock()
	defer a.Unlock()
	calls := slices.Clone(a.calls)
	a.calls = nil
	return calls
}

func bindingInputs(a *bindingRuntimeAPI) map[string]any {
	return map[string]any{
		"server": a.server, "token": "current", "namespace": "unused",
		"metadata": map[string]any{"generate-name": "binding-"},
		"spec":     map[string]any{"policy-name": "policy", "validation-actions": []any{"Warn", "Audit"}},
	}
}

func bindingExecutor(t *testing.T, lib *runtime.Library, fixture string,
	inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body := bindingSource(t, fixture)
	libs := map[string]*runtime.Library{"admission": lib}
	store, err := local.NewStore(t.TempDir(), "binding", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body, Libraries: libs,
		Store: store, Inputs: inputs, Parallelism: 1,
		Factory: state.FactoryInfo{Name: "binding", Version: "v0", ContentRevision: "c0"},
	}
}

func bindingPlan(t *testing.T, e *runtime.Executor, want runtime.Decision) *runtime.Plan {
	t.Helper()
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			require.Equal(t, want, step.Decision)
			return plan
		}
	}
	t.Fatal("missing resource.subject")
	return nil
}

func bindingSaved(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return saved
}

func bindingApply(t *testing.T, e *runtime.Executor, plan *runtime.Plan) {
	t.Helper()
	_, err := e.ApplyPlan(t.Context(), bindingSaved(t, plan))
	require.NoError(t, err)
}

func TestBindingPlansAndDrift(t *testing.T) {
	lib := bindingLibrary(t)
	api := newBindingRuntimeAPI(t)
	inputs := bindingInputs(api)
	e := bindingExecutor(t, lib, "runtime", inputs)
	bindingApply(t, e, bindingPlan(t, e, runtime.DecisionCreate))
	require.Equal(t, []string{"POST binding-actual"}, api.drain())
	bindingApply(t, e, bindingPlan(t, e, runtime.DecisionNoOp))
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	spec := inputs["spec"].(map[string]any)
	spec["validation-actions"] = []any{"Audit", "Warn"}
	spec["match-resources"] = map[string]any{
		"match-policy": "Equivalent", "namespace-selector": map[string]any{},
	}
	bindingPlan(t, e, runtime.DecisionNoOp)
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	api.Lock()
	live := api.objects["binding-actual"]
	live.ResourceVersion, live.Generation = "99", 55
	live.CreationTimestamp = metav1.Now()
	live.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller"}}
	api.Unlock()
	bindingPlan(t, e, runtime.DecisionNoOp)
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	for _, edit := range []struct {
		name string
		run  func(*admissionv1.ValidatingAdmissionPolicyBinding)
	}{
		{"policy", func(b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.PolicyName = "external"
		}},
		{"actions", func(b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.ValidationActions = []admissionv1.ValidationAction{admissionv1.Deny}
		}},
		{"param", func(b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.ParamRef = &admissionv1.ParamRef{
				Name: "external", ParameterNotFoundAction: new(admissionv1.AllowAction),
			}
		}},
		{"rules", func(b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Spec.MatchResources = &admissionv1.MatchResources{MatchPolicy: new(admissionv1.Exact)}
		}},
		{"maps", func(b *admissionv1.ValidatingAdmissionPolicyBinding) {
			b.Labels = map[string]string{"external": "label"}
			b.Annotations = map[string]string{"external": "annotation"}
		}},
	} {
		t.Run(edit.name, func(t *testing.T) {
			api.Lock()
			before := api.objects["binding-actual"].DeepCopy()
			edit.run(api.objects["binding-actual"])
			api.Unlock()
			bindingApply(t, e, bindingPlan(t, e, runtime.DecisionUpdate))
			require.Equal(t, []string{"GET binding-actual", "GET binding-actual", "PUT binding-actual"},
				api.drain())
			api.Lock()
			require.Equal(t, before.UID, api.objects["binding-actual"].UID)
			api.Unlock()
			bindingPlan(t, e, runtime.DecisionNoOp)
			require.Equal(t, []string{"GET binding-actual"}, api.drain())
		})
	}
	inputs["namespace"], inputs["token"] = "new-namespace", "new-token"
	bindingPlan(t, e, runtime.DecisionNoOp)
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	inputs["metadata"] = map[string]any{"name": "renamed"}
	bindingPlan(t, e, runtime.DecisionReplace)
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	spec["validation-actions"] = []any{"Deny", "Warn"}
	_, err := e.Plan(t.Context())
	require.ErrorContains(t, err, "spec.validation-actions must not combine Deny and Warn")
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
}

func TestBindingSavedNoOpAndHistoricalDestroy(t *testing.T) {
	lib := bindingLibrary(t)
	api := newBindingRuntimeAPI(t)
	inputs := bindingInputs(api)
	inputs["spec"].(map[string]any)["validation-actions"] = []any{"Deny", "Warn"}
	rules := lib.Constraints
	lib.Constraints = nil
	e := bindingExecutor(t, lib, "runtime", inputs)
	bindingApply(t, e, bindingPlan(t, e, runtime.DecisionCreate))
	require.Equal(t, []string{"POST binding-actual"}, api.drain())
	noop := bindingSaved(t, bindingPlan(t, e, runtime.DecisionNoOp))
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	lib.Constraints = rules
	_, err := e.Plan(t.Context())
	requireBindingRule(t, err, "must not combine Deny and Warn")
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	_, err = e.ApplyPlan(t.Context(), noop)
	requireBindingRule(t, err, "must not combine Deny and Warn")
	require.Empty(t, api.drain())
	e.Destroy = true
	bindingApply(t, e, bindingPlan(t, e, runtime.DecisionDestroy))
	require.Equal(t, []string{
		"GET binding-actual", "GET binding-actual", "DELETE binding-actual", "GET binding-actual",
	}, api.drain())
}

func requireBindingRule(t *testing.T, err error, message string) {
	t.Helper()
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints[")
	require.ErrorContains(t, err, message)
}

type bindingSeed struct {
	Metadata svc.PolicyMetadata
	Spec     svc.BindingSpec
}

type bindingSeedOutput struct{ Result bindingSeed }

func (s *bindingSeed) Run(context.Context, any) (*bindingSeedOutput, error) {
	return &bindingSeedOutput{Result: *s}, nil
}

func TestBindingDeferredConstraints(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "invalid"}[invalid], func(t *testing.T) {
			lib := bindingLibrary(t)
			lib.Actions = map[string]runtime.ActionRegistration{
				"seed": runtime.MakeAction[bindingSeed, *bindingSeedOutput, any](),
			}
			api := newBindingRuntimeAPI(t)
			inputs := bindingInputs(api)
			if invalid {
				inputs["spec"].(map[string]any)["policy-name"] = ""
			}
			e := bindingExecutor(t, lib, "deferred", inputs)
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.drain())
			_, err = e.ApplyPlan(t.Context(), bindingSaved(t, plan))
			if invalid {
				requireBindingRule(t, err, "spec.policy-name must not be empty")
				require.Empty(t, api.drain())
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"POST binding-actual"}, api.drain())
			}
		})
	}
}

func TestBindingEvaluatedInputs(t *testing.T) {
	lib := bindingLibrary(t)
	api := newBindingRuntimeAPI(t)
	paths, err := filepath.Glob(filepath.Join(bindingFixtures, "*", "factory.ub"))
	require.NoError(t, err)
	for _, path := range paths {
		name := filepath.Base(filepath.Dir(path))
		if name == "runtime" || name == "deferred" || name == "partial" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			body := bindingSource(t, name)
			dag := runtime.BuildSyntaxDAG(body, map[string]*runtime.Library{"admission": lib})
			var desired map[string]any
			for _, node := range dag.Nodes {
				if node.Address == "resource.subject" {
					desired, err = runtime.LiteralConstraintInputs(node.Body, nil)
					require.NoError(t, err)
				}
			}
			require.NotNil(t, desired)
			inputs := bindingInputs(api)
			inputs["metadata"], inputs["spec"] = desired["metadata"], desired["spec"]
			e := bindingExecutor(t, lib, "runtime", inputs)
			_, err = e.Plan(t.Context())
			if strings.HasPrefix(name, "valid-") {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, "schema:")
				if strings.HasPrefix(name, "invalid-") {
					require.ErrorContains(t, err, "constraints[")
				}
			}
			require.Empty(t, api.drain())
		})
	}
}

func TestBindingInvalidSavedReplacement(t *testing.T) {
	lib := bindingLibrary(t)
	api := newBindingRuntimeAPI(t)
	inputs := bindingInputs(api)
	e := bindingExecutor(t, lib, "runtime", inputs)
	bindingApply(t, e, bindingPlan(t, e, runtime.DecisionCreate))
	api.drain()
	inputs["metadata"] = map[string]any{"name": "replacement"}
	inputs["spec"].(map[string]any)["policy-name"] = ""
	rules := lib.Constraints
	lib.Constraints = nil
	replacement := bindingSaved(t, bindingPlan(t, e, runtime.DecisionReplace))
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	lib.Constraints = rules
	_, err := e.ApplyPlan(t.Context(), replacement)
	requireBindingRule(t, err, "spec.policy-name must not be empty")
	require.Empty(t, api.drain())
}

func TestBindingDefaultedPlans(t *testing.T) {
	lib := bindingLibrary(t)
	api := newBindingRuntimeAPI(t)
	inputs := bindingInputs(api)
	rule := map[string]any{
		"api-groups": []any{""}, "api-versions": []any{"v1"},
		"operations": []any{"CREATE"}, "resources": []any{"configmaps"},
	}
	match := map[string]any{"resource-rules": []any{rule}}
	param := map[string]any{"name": "parameter", "parameter-not-found-action": "Allow"}
	spec := inputs["spec"].(map[string]any)
	spec["match-resources"], spec["param-ref"] = match, param
	e := bindingExecutor(t, lib, "runtime", inputs)
	bindingApply(t, e, bindingPlan(t, e, runtime.DecisionCreate))
	require.Equal(t, []string{"POST binding-actual"}, api.drain())
	bindingPlan(t, e, runtime.DecisionNoOp)
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
	match["match-policy"], match["object-selector"] = "Equivalent", map[string]any{}
	rule["scope"] = "*"
	param["namespace"] = ""
	inputs["metadata"].(map[string]any)["labels"] = map[string]any{}
	inputs["metadata"].(map[string]any)["annotations"] = map[string]any{}
	bindingPlan(t, e, runtime.DecisionNoOp)
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
}

func TestBindingIdentityDefinition(t *testing.T) {
	reg := Library().Resources["validating-admission-policy-binding"]
	id, err := reg.StableID(nil, map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
	_, err = reg.StableID(nil, map[string]any{})
	require.Error(t, err)
	before := map[string]any{"metadata": map[string]any{"name": "binding"}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, before))
	method := reflect.ValueOf(reg).MethodByName("ReplacementReasons")
	prior := reflect.New(method.Type().In(2)).Elem()
	prior.FieldByName("Inputs").Set(reflect.ValueOf(before))
	prior.FieldByName("Configuration").Set(reflect.ValueOf(map[string]any{}))
	for _, field := range []string{"server", "kubeconfig", "current-context", "namespace", "token"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
		result := method.Call([]reflect.Value{
			reflect.ValueOf(receiver), reflect.ValueOf(current), prior,
		})
		require.True(t, result[1].IsNil(), "%v", result[1])
		reasons := result[0].Interface().([]string)
		if field == "namespace" || field == "token" {
			require.Empty(t, reasons)
		} else {
			require.Equal(t, []string{field}, reasons)
		}
	}
}

func TestBindingPartialInputs(t *testing.T) {
	lib := bindingLibrary(t)
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[bindingSeed, *bindingSeedOutput, any](),
	}
	api := newBindingRuntimeAPI(t)
	inputs := bindingInputs(api)
	inputs["actions"] = []any{"Warn"}
	e := bindingExecutor(t, lib, "partial", inputs)
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, api.drain())
	bindingApply(t, e, plan)
	require.Equal(t, []string{"POST binding-actual"}, api.drain())
	inputs["actions"] = []any{"Deny", "Warn"}
	_, err = e.Plan(t.Context())
	requireBindingRule(t, err, "must not combine Deny and Warn")
	require.Equal(t, []string{"GET binding-actual"}, api.drain())
}
