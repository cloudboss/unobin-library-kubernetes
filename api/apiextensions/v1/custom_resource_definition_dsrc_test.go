package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	native "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubejson "k8s.io/apimachinery/pkg/util/json"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apiextensions/v1"
)

func crdLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
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
	lookup := schema.DataSources["custom-resource-definition"]
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.custom-resource-definition": lookup.Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.custom-resource-definition": lookup.Defaults,
	}
	return lib
}

func crdLookupExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"crd": lib}
	store, err := local.NewStore(t.TempDir(), "crd-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "crd-lookup", Version: "v0", ContentRevision: "c0"},
	}
}

func crdLookupSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func TestCRDLookupNativeInputs(t *testing.T) {
	lib := crdLookupLibrary(t)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		valid    bool
	}{
		{name: "valid", valid: true, metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{"EXAMPLE.COM/Key": "arbitrary value"},
			"labels": map[string]any{"example.com/Key": "value", "empty": ""}}},
		{name: "omitted optional maps", valid: true, metadata: map[string]any{"name": "cm"}},
		{name: "name boundary", valid: true, metadata: map[string]any{
			"name": strings.Repeat("a", 253)}},
		{name: "dotted name", valid: true, metadata: map[string]any{"name": "a.b-c"}},
		{name: "uppercase name", metadata: map[string]any{"name": "UPPER"}},
		{name: "slash name", metadata: map[string]any{"name": "a/b"}},
		{name: "null entries", valid: true, metadata: map[string]any{"name": "cm",
			"labels": map[string]any{"null": nil}, "annotations": map[string]any{"null": nil}}},
		{name: "underscore name", metadata: map[string]any{"name": "a_b"}},
		{name: "empty segment", metadata: map[string]any{"name": "a..b"}},
		{name: "leading hyphen", metadata: map[string]any{"name": "-a"}},
		{name: "trailing hyphen", metadata: map[string]any{"name": "a-"}},
		{name: "dotted hyphen", metadata: map[string]any{"name": "a.-b"}},
		{name: "trailing dot", metadata: map[string]any{"name": "a."}},
		{name: "invalid name", metadata: map[string]any{"name": "Bad_Name"}},
		{name: "empty name", metadata: map[string]any{"name": ""}},
		{name: "long name", metadata: map[string]any{"name": strings.Repeat("a", 254)}},
		{name: "invalid annotation", metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{"bad key": "value"}}},
		{name: "long annotation name", metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{strings.Repeat("a", 64): "value"}}},
		{name: "invalid label key", metadata: map[string]any{
			"name": "cm", "labels": map[string]any{"EXAMPLE.COM/Key": "value"}}},
		{name: "invalid label value", metadata: map[string]any{
			"name": "cm", "labels": map[string]any{"app": "bad value"}}},
		{name: "long label value", metadata: map[string]any{
			"name": "cm", "labels": map[string]any{"app": strings.Repeat("a", 64)}}},
		{name: "label empty key", metadata: map[string]any{
			"name": "cm", "labels": map[string]any{"": ""}}},
		{name: "label unicode", metadata: map[string]any{
			"name": "cm", "labels": map[string]any{"Key": ""}}},
		{name: "label prefix boundary", valid: true, metadata: map[string]any{
			"name": "cm", "labels": map[string]any{strings.Repeat("a", 253) + "/key": ""}}},
		{name: "label prefix overflow", metadata: map[string]any{
			"name": "cm", "labels": map[string]any{strings.Repeat("a", 254) + "/key": ""}}},
		{name: "label suffix boundary", valid: true, metadata: map[string]any{
			"name": "cm", "labels": map[string]any{strings.Repeat("a", 63): ""}}},
		{name: "label suffix overflow", metadata: map[string]any{
			"name": "cm", "labels": map[string]any{strings.Repeat("a", 64): ""}}},
		{name: "label value boundary", valid: true, metadata: map[string]any{
			"name": "cm", "labels": map[string]any{"key": strings.Repeat("a", 63)}}},
		{name: "annotation Kelvin", valid: true, metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{"K.example/Key": ""}}},
		{name: "annotation dotted I", valid: true, metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{"İ.example/İey": ""}}},
		{name: "annotation long s", metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{"ſ.example/key": ""}}},
		{name: "annotation unicode boundary", valid: true, metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{strings.Repeat("İ", 63): ""}}},
		{name: "annotation unicode overflow", metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{strings.Repeat("İ", 64): ""}}},
		{name: "annotation prefix boundary", valid: true, metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{strings.Repeat("a", 253) + "/key": ""}}},
		{name: "annotation prefix overflow", metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{strings.Repeat("a", 254) + "/key": ""}}},
		{name: "annotation empty key", metadata: map[string]any{
			"name": "cm", "annotations": map[string]any{"": ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
					Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
				}))
			}))
			t.Cleanup(server.Close)
			executor := crdLookupExecutor(t, lib, "lookup-inputs")
			executor.Inputs = map[string]any{"server": server.URL, "metadata": tc.metadata}
			want := "GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/" +
				tc.metadata["name"].(string)
			plan, err := executor.Plan(t.Context())
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{want}, requests)
			for _, step := range plan.Steps {
				require.Empty(t, step.SensitiveInputs)
				require.Empty(t, step.SensitiveOutputs)
			}
			_, err = executor.ApplyPlan(t.Context(), crdLookupSavedPlan(t, plan))
			require.NoError(t, err)
			requests = nil
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), crdLookupSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{
				want, want,
			}, requests)
			executor.Inputs["metadata"] = map[string]any{"name": "Bad_Name"}
			requests = nil
			_, err = executor.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
			require.Empty(t, requests)
		})
	}
}

