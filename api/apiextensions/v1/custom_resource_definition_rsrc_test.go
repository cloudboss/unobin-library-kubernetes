package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func crdLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	module, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.custom-resource-definition": schema.Resources["custom-resource-definition"].Constraints,
	}
	return lib
}

func crdFixture(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub", fixture+".ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func crdExecutor(
	t *testing.T, lib *runtime.Library, fixture string, inputs map[string]any, store *local.Store,
) *runtime.Executor {
	t.Helper()
	body := crdFixture(t, fixture)
	if store == nil {
		var err error
		store, err = local.NewStore(t.TempDir(), "crd", "test", encrypters.Noop{})
		require.NoError(t, err)
	}
	libs := map[string]*runtime.Library{"crd": lib}
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Store: store, Inputs: inputs, Parallelism: 1,
		Factory: state.FactoryInfo{Name: "crd", Version: "v0"},
	}
}

func crdSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func crdInput() map[string]any {
	return map[string]any{
		"create-timeout": nil,
		"metadata":       map[string]any{"name": "widgets.example.test"},
		"spec": map[string]any{
			"group": "example.test", "names": map[string]any{"plural": "widgets", "kind": "Widget"},
			"scope":    "Namespaced",
			"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}},
		},
	}
}

func TestCRDPublicLiteralCompiler(t *testing.T) {
	lib := crdLibrary(t)
	for _, tc := range []struct{ fixture, diagnostic string }{
		{"literal-valid", ""}, {"literal-invalid", "spec.scope must be Cluster or Namespaced"},
		{"literal-storage", "exactly one storage"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			body := crdFixture(t, tc.fixture)
			checker := check.NewSyntax(body, map[string]*runtime.Library{"crd": lib}, nil, "")
			require.NoError(t, checker.References(nil).Err())
			err := checker.LiteralConstraints().Err()
			if tc.diagnostic != "" {
				require.ErrorContains(t, err, tc.diagnostic)
				return
			}
			require.NoError(t, err)
			executor := crdExecutor(t, lib, tc.fixture, nil, nil)
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionCreate, crdStep(t, plan).Decision)
		})
	}
}

