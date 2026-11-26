package dynamic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func apiResourcesLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	lib := waitLibrary(t)
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.api-resources": lib.Schema.DataSources["api-resources"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.api-resources": lib.Schema.DataSources["api-resources"].Defaults,
	}
	return lib
}

func apiResourcesSyntax(t *testing.T, fixture string) *syntax.File {
	t.Helper()
	path := filepath.Join("testdata/ub/api-resources", fixture, "factory.ub")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(path, source)
	require.NoError(t, err)
	return parsed
}

func apiResourcesExecutor(
	t *testing.T, lib *runtime.Library, fixture string, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	parsed := apiResourcesSyntax(t, fixture)
	libs := map[string]*runtime.Library{"dynamic": lib}
	store, err := local.NewStore(t.TempDir(), "api-resources", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Store: store, Inputs: inputs,
	}
}

func apiResourcesServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var value any
		switch r.URL.Path {
		case "/api":
			value = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			value = metav1.APIGroupList{Groups: []metav1.APIGroup{}}
		case "/api/v1":
			value = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true,
					Verbs: []string{"list", "get"}},
				{Name: "pods/status", Kind: "Pod", Namespaced: true,
					Verbs: []string{"get"}},
			}}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(value))
	}))
	t.Cleanup(server.Close)
	return server.URL, calls
}

func TestAPIResourcesNativeLiteralInputs(t *testing.T) {
	lib := apiResourcesLibrary(t)
	for _, tc := range []struct {
		fixture string
		valid   bool
	}{
		{"literal/valid", true}, {"literal/empty", true}, {"literal/omitted", true},
		{"literal/invalid-empty", false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			parsed := apiResourcesSyntax(t, tc.fixture)
			checker := check.NewSyntax(parsed.Factory.Body,
				map[string]*runtime.Library{"dynamic": lib}, nil, "")
			require.Empty(t, checker.References(nil).Errors())
			diagnostics := checker.LiteralConstraints()
			if tc.valid {
				require.Empty(t, diagnostics.Errors())
			} else {
				require.Contains(t, diagnostics.Error(), "data-source.target")
				require.Contains(t, diagnostics.Error(), "constraints[")
			}
		})
	}
	for _, fixture := range []string{"structural/unknown", "structural/wrong-verbs",
		"structural/wrong-scope", "literal/invalid-null"} {
		parsed := apiResourcesSyntax(t, fixture)
		checker := check.NewSyntax(parsed.Factory.Body,
			map[string]*runtime.Library{"dynamic": lib}, nil, "")
		require.NotEmpty(t, checker.References(nil).Errors())
	}
}

func TestAPIResourcesNativeEvaluatedDeferredAndSavedInputs(t *testing.T) {
	lib := apiResourcesLibrary(t)
	lib.Actions["seed"] = runtime.MakeAction[waitSeed, *waitSeedOutput, *config.Configuration]()
	real, rules := lib.DataSources["api-resources"], lib.Constraints
	for _, tc := range []struct {
		name  string
		verbs any
		valid bool
	}{
		{"omitted", nil, true}, {"empty", []any{}, true},
		{"custom duplicate", []any{"custom", "custom"}, true},
		{"empty item", []any{"get", ""}, false}, {"null item", []any{nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lib.DataSources["api-resources"], lib.Constraints = real, rules
			server, calls := apiResourcesServer(t)
			inputs := map[string]any{"server": server, "verbs": tc.verbs}
			known := apiResourcesExecutor(t, lib, "known", inputs)
			plan, err := known.Plan(t.Context())
			if tc.valid {
				require.NoError(t, err)
				require.Positive(t, calls.Load())
				_, err = known.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
				require.Zero(t, calls.Load())
			}
			calls.Store(0)
			deferred := apiResourcesExecutor(t, lib, "deferred", map[string]any{
				"server": server, "settings": map[string]any{"verbs": tc.verbs},
			})
			plan, err = deferred.Plan(t.Context())
			require.NoError(t, err)
			require.Zero(t, calls.Load())
			_, err = deferred.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
			if tc.valid {
				require.NoError(t, err)
				require.Positive(t, calls.Load())
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
				require.Zero(t, calls.Load())
			}
			lib.DataSources["api-resources"] = runtime.MakeDataSource[
				apiResourcesHistorical, *svc.APIResourcesDataSourceOutput, *config.Configuration,
			]()
			lib.Constraints = nil
			historical := apiResourcesExecutor(t, lib, "known", inputs)
			plan, err = historical.Plan(t.Context())
			require.NoError(t, err)
			_, err = historical.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
			require.NoError(t, err)
			plan, err = historical.Plan(t.Context())
			require.NoError(t, err)
			saved := waitSavedPlan(t, plan)
			lib.DataSources["api-resources"], lib.Constraints = real, rules
			calls.Store(0)
			_, err = historical.Plan(t.Context())
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
				require.Zero(t, calls.Load())
			}
			calls.Store(0)
			_, err = historical.ApplyPlan(t.Context(), saved)
			if tc.valid {
				require.NoError(t, err)
				require.Positive(t, calls.Load())
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
				require.Zero(t, calls.Load())
			}
		})
	}
}

type apiResourcesHistorical svc.APIResourcesDataSource

func (*apiResourcesHistorical) Read(
	context.Context, *config.Configuration,
) (*svc.APIResourcesDataSourceOutput, error) {
	return &svc.APIResourcesDataSourceOutput{Resources: []svc.APIResourceRow{}}, nil
}

func TestAPIResourcesPublicDefaultsAndOutputs(t *testing.T) {
	lib := apiResourcesLibrary(t)
	server, _ := apiResourcesServer(t)
	executor := apiResourcesExecutor(t, lib, "defaults", map[string]any{"server": server})
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	result, err := executor.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"resources": []any{map[string]any{
		"api-version": "v1", "group": "", "version": "v1", "name": "configmaps",
		"singular-name": "", "kind": "ConfigMap", "namespaced": true,
		"verbs": []any{"get", "list"}, "short-names": []any{}, "categories": []any{},
	}}}, result.Outputs)
}
