package v1_test

import (
	"context"
	"encoding/json"
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

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func namespaceInfoLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read("../../../../api/core/v1", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := core.Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.namespace-info": schema.DataSources["namespace-info"].Constraints,
	}
	return lib
}

func namespaceInfoExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": lib}
	base := "../../../../_output/tests/core-v1-namespace-info"
	require.NoError(t, os.MkdirAll(base, 0o700))
	dir, err := os.MkdirTemp(base, "state-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	store, err := local.NewStore(dir, "namespace-info", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "namespace-info", Version: "v0", ContentRevision: "c0"},
	}
}

func namespaceInfoSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func TestNamespaceInfoNativeInputs(t *testing.T) {
	lib := namespaceInfoLibrary(t)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		valid    bool
	}{
		{name: "valid", valid: true, metadata: map[string]any{
			"name": "svc", "annotations": map[string]any{"EXAMPLE.COM/Key": "arbitrary value"},
			"labels": map[string]any{"example.com/Key": "value", "empty": ""}}},
		{name: "nullable values", valid: true, metadata: map[string]any{
			"name": "svc", "annotations": map[string]any{"İ.COM/Key": nil},
			"labels": map[string]any{"app": nil}}},
		{name: "omitted optional maps", valid: true, metadata: map[string]any{"name": "svc"}},
		{name: "invalid name", metadata: map[string]any{"name": "Bad_Name"}},
		{name: "empty name", metadata: map[string]any{"name": ""}},
		{name: "long name", metadata: map[string]any{"name": strings.Repeat("a", 254)}},
		{name: "invalid annotation", metadata: map[string]any{
			"name": "svc", "annotations": map[string]any{"bad key": "value"}}},
		{name: "long annotation name", metadata: map[string]any{
			"name": "svc", "annotations": map[string]any{strings.Repeat("a", 64): "value"}}},
		{name: "invalid label key", metadata: map[string]any{
			"name": "svc", "labels": map[string]any{"EXAMPLE.COM/Key": "value"}}},
		{name: "invalid label value", metadata: map[string]any{
			"name": "svc", "labels": map[string]any{"app": "bad value"}}},
		{name: "long label value", metadata: map[string]any{
			"name": "svc", "labels": map[string]any{"app": strings.Repeat("a", 64)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				code, reason := http.StatusNotFound, metav1.StatusReasonNotFound
				if !tc.valid {
					code, reason = http.StatusForbidden, metav1.StatusReasonForbidden
				}
				w.WriteHeader(code)
				require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
					Status: "Failure", Reason: reason, Code: int32(code),
				}))
			}))
			t.Cleanup(server.Close)
			executor := namespaceInfoExecutor(t, lib, "namespace-info-inputs")
			executor.Inputs = map[string]any{"server": server.URL, "metadata": tc.metadata}
			plan, err := executor.Plan(t.Context())
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"GET /api/v1/namespaces/svc"}, requests)
			for _, step := range plan.Steps {
				require.Empty(t, step.SensitiveInputs)
				require.Empty(t, step.SensitiveOutputs)
			}
			_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
			require.NoError(t, err)
			requests = nil
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /api/v1/namespaces/svc",
				"GET /api/v1/namespaces/svc",
			}, requests)
			executor.Inputs["metadata"] = map[string]any{"name": "Bad_Name"}
			requests = nil
			_, err = executor.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
			require.Empty(t, requests)
		})
	}
}

type namespaceInfoSeed struct{ Metadata svc.NamespaceLookupMetadata }
type namespaceInfoSeedOutput struct{ Value svc.NamespaceLookupMetadata }

func (s *namespaceInfoSeed) Run(context.Context, any) (*namespaceInfoSeedOutput, error) {
	return &namespaceInfoSeedOutput{Value: s.Metadata}, nil
}

func TestNamespaceInfoDeferredInputs(t *testing.T) {
	for _, name := range []string{"svc", "Bad_Name"} {
		t.Run(name, func(t *testing.T) {
			lib := namespaceInfoLibrary(t)
			lib.Actions = map[string]runtime.ActionRegistration{
				"seed": runtime.MakeAction[namespaceInfoSeed, *namespaceInfoSeedOutput, any](),
			}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: name},
				}))
			}))
			t.Cleanup(server.Close)
			executor := namespaceInfoExecutor(t, lib, "namespace-info-deferred")
			executor.Inputs = map[string]any{"server": server.URL,
				"metadata": map[string]any{"name": name}}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, requests)
			_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
			if name == "Bad_Name" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"GET /api/v1/namespaces/svc"}, requests)
		})
	}
}