func TestCRDPublicEvaluatedConstraints(t *testing.T) {
	lib := crdLibrary(t)
	for _, tc := range []struct {
		name       string
		change     func(map[string]any, map[string]any)
		diagnostic string
	}{
		{"derived name", func(m, _ map[string]any) { m["name"] = "wrong.example.test" },
			"metadata.name must equal"},
		{"namespace", func(m, _ map[string]any) { m["namespace"] = "default" },
			"metadata.namespace is forbidden"},
		{"scope", func(_, s map[string]any) { s["scope"] = "Wrong" }, "spec.scope must be"},
		{"no versions", func(_, s map[string]any) { s["versions"] = []any{} },
			"spec.versions must not be empty"},
		{"duplicate versions", func(_, s map[string]any) {
			s["versions"] = []any{
				map[string]any{"name": "v1", "served": true, "storage": true},
				map[string]any{"name": "v1", "served": false, "storage": false},
			}
		}, "spec.versions names must be unique"},
		{"storage", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["storage"] = false
		}, "exactly one storage"},
		{"version syntax", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["name"] = "Bad_V1"
		}, "invalid spec.versions name"},
		{"missing version name", func(_, s map[string]any) {
			delete(s["versions"].([]any)[0].(map[string]any), "name")
		}, "invalid spec.versions name"},
		{"missing served", func(_, s map[string]any) {
			delete(s["versions"].([]any)[0].(map[string]any), "served")
		}, "versions require served and storage"},
		{"missing storage", func(_, s map[string]any) {
			s["versions"] = append(s["versions"].([]any), map[string]any{"name": "v2", "served": false})
		}, "versions require served and storage"},
		{"missing scale path", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["subresources"] = map[string]any{
				"scale": map[string]any{"status-replicas-path": ".status.replicas"},
			}
		}, "scale requires spec-replicas-path and status-replicas-path"},
		{"missing printer path", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["additional-printer-columns"] = []any{
				map[string]any{"name": "Message", "type": "string"},
			}
		}, "printer columns require name, type and json-path"},
		{"missing selectable path", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["selectable-fields"] = []any{map[string]any{}}
		}, "selectable fields require json-path"},
		{"missing review versions", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "Webhook", "webhook": map[string]any{
				"client-config": map[string]any{"url": "https://example.test"},
			}}
		}, "conversion-review-versions must not be empty"},
		{"conversion enum", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "Wrong"}
		}, "conversion.strategy must be"},
		{"duplicate webhook target", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "Webhook", "webhook": map[string]any{
				"conversion-review-versions": []any{"v1"}, "client-config": map[string]any{
					"url":     "https://example.test",
					"service": map[string]any{"namespace": "system", "name": "convert"},
				},
			}}
		}, "exactly one URL or service"},
		{"too many selectable fields", func(_, s map[string]any) {
			fields := make([]any, 9)
			for i := range fields {
				fields[i] = map[string]any{"json-path": ".spec.field"}
			}
			s["versions"].([]any)[0].(map[string]any)["selectable-fields"] = fields
		}, "at most eight fields"},
		{"printer priority", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["additional-printer-columns"] = []any{
				map[string]any{"name": "Message", "type": "string",
					"json-path": ".spec.message", "priority": int64(-1)},
			}
		}, "printer column priority"},
		{"deprecation warning", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["deprecation-warning"] = "use v2"
		}, "deprecation-warning requires deprecated"},
		{"status fields", func(_, s map[string]any) {
			s["versions"].([]any)[0].(map[string]any)["subresources"] = map[string]any{
				"status": map[string]any{"unsupported": true},
			}
		}, "subresources.status must be empty"},
		{"webhook missing", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "Webhook"}
		}, "Webhook conversion requires"},
		{"none webhook", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "None", "webhook": map[string]any{}}
		}, "None conversion forbids"},
		{"webhook target", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "Webhook", "webhook": map[string]any{
				"conversion-review-versions": []any{"v1"}, "client-config": map[string]any{},
			}}
		}, "exactly one URL or service"},
		{"webhook port", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "Webhook", "webhook": map[string]any{
				"conversion-review-versions": []any{"v1"}, "client-config": map[string]any{
					"service": map[string]any{"namespace": "ns", "name": "convert", "port": int64(65536)},
				},
			}}
		}, "port must be"},
		{"ca", func(_, s map[string]any) {
			s["conversion"] = map[string]any{"strategy": "Webhook", "webhook": map[string]any{
				"conversion-review-versions": []any{"v1"}, "client-config": map[string]any{
					"url": "https://example.test", "ca-bundle": "bad",
				},
			}}
		}, "ca-bundle must be base64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := crdInput()
			inputs["server"] = "http://127.0.0.1:1"
			tc.change(inputs["metadata"].(map[string]any), inputs["spec"].(map[string]any))
			executor := crdExecutor(t, lib, "evaluated", inputs, nil)
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.subject: schema: constraints[")
			require.ErrorContains(t, err, tc.diagnostic)
		})
	}
}

type crdRuntimeAPI struct {
	mu      sync.Mutex
	objects map[string]*api.CustomResourceDefinition
	calls   []string
}

func (a *crdRuntimeAPI) serve(w http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if req.Method == http.MethodGet {
		var discovery any
		switch req.URL.Path {
		case "/api":
			discovery = &metav1.APIVersions{Versions: []string{"v1"}}
		case "/api/v1":
			discovery = &metav1.APIResourceList{GroupVersion: "v1"}
		case "/apis":
			version := metav1.GroupVersionForDiscovery{
				GroupVersion: "apiextensions.k8s.io/v1", Version: "v1",
			}
			discovery = &metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name:     "apiextensions.k8s.io",
				Versions: []metav1.GroupVersionForDiscovery{version}, PreferredVersion: version,
			}}}
		case "/apis/apiextensions.k8s.io/v1":
			discovery = &metav1.APIResourceList{
				GroupVersion: "apiextensions.k8s.io/v1",
				APIResources: []metav1.APIResource{{
					Name: "customresourcedefinitions", Kind: "CustomResourceDefinition",
				}},
			}
		}
		if discovery != nil {
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
	}
	name := filepath.Base(req.URL.Path)
	a.calls = append(a.calls, req.Method+" "+name)
	var out any
	switch req.Method {
	case http.MethodPost, http.MethodPut:
		var body api.CustomResourceDefinition
		_ = json.NewDecoder(req.Body).Decode(&body)
		body.UID = types.UID(body.Name + "-uid")
		body.ResourceVersion = "1"
		a.objects[body.Name] = &body
		out = &body
	case http.MethodGet:
		if obj := a.objects[name]; obj != nil {
			out = obj
		} else {
			w.WriteHeader(404)
			out = metav1.Status{Status: "Failure", Code: 404, Reason: metav1.StatusReasonNotFound}
		}
	case http.MethodDelete:
		delete(a.objects, name)
		out = metav1.Status{Status: "Success"}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *crdRuntimeAPI) drain() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.calls
	a.calls = nil
	return out
}