type crdLookupSeed struct{ Metadata svc.CRDLookupMetadata }
type crdLookupSeedOutput struct{ Value svc.CRDLookupMetadata }

func (s *crdLookupSeed) Run(context.Context, any) (*crdLookupSeedOutput, error) {
	return &crdLookupSeedOutput{Value: s.Metadata}, nil
}

func TestCRDLookupDeferredInputs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		valid    bool
	}{
		{"valid", map[string]any{"name": "cm"}, true},
		{"name", map[string]any{"name": "Bad_Name"}, false},
		{"label key", map[string]any{"name": "cm", "labels": map[string]any{"bad key": ""}}, false},
		{"label value", map[string]any{"name": "cm", "labels": map[string]any{"app": "bad value"}},
			false},
		{"annotation", map[string]any{"name": "cm", "annotations": map[string]any{"bad key": ""}},
			false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lib := crdLookupLibrary(t)
			lib.Actions = map[string]runtime.ActionRegistration{
				"seed": runtime.MakeAction[crdLookupSeed, *crdLookupSeedOutput, any](),
			}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(&native.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				}))
			}))
			t.Cleanup(server.Close)
			executor := crdLookupExecutor(t, lib, "lookup-deferred")
			executor.Inputs = map[string]any{"server": server.URL,
				"metadata": tc.metadata}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, requests)
			_, err = executor.ApplyPlan(t.Context(), crdLookupSavedPlan(t, plan))
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/cm",
			}, requests)
		})
	}
}

type crdLookupStable struct{ Value string }

type crdLookupStableOutput struct{ Value string }

func (r *crdLookupStable) Create(
	context.Context, *config.Configuration,
) (*crdLookupStableOutput, error) {
	return &crdLookupStableOutput{Value: r.Value}, nil
}

func (r *crdLookupStable) Read(
	_ context.Context, _ *config.Configuration,
	prior runtime.Prior[crdLookupStable, *crdLookupStableOutput, *config.Configuration],
) (*crdLookupStableOutput, error) {
	return prior.Outputs, nil
}

func (r *crdLookupStable) Update(
	context.Context, *config.Configuration,
	runtime.Prior[crdLookupStable, *crdLookupStableOutput, *config.Configuration],
) (*crdLookupStableOutput, error) {
	panic("unexpected update")
}

func (r *crdLookupStable) Delete(
	context.Context, *config.Configuration,
	runtime.Prior[crdLookupStable, *crdLookupStableOutput, *config.Configuration],
) error {
	return nil
}

