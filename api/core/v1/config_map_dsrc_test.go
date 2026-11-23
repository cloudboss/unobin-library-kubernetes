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

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func configMapInfoLibrary(t *testing.T) *runtime.Library {
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
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.config-map": schema.DataSources["config-map"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.config-map": schema.DataSources["config-map"].Defaults,
	}
	return lib
}

func configMapInfoExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": lib}
	store, err := local.NewStore(t.TempDir(), "config-map-info", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "config-map-info", Version: "v0", ContentRevision: "c0"},
	}
}

func configMapInfoSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func TestConfigMapInfoNativeInputs(t *testing.T) {
	lib := configMapInfoLibrary(t)
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
			executor := configMapInfoExecutor(t, lib, "config-map-inputs")
			executor.Inputs = map[string]any{"server": server.URL, "metadata": tc.metadata}
			want := "GET /api/v1/namespaces/default/configmaps/" + tc.metadata["name"].(string)
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
			_, err = executor.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.NoError(t, err)
			requests = nil
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
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

type configMapInfoSeed struct{ Metadata svc.ConfigMapLookupMetadata }
type configMapInfoSeedOutput struct{ Value svc.ConfigMapLookupMetadata }

func (s *configMapInfoSeed) Run(context.Context, any) (*configMapInfoSeedOutput, error) {
	return &configMapInfoSeedOutput{Value: s.Metadata}, nil
}

func TestConfigMapInfoDeferredInputs(t *testing.T) {
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
			lib := configMapInfoLibrary(t)
			lib.Actions = map[string]runtime.ActionRegistration{
				"seed": runtime.MakeAction[configMapInfoSeed, *configMapInfoSeedOutput, any](),
			}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"},
				}))
			}))
			t.Cleanup(server.Close)
			executor := configMapInfoExecutor(t, lib, "config-map-deferred")
			executor.Inputs = map[string]any{"server": server.URL,
				"metadata": tc.metadata}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, requests)
			_, err = executor.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"GET /api/v1/namespaces/default/configmaps/cm"}, requests)
		})
	}
}

type configMapInfoStable struct{ Value string }

type configMapInfoStableOutput struct{ Value string }

func (r *configMapInfoStable) Create(
	context.Context, *config.Configuration,
) (*configMapInfoStableOutput, error) {
	return &configMapInfoStableOutput{Value: r.Value}, nil
}

func (r *configMapInfoStable) Read(
	_ context.Context, _ *config.Configuration,
	prior runtime.Prior[configMapInfoStable, *configMapInfoStableOutput, *config.Configuration],
) (*configMapInfoStableOutput, error) {
	return prior.Outputs, nil
}

func (r *configMapInfoStable) Update(
	context.Context, *config.Configuration,
	runtime.Prior[configMapInfoStable, *configMapInfoStableOutput, *config.Configuration],
) (*configMapInfoStableOutput, error) {
	panic("unexpected update")
}

func (r *configMapInfoStable) Delete(
	context.Context, *config.Configuration,
	runtime.Prior[configMapInfoStable, *configMapInfoStableOutput, *config.Configuration],
) error {
	return nil
}

func TestConfigMapInfoSavedInputs(t *testing.T) {
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
			lib := configMapInfoLibrary(t)
			lib.Resources["stable"] = runtime.MakeResource[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			](runtime.ResourceDefinition[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			]{SchemaVersion: 1})
			rules := lib.Constraints
			lib.Constraints = nil
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"},
				}))
			}))
			t.Cleanup(server.Close)
			executor := configMapInfoExecutor(t, lib, "config-map-saved")
			executor.Inputs = map[string]any{"server": server.URL, "metadata": tc.metadata}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.NoError(t, err)
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			var decisions []runtime.Decision
			for _, step := range plan.Steps {
				if step.Address == "resource.stable" {
					decisions = append(decisions, step.Decision)
				}
			}
			require.Equal(t, []runtime.Decision{runtime.DecisionNoOp}, decisions)
			file := configMapInfoSavedPlan(t, plan)
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
					"GET /api/v1/namespaces/default/configmaps/cm",
					"GET /api/v1/namespaces/default/configmaps/cm",
				}, requests)
				requests = nil
			} else {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
			}
			removed := configMapInfoExecutor(t, lib, "config-map-removed")
			removed.Store = executor.Store
			removed.Inputs = executor.Inputs
			removal, err := removed.Plan(t.Context())
			require.NoError(t, err)
			result, err := removed.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, removal))
			require.NoError(t, err)
			require.Equal(t, map[string]any{}, result.Data)
			require.Empty(t, requests)
			executor = removed
			executor.Destroy = true
			removal, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, removal))
			require.NoError(t, err)
			require.Empty(t, requests)
		})
	}
}

func TestConfigMapInfoObservedValues(t *testing.T) {
	lib := configMapInfoLibrary(t)
	for _, change := range []string{"stable", "data", "binary-data", "resource-version", "uid",
		"immutable"} {
		t.Run(change, func(t *testing.T) {
			obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: "cm", Namespace: "default", UID: "uid", ResourceVersion: "1"},
				Data: map[string]string{"value": "雪"}, BinaryData: map[string][]byte{"bytes": {0}},
			}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			}))
			t.Cleanup(server.Close)
			executor := configMapInfoExecutor(t, lib, "config-map-inputs")
			executor.Inputs = map[string]any{"server": server.URL,
				"metadata": map[string]any{"name": "cm"}}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			var observed []map[string]any
			for _, step := range plan.Steps {
				if step.Address == "data-source.endpoint" {
					observed = append(observed, step.ObservedOutputs)
					require.Empty(t, step.SensitiveInputs)
					require.Empty(t, step.SensitiveOutputs)
				}
			}
			require.Equal(t, []map[string]any{{
				"metadata": map[string]any{"name": "cm", "namespace": "default", "uid": "uid",
					"resource-version": "1", "generation": int64(0),
					"annotations": map[string]any{}, "labels": map[string]any{}},
				"data":        map[string]any{"value": "雪"},
				"binary-data": map[string]any{"bytes": "AA=="}, "immutable": false,
			}}, observed)
			switch change {
			case "data":
				obj.Data["value"] = "changed"
			case "binary-data":
				obj.BinaryData["bytes"] = []byte{1}
			case "resource-version":
				obj.ResourceVersion = "2"
			case "uid":
				obj.UID = "changed"
			case "immutable":
				obj.Immutable = new(true)
			}
			_, err = executor.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			if change == "stable" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			require.Equal(t, []string{
				"GET /api/v1/namespaces/default/configmaps/cm",
				"GET /api/v1/namespaces/default/configmaps/cm",
			}, requests)
		})
	}
}