func runtimeCRDServer(t *testing.T) (*crdRuntimeAPI, string) {
	t.Helper()
	a := &crdRuntimeAPI{objects: map[string]*api.CustomResourceDefinition{}}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(server.Close)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	return a, server.URL
}

func TestCRDSavedNoOpAndDrift(t *testing.T) {
	lib := crdLibrary(t)
	a, url := runtimeCRDServer(t)
	inputs := crdInput()
	inputs["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"] = map[string]any{
		"type": "object", "properties": map[string]any{
			"value": map[string]any{"type": "integer", "default": int64(9007199254740993)},
		},
	}
	inputs["server"] = url
	executor := crdExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST customresourcedefinitions"}, a.drain())
	a.mu.Lock()
	require.Equal(t, []byte("9007199254740993"),
		a.objects["widgets.example.test"].Spec.Versions[0].Schema.OpenAPIV3Schema.
			Properties["value"].Default.Raw)
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, crdStep(t, plan).Decision)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"GET widgets.example.test"}, a.drain())
	a.mu.Lock()
	live := a.objects["widgets.example.test"]
	api.SetObjectDefaults_CustomResourceDefinition(live)
	live.Generation, live.ResourceVersion = 7, "8"
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, crdStep(t, plan).Decision)
	a.mu.Lock()
	live.Labels = map[string]string{"external": "changed"}
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, crdStep(t, plan).Decision)
	require.Equal(t, map[string]any{"external": "changed"},
		crdStep(t, plan).ObservedOutputs["metadata"].(map[string]any)["labels"])
	a.mu.Lock()
	live.Labels = nil
	live.Spec.Versions = append(live.Spec.Versions, api.CustomResourceDefinitionVersion{
		Name: "v2", Served: false,
	})
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, crdStep(t, plan).Decision)
	a.mu.Lock()
	live.Spec.Versions = live.Spec.Versions[:1]
	live.Spec.Versions[0].Schema = &api.CustomResourceValidation{
		OpenAPIV3Schema: &api.JSONSchemaProps{Type: "object"},
	}
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, crdStep(t, plan).Decision)
}

func TestCRDInvalidSavedNoOp(t *testing.T) {
	lib := crdLibrary(t)
	a, url := runtimeCRDServer(t)
	inputs := crdInput()
	inputs["server"] = url
	inputs["spec"].(map[string]any)["scope"] = "Wrong"
	rules := lib.Constraints
	lib.Constraints = nil
	executor := crdExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, crdStep(t, plan).Decision)
	a.drain()
	lib.Constraints = rules
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.ErrorContains(t, err, "spec.scope")
	require.Equal(t, []string{"GET widgets.example.test"}, a.drain())
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.Empty(t, a.drain())
}

func TestCRDDeferredBeforeReplacementDeletion(t *testing.T) {
	lib := crdLibrary(t)
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "valid"}[valid], func(t *testing.T) {
			a, url := runtimeCRDServer(t)
			inputs := crdInput()
			inputs["server"] = url
			inputs["spec"].(map[string]any)["scope"] = "Cluster"
			initial := crdExecutor(t, lib, "evaluated", inputs, nil)
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
			require.NoError(t, err)
			a.drain()
			executor := crdExecutor(t, lib, "deferred",
				map[string]any{"server": url, "valid": valid}, initial.Store.(*local.Store))
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			a.drain()
			_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
			if !valid {
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
				require.ErrorContains(t, err, "spec.scope")
				require.Equal(t, []string{"POST customresourcedefinitions"}, a.drain())
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{
					"POST customresourcedefinitions", "GET widgets.example.test",
					"GET widgets.example.test", "DELETE widgets.example.test", "GET widgets.example.test",
					"POST customresourcedefinitions", "GET seeds.example.test",
				}, a.drain())
			}
		})
	}
}

func crdStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			return step
		}
	}
	t.Fatal("missing resource.subject")
	return nil
}