func TestNamespaceInfoSavedInputs(t *testing.T) {
	lib := namespaceInfoLibrary(t)
	lib.Resources["anchor"] = runtime.MakeResource[
		namespaceInfoAnchor, *namespaceInfoAnchorOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		namespaceInfoAnchor, *namespaceInfoAnchorOutput, *config.Configuration,
	]{SchemaVersion: 1})
	rules := lib.Constraints
	lib.Constraints = nil
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "Bad_Name"},
		}))
	}))
	t.Cleanup(server.Close)
	executor := namespaceInfoExecutor(t, lib, "namespace-info-saved")
	executor.Inputs = map[string]any{"server": server.URL,
		"metadata": map[string]any{"name": "Bad_Name"}}
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	decisions := map[string]runtime.Decision{}
	for _, step := range plan.Steps {
		if step.Kind == runtime.NodeResource || step.Kind == runtime.NodeDataSource {
			decisions[step.Address] = step.Decision
		}
	}
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionNoOp, "data-source.endpoint": runtime.DecisionRead,
	}, decisions)
	file := namespaceInfoSavedPlan(t, plan)
	lib.Constraints = rules
	requests = nil
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
	require.Empty(t, requests)
	_, err = executor.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
	require.Empty(t, requests)
	executor.Destroy = true
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	require.Empty(t, requests)
	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
}

type namespaceInfoAnchor struct{ Value string }
type namespaceInfoAnchorOutput struct{ Value string }
type namespaceInfoAnchorPrior = runtime.Prior[
	namespaceInfoAnchor, *namespaceInfoAnchorOutput, *config.Configuration,
]

func (r *namespaceInfoAnchor) Create(
	context.Context, *config.Configuration,
) (*namespaceInfoAnchorOutput, error) {
	return &namespaceInfoAnchorOutput{Value: r.Value}, nil
}

func (r *namespaceInfoAnchor) Read(
	_ context.Context, _ *config.Configuration, p namespaceInfoAnchorPrior,
) (*namespaceInfoAnchorOutput, error) {
	return p.Outputs, nil
}

func (r *namespaceInfoAnchor) Update(
	context.Context, *config.Configuration, namespaceInfoAnchorPrior,
) (*namespaceInfoAnchorOutput, error) {
	return &namespaceInfoAnchorOutput{Value: r.Value}, nil
}

func (r *namespaceInfoAnchor) Delete(
	context.Context, *config.Configuration, namespaceInfoAnchorPrior,
) error {
	return nil
}

func TestNamespaceInfoObservationConsistency(t *testing.T) {
	lib := namespaceInfoLibrary(t)
	for _, change := range []string{"stable", "metadata", "finalizers", "absence"} {
		t.Run(change, func(t *testing.T) {
			obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "svc", ResourceVersion: "1"},
				Spec: corev1.NamespaceSpec{Finalizers: []corev1.FinalizerName{"z", "a"}}}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				if obj == nil {
					w.WriteHeader(http.StatusNotFound)
					require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
						Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
					}))
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			}))
			defer server.Close()
			executor := namespaceInfoExecutor(t, lib, "namespace-info-inputs")
			executor.Inputs = map[string]any{"server": server.URL, "metadata": map[string]any{"name": "svc"}}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			switch change {
			case "metadata":
				obj.ResourceVersion = "2"
			case "finalizers":
				obj.Spec.Finalizers = []corev1.FinalizerName{"a", "z"}
			case "absence":
				obj = nil
			}
			_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
			if change == "stable" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			require.Equal(t, []string{"GET /api/v1/namespaces/svc", "GET /api/v1/namespaces/svc"}, requests)
		})
	}
}

func TestNamespaceInfoSavedWithUnchangedResource(t *testing.T) {
	lib := namespaceInfoLibrary(t)
	lib.Resources["anchor"] = runtime.MakeResource[
		namespaceInfoAnchor, *namespaceInfoAnchorOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		namespaceInfoAnchor, *namespaceInfoAnchorOutput, *config.Configuration,
	]{SchemaVersion: 1})
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "svc", UID: "stable"},
		}))
	}))
	defer server.Close()
	executor := namespaceInfoExecutor(t, lib, "namespace-info-saved")
	executor.Inputs = map[string]any{"server": server.URL, "metadata": map[string]any{"name": "svc"}}
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	requests = nil
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	decisions := map[string]runtime.Decision{}
	for _, step := range plan.Steps {
		if step.Kind == runtime.NodeResource || step.Kind == runtime.NodeDataSource {
			decisions[step.Address] = step.Decision
		}
	}
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionNoOp, "data-source.endpoint": runtime.DecisionRead,
	}, decisions)
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"GET /api/v1/namespaces/svc", "GET /api/v1/namespaces/svc"}, requests)
}