func TestCRDLookupSavedInputs(t *testing.T) {
	for _, tc := range []struct {
		metadata map[string]any
		valid    bool
	}{
		{map[string]any{"name": "cm"}, true},
		{map[string]any{"name": "Bad_Name"}, false},
		{map[string]any{"name": "cm", "labels": map[string]any{"bad key": ""}}, false},
		{map[string]any{"name": "cm", "labels": map[string]any{"key": "bad value"}}, false},
		{map[string]any{"name": "cm", "annotations": map[string]any{"bad key": ""}}, false},
	} {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			lib := crdLookupLibrary(t)
			lib.Resources["stable"] = runtime.MakeResource[
				crdLookupStable, *crdLookupStableOutput, *config.Configuration,
			](runtime.ResourceDefinition[
				crdLookupStable, *crdLookupStableOutput, *config.Configuration,
			]{SchemaVersion: 1})
			rules := lib.Constraints
			lib.Constraints = nil
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(&native.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				}))
			}))
			t.Cleanup(server.Close)
			executor := crdLookupExecutor(t, lib, "lookup-saved")
			executor.Inputs = map[string]any{"server": server.URL, "metadata": tc.metadata}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), crdLookupSavedPlan(t, plan))
			require.NoError(t, err)
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			decisions := map[string]runtime.Decision{}
			for _, step := range plan.Steps {
				if step.Address == "resource.stable" || step.Address == "data-source.endpoint" {
					decisions[step.Address] = step.Decision
				}
			}
			require.Equal(t, map[string]runtime.Decision{
				"resource.stable": runtime.DecisionNoOp, "data-source.endpoint": runtime.DecisionRead,
			}, decisions)
			file := crdLookupSavedPlan(t, plan)
			lib.Constraints = rules
			requests = nil
			_, err = executor.Plan(t.Context())
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
			}
			_, err = executor.ApplyPlan(t.Context(), file)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, []string{
					"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/cm",
					"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/cm",
				}, requests)
				requests = nil
			} else {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
			}
			removed := crdLookupExecutor(t, lib, "lookup-removed")
			removed.Store = executor.Store
			removed.Inputs = executor.Inputs
			removal, err := removed.Plan(t.Context())
			require.NoError(t, err)
			result, err := removed.ApplyPlan(t.Context(), crdLookupSavedPlan(t, removal))
			require.NoError(t, err)
			require.Equal(t, map[string]any{}, result.Data)
			require.Empty(t, requests)
			executor = removed
			executor.Destroy = true
			removal, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), crdLookupSavedPlan(t, removal))
			require.NoError(t, err)
			require.Empty(t, requests)
		})
	}
}

func TestCRDLookupLiteralInputs(t *testing.T) {
	lib := crdLookupLibrary(t)
	for _, tc := range []struct{ fixture, diagnostic string }{
		{"valid", ""}, {"null-maps", ""}, {"missing-metadata", "metadata"},
		{"null-metadata", "type mismatch"}, {"missing-name", "name"}, {"null-name", "type mismatch"},
		{"wrong-metadata", "type mismatch"}, {"wrong-name", "type mismatch"},
		{"wrong-labels", "type mismatch"},
		{"namespace", "namespace"}, {"spec", "spec"}, {"status", "status"},
		{"wait", "wait-for-established"}, {"invalid-name", "constraints["},
		{"invalid-label-key", "constraints["}, {"invalid-label-value", "constraints["},
		{"invalid-annotation-key", "constraints["},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			body := crdFixture(t, "lookup-literal-"+tc.fixture)
			checker := check.NewSyntax(body, map[string]*runtime.Library{"crd": lib}, nil, "")
			err := checker.References(nil).Err()
			if err == nil {
				err = checker.LiteralConstraints().Err()
			}
			if tc.diagnostic == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.diagnostic)
			}
		})
	}
}

func TestCRDLookupPartialInputs(t *testing.T) {
	for _, tc := range []struct {
		fixture, name string
		valid         bool
	}{
		{"lookup-partial", "Bad_Name", false}, {"lookup-partial-key", "cm", false},
		{"lookup-partial", "cm", true},
	} {
		t.Run(tc.fixture+tc.name, func(t *testing.T) {
			lib := crdLookupLibrary(t)
			lib.Actions = map[string]runtime.ActionRegistration{
				"seed": runtime.MakeAction[crdLookupSeed, *crdLookupSeedOutput, any](),
			}
			a, url := runtimeCRDServer(t)
			executor := crdExecutor(t, lib, tc.fixture, map[string]any{
				"server": url, "name": tc.name, "metadata": map[string]any{"name": "label"},
			}, nil)
			plan, err := executor.Plan(t.Context())
			require.Empty(t, a.drain())
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				return
			}
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"GET cm"}, a.drain())
		})
	}
}