func TestCRDHistoricalInvalidSchemaDestroy(t *testing.T) {
	lib := crdLibrary(t)
	a, url := runtimeCRDServer(t)
	inputs := crdInput()
	inputs["server"] = url
	executor := crdExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	a.drain()
	store := executor.Store.(*local.Store)
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.subject")
	require.NotNil(t, entry)
	spec := entry.Inputs["spec"].(map[string]any)
	spec["scope"] = "historical-invalid"
	spec["versions"].([]any)[0].(map[string]any)["schema"] = map[string]any{
		"invalidSchema": true,
	}
	rev, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(rev))
	executor.Inputs["server"] = "http://127.0.0.1:1"
	executor.Destroy = true
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionDestroy, crdStep(t, plan).Decision)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET widgets.example.test", "GET widgets.example.test",
		"GET widgets.example.test", "DELETE widgets.example.test", "GET widgets.example.test",
	}, a.drain())
}

func TestCRDLiteralNamespaceIsAbsentFromSchema(t *testing.T) {
	lib := crdLibrary(t)
	body := crdFixture(t, "literal-namespace")
	checker := check.NewSyntax(body, map[string]*runtime.Library{"crd": lib}, nil, "")
	require.ErrorContains(t, checker.References(nil).Err(), "namespace")
}

func TestCRDImmutableTargetPlans(t *testing.T) {
	lib := crdLibrary(t)
	a, url := runtimeCRDServer(t)
	inputs := crdInput()
	inputs["server"] = url
	executor := crdExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		change  func(map[string]any)
		reasons []string
	}{
		{"group", func(in map[string]any) {
			in["metadata"].(map[string]any)["name"] = "widgets.other.test"
			in["spec"].(map[string]any)["group"] = "other.test"
		}, []string{"metadata.name", "spec.group"}},
		{"plural", func(in map[string]any) {
			in["metadata"].(map[string]any)["name"] = "gadgets.example.test"
			in["spec"].(map[string]any)["names"].(map[string]any)["plural"] = "gadgets"
		}, []string{"metadata.name", "spec.names.plural"}},
		{"kind", func(in map[string]any) {
			in["spec"].(map[string]any)["names"].(map[string]any)["kind"] = "Gadget"
		}, []string{"spec.names.kind"}},
		{"scope", func(in map[string]any) {
			in["spec"].(map[string]any)["scope"] = "Cluster"
		}, []string{"spec.scope"}},
		{"server", func(in map[string]any) {
			in["server"] = "http://127.0.0.1:1"
		}, []string{"server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desired := crdInput()
			desired["server"] = url
			tc.change(desired)
			executor.Inputs = desired
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			step := crdStep(t, plan)
			require.Equal(t, runtime.DecisionReplace, step.Decision)
			require.Equal(t, tc.reasons, step.ReplacementReasons)
		})
	}
	a.mu.Lock()
	a.objects["widgets.example.test"].Spec.Scope = "Cluster"
	a.mu.Unlock()
	executor.Inputs = inputs
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, crdStep(t, plan).Decision)
	require.Equal(t, []string{"spec.scope"}, crdStep(t, plan).ReplacementReasons)
}

func TestCRDTimeoutNativeConstraint(t *testing.T) {
	lib := crdLibrary(t)
	for _, timeout := range []string{"0s", "-1s", "invalid"} {
		t.Run(timeout, func(t *testing.T) {
			inputs := crdInput()
			inputs["server"], inputs["create-timeout"] = "http://127.0.0.1:1", timeout
			executor := crdExecutor(t, lib, "evaluated", inputs, nil)
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.subject: schema: constraints[")
			require.ErrorContains(t, err, "create-timeout")
		})
	}
}

func TestCRDValidWebhookNative(t *testing.T) {
	lib := crdLibrary(t)
	for _, target := range []map[string]any{
		{"url": "https://convert.example.test", "ca-bundle": "Y2E="},
		{"service": map[string]any{"namespace": "system", "name": "convert", "port": int64(65535)}},
	} {
		inputs := crdInput()
		inputs["server"] = "http://127.0.0.1:1"
		inputs["spec"].(map[string]any)["conversion"] = map[string]any{
			"strategy": "Webhook", "webhook": map[string]any{
				"conversion-review-versions": []any{"v1"}, "client-config": target,
			},
		}
		executor := crdExecutor(t, lib, "evaluated", inputs, nil)
		plan, err := executor.Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, runtime.DecisionCreate, crdStep(t, plan).Decision)
	}
}