func TestCRDLookupRequiredEvaluatedInputs(t *testing.T) {
	lib := crdLookupLibrary(t)
	for _, metadata := range []any{nil, "wrong", map[string]any{},
		map[string]any{"name": nil}, map[string]any{"name": int64(1)},
		map[string]any{"name": "cm", "labels": "wrong"},
		map[string]any{"name": "cm", "labels": map[string]any{"key": int64(1)}},
		map[string]any{"name": "cm", "namespace": "ignored"},
	} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			a, url := runtimeCRDServer(t)
			executor := crdExecutor(t, lib, "lookup-opaque", map[string]any{
				"server": url, "metadata": metadata,
			}, nil)
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.endpoint")
			require.Empty(t, a.drain())
		})
	}
}

func TestCRDLookupObservedValues(t *testing.T) {
	lib := crdLookupLibrary(t)
	for _, change := range []string{"equal", "spec", "metadata", "status", "uid", "hit to miss",
		"miss to hit", "equal absence"} {
		t.Run(change, func(t *testing.T) {
			a, url := runtimeCRDServer(t)
			obj := &native.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "cm", UID: "uid", ResourceVersion: "1"},
				Spec:       native.CustomResourceDefinitionSpec{Group: "example.test"},
			}
			if change != "miss to hit" && change != "equal absence" {
				a.objects["cm"] = obj
			}
			executor := crdExecutor(t, lib, "lookup-opaque", map[string]any{
				"server": url, "metadata": map[string]any{"name": "cm"},
			}, nil)
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			var observed []map[string]any
			for _, step := range plan.Steps {
				if step.Address == "data-source.endpoint" {
					require.Equal(t, runtime.DecisionRead, step.Decision)
					observed = append(observed, step.ObservedOutputs)
				}
			}
			want := map[string]any{
				"metadata": map[string]any{"name": "cm", "labels": map[string]any{},
					"annotations": map[string]any{}, "generation": int64(0),
					"uid": "uid", "resource-version": "1"},
				"spec": map[string]any{
					"group": "example.test", "scope": "", "preserve-unknown-fields": false,
					"versions": []any{}, "conversion": nil,
					"names": map[string]any{"plural": "", "singular": "", "kind": "", "list-kind": "",
						"short-names": nil, "categories": nil},
				},
				"status": map[string]any{
					"conditions": []any{}, "stored-versions": []any{},
					"accepted-names": map[string]any{"plural": "", "singular": "", "kind": "", "list-kind": "",
						"short-names": nil, "categories": nil},
				},
			}
			if change == "miss to hit" || change == "equal absence" {
				want["spec"], want["status"] = nil, nil
				want["metadata"].(map[string]any)["uid"] = ""
				want["metadata"].(map[string]any)["resource-version"] = ""
			}
			require.Equal(t, []map[string]any{want}, observed)
			a.mu.Lock()
			switch change {
			case "spec":
				obj.Spec.Group = "changed"
			case "metadata":
				obj.Annotations = map[string]string{"extra": "value"}
			case "status":
				obj.Status.StoredVersions = []string{"v2", "v1"}
			case "uid":
				obj.UID = "changed"
			case "hit to miss":
				delete(a.objects, "cm")
			case "miss to hit":
				a.objects["cm"] = obj
			}
			a.mu.Unlock()
			_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
			if change == "equal" || change == "equal absence" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			require.Equal(t, []string{"GET cm", "GET cm"}, a.drain())
		})
	}
}

func TestCRDLookupDestroyHistoricalInputs(t *testing.T) {
	lib := crdLookupLibrary(t)
	rules := lib.Constraints
	lib.Constraints = nil
	a, url := runtimeCRDServer(t)
	executor := crdExecutor(t, lib, "lookup-opaque", map[string]any{
		"server": url, "metadata": map[string]any{"name": "Bad_Name"},
	}, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"GET Bad_Name", "GET Bad_Name"}, a.drain())
	lib.Constraints = rules
	executor.Destroy = true
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), crdSavedPlan(t, plan))
	require.NoError(t, err)
	require.Empty(t, a.drain())
}

func TestCRDLookupEncodedLiveOutput(t *testing.T) {
	lib := crdLookupLibrary(t)
	body, err := os.ReadFile("testdata/crd-lookup/full.json")
	require.NoError(t, err)
	expected, err := os.ReadFile("testdata/crd-lookup/expected.json")
	require.NoError(t, err)
	var want map[string]any
	require.NoError(t, kubejson.Unmarshal(expected, &want))
	want["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["last-transition-time"] =
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Local().String()
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(body)
		require.NoError(t, err)
	}))
	defer server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	executor := crdExecutor(t, lib, "lookup-opaque", map[string]any{
		"server": server.URL, "metadata": map[string]any{"name": "widgets.example.test"},
	}, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	saved := crdSavedPlan(t, plan)
	var outputs []map[string]any
	for _, step := range saved.Steps {
		if step.Address == "data-source.endpoint" {
			outputs = append(outputs, step.ObservedOutputs)
		}
	}
	require.Equal(t, []map[string]any{want}, outputs)
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
		"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test",
	}, calls)
}

func TestCRDLookupEncodedPointerValues(t *testing.T) {
	lib := crdLookupLibrary(t)
	for _, mode := range []string{"absent", "empty", "empty client", "empty service"} {
		t.Run(mode, func(t *testing.T) {
			names := map[string]any{"plural": "", "singular": "", "kind": "", "list-kind": "",
				"short-names": nil, "categories": nil}
			version := map[string]any{
				"name": "v1", "served": false, "storage": false, "deprecated": false,
				"deprecation-warning": nil, "schema": nil, "subresources": nil,
				"additional-printer-columns": nil, "selectable-fields": nil,
			}
			webhook := map[string]any{"conversion-review-versions": []any{}, "client-config": nil}
			nativeVersion := map[string]any{"name": "v1", "schema": map[string]any{}}
			nativeNames := map[string]any{}
			nativeWebhook := map[string]any{}
			switch mode {
			case "empty":
				nativeVersion["schema"] = map[string]any{"openAPIV3Schema": map[string]any{}}
				nativeVersion["subresources"] = map[string]any{"status": map[string]any{}}
				nativeVersion["deprecationWarning"] = ""
				nativeVersion["additionalPrinterColumns"], nativeVersion["selectableFields"] = []any{}, []any{}
				nativeNames["shortNames"], nativeNames["categories"] = []any{}, []any{}
				nativeWebhook["clientConfig"] = map[string]any{"url": "", "caBundle": "",
					"service": map[string]any{"path": "", "port": int64(0)}}
				names["short-names"], names["categories"] = []any{}, []any{}
				version["schema"], version["subresources"] = map[string]any{},
					map[string]any{"status": map[string]any{}, "scale": nil}
				version["deprecation-warning"] = ""
				version["additional-printer-columns"], version["selectable-fields"] = []any{}, []any{}
				webhook["client-config"] = map[string]any{
					"url": "", "ca-bundle": "", "service": map[string]any{
						"namespace": "", "name": "", "path": "", "port": int64(0),
					},
				}
			case "empty client", "empty service":
				nativeWebhook["clientConfig"] = map[string]any{}
				client := map[string]any{"url": nil, "ca-bundle": nil, "service": nil}
				webhook["client-config"] = client
				if mode == "empty service" {
					nativeWebhook["clientConfig"].(map[string]any)["service"] = map[string]any{}
					client["service"] = map[string]any{
						"namespace": "", "name": "", "path": nil, "port": nil,
					}
					nativeVersion["subresources"] = map[string]any{}
					version["subresources"] = map[string]any{"status": nil, "scale": nil}
				}
			}
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"metadata": map[string]any{"name": "cm"},
					"spec": map[string]any{"versions": []any{nativeVersion}, "names": nativeNames,
						"conversion": map[string]any{"webhook": nativeWebhook}},
				}))
			}))
			defer server.Close()
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
			executor := crdExecutor(t, lib, "lookup-opaque", map[string]any{
				"server": server.URL, "metadata": map[string]any{"name": "cm"},
			}, nil)
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			saved := crdSavedPlan(t, plan)
			var outputs []map[string]any
			for _, step := range saved.Steps {
				if step.Address == "data-source.endpoint" {
					outputs = append(outputs, step.ObservedOutputs)
				}
			}
			require.Equal(t, []map[string]any{{
				"metadata": map[string]any{"name": "cm", "uid": "", "resource-version": "",
					"generation": int64(0), "labels": map[string]any{}, "annotations": map[string]any{}},
				"spec": map[string]any{"group": "", "scope": "", "names": names,
					"preserve-unknown-fields": false, "versions": []any{version},
					"conversion": map[string]any{"strategy": "", "webhook": webhook}},
				"status": map[string]any{"conditions": []any{}, "stored-versions": []any{},
					"accepted-names": map[string]any{"plural": "", "singular": "", "kind": "", "list-kind": "",
						"short-names": nil, "categories": nil}},
			}}, outputs)
			_, err = executor.ApplyPlan(t.Context(), saved)
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/cm",
				"GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/cm",
			}, calls)
		})
	}
}
