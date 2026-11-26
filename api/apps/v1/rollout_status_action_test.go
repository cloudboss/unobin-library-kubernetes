package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func statusLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := restartSchema()
	require.NoError(t, err)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.rollout-status": schema.Actions["rollout-status"].Constraints,
	}
	return lib
}

func statusSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/rollout-status", fixture, "factory.ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func TestRolloutStatusPublicCompiler(t *testing.T) {
	lib := statusLibrary(t)
	for _, fixture := range []string{"valid", "kind-invalid", "name-invalid", "uid-invalid",
		"required-invalid", "type-invalid", "evaluated", "namespace-invalid",
		"namespace-length-invalid", "name-length-invalid", "timeout-invalid", "generation-invalid",
		"generation-negative-invalid", "generation-fraction-invalid", "generation-overflow-invalid",
		"maximums-valid", "optionals-null-valid", "null-name-invalid", "null-kind-invalid"} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(statusSyntax(t, fixture),
				map[string]*runtime.Library{"apps": lib}, nil, "")
			references := checker.References(nil).Err()
			constraints := checker.LiteralConstraints().Err()
			if fixture == "required-invalid" {
				require.ErrorContains(t, references, "missing required input \"name\"")
				return
			}
			if fixture == "type-invalid" || fixture == "generation-fraction-invalid" {
				require.ErrorContains(t, references, "type mismatch")
				return
			}
			if strings.HasPrefix(fixture, "null-") {
				require.Error(t, references)
			} else {
				require.NoError(t, references)
			}
			if strings.Contains(fixture, "invalid") {
				require.ErrorContains(t, constraints, "action.status")
				require.ErrorContains(t, constraints, "constraints[")
			} else {
				require.NoError(t, constraints)
			}
		})
	}
}

func statusExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	body := statusSyntax(t, fixture)
	libs := map[string]*runtime.Library{"apps": lib}
	store, err := local.NewStore(dir, "status", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "status", Version: "v0", ContentRevision: "c0"}}
}

func statusStep(t *testing.T, p *runtime.Plan, want runtime.Decision) *runtime.PlanStep {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "action.status" {
			require.Equal(t, want, step.Decision)
			return step
		}
	}
	t.Fatal("missing status action")
	return nil
}

type statusServer struct {
	mu    sync.Mutex
	obj   *appsv1.Deployment
	calls []string
	url   string
}

func newStatusServer(t *testing.T) *statusServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &statusServer{obj: &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", UID: "uid", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: new(int32(0))},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.RequestURI())
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/apis/apps/v1/namespaces/default/deployments/example", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(api.obj))
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func (a *statusServer) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func (a *statusServer) generation(generation int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.obj.Generation, a.obj.Status.ObservedGeneration = generation, generation
}

func statusInputs(server string) map[string]any {
	return map[string]any{"server": server, "kind": "Deployment", "name": "example",
		"namespace": nil, "expected-uid": nil, "expected-generation": nil, "timeout": nil}
}

func TestRolloutStatusNativePlansAndTriggers(t *testing.T) {
	get := "GET /apis/apps/v1/namespaces/default/deployments/example"
	for _, trigger := range []string{"default", "explicit", "always"} {
		t.Run(trigger, func(t *testing.T) {
			api := newStatusServer(t)
			lib, dir := statusLibrary(t), t.TempDir()
			inputs, fixture := statusInputs(api.url), "evaluated"
			inputs["expected-generation"] = int64(1)
			if trigger != "default" {
				fixture = "explicit"
				inputs["trigger"] = "stable"
				if trigger == "always" {
					inputs["trigger"] = "always"
				}
			}
			executor := func() *runtime.Executor { return statusExecutor(t, lib, dir, fixture, inputs) }
			e := executor()
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			statusStep(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			require.NoError(t, restartSavedApply(t, e, p))
			require.Equal(t, []string{get}, api.take())
			api.generation(2)
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger != "always" {
				step := statusStep(t, p, runtime.DecisionSkip)
				require.Equal(t, map[string]any{
					"server": api.url, "api-version": "apps/v1", "kind": "Deployment", "name": "example",
					"namespace": "default", "uid": "uid", "generation": int64(1),
					"observed-generation": int64(1),
				}, step.PriorOutputs)
				require.NoError(t, restartSavedApply(t, e, p))
				require.Empty(t, api.take())
			} else {
				statusStep(t, p, runtime.DecisionRerun)
				api.generation(1)
				require.NoError(t, restartSavedApply(t, e, p))
				require.Equal(t, []string{get}, api.take())
				api.generation(2)
			}
			inputs["expected-generation"] = int64(2)
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger == "explicit" {
				statusStep(t, p, runtime.DecisionSkip)
				require.NoError(t, restartSavedApply(t, e, p))
				require.Empty(t, api.take())
				inputs["trigger"] = "revision-2"
				e = executor()
				p, err = e.Plan(t.Context())
				require.NoError(t, err)
			}
			statusStep(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			require.NoError(t, restartSavedApply(t, e, p))
			require.Equal(t, []string{get}, api.take())
			e = executor()
			e.Destroy = true
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, restartSavedApply(t, e, p))
			require.Empty(t, api.take())
		})
	}
}

func TestRolloutStatusNativeInvalidKnownAndSaved(t *testing.T) {
	api := newStatusServer(t)
	lib, dir := statusLibrary(t), t.TempDir()
	valid := statusInputs(api.url)
	valid["trigger"] = "stable"
	e := statusExecutor(t, lib, dir, "explicit", valid)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	api.take()
	for _, tc := range []struct {
		field string
		value any
		rule  string
	}{
		{"kind", nil, "constraints[0]"}, {"kind", "deployment", "constraints[0]"},
		{"name", nil, "constraints[1]"}, {"name", "BAD", "constraints[1]"},
		{"name", strings.Repeat("a", 254), "constraints[1]"},
		{"namespace", "bad.namespace", "constraints[2]"},
		{"namespace", strings.Repeat("a", 64), "constraints[2]"},
		{"expected-uid", "", "constraints[3]"},
		{"expected-generation", int64(0), "constraints[4]"},
		{"expected-generation", int64(-1), "constraints[4]"},
		{"expected-generation", 1.5, "constraints[4]"},
		{"expected-generation", int64(9007199254740992), "constraints[4]"},
		{"timeout", "0s", "constraints[5]"}, {"timeout", "bad", "constraints[5]"},
	} {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			inputs := maps.Clone(valid)
			inputs[tc.field] = tc.value
			bad := statusExecutor(t, lib, dir, "explicit", inputs)
			_, err := bad.Plan(t.Context())
			require.ErrorContains(t, err, "action.status:")
			require.ErrorContains(t, err, "schema: "+tc.rule)
			require.Empty(t, api.take())
			for _, fixture := range []string{"explicit", "evaluated"} {
				good := statusExecutor(t, lib, dir, fixture, valid)
				saved, err := good.Plan(t.Context())
				require.NoError(t, err)
				want := runtime.DecisionSkip
				if fixture == "evaluated" {
					want = runtime.DecisionRerun
				}
				statusStep(t, saved, want)
				bad := statusExecutor(t, lib, dir, fixture, inputs)
				err = restartSavedApply(t, bad, saved)
				require.ErrorContains(t, err, "action.status:")
				require.ErrorContains(t, err, "schema: "+tc.rule)
				require.Empty(t, api.take())
			}
		})
	}
}

type statusSeed struct{ Values map[string]any }
type statusSeedOutput struct{ Resolved map[string]any }

func (s *statusSeed) Run(context.Context, *config.Configuration) (*statusSeedOutput, error) {
	return &statusSeedOutput{Resolved: s.Values}, nil
}

func TestRolloutStatusNativeDeferred(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
		rule  string
	}{
		{"kind", "Deployment", ""}, {"expected-generation", int64(1), ""},
		{"kind", "BAD", "constraints[0]"}, {"kind", nil, "constraints[0]"},
		{"name", "BAD", "constraints[1]"}, {"name", nil, "constraints[1]"},
		{"namespace", "bad.namespace", "constraints[2]"},
		{"expected-uid", "", "constraints[3]"},
		{"expected-generation", int64(0), "constraints[4]"},
		{"expected-generation", 1.5, "constraints[4]"},
		{"expected-generation", int64(9007199254740992), "constraints[4]"},
		{"timeout", "bad", "constraints[5]"},
	} {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			api := newStatusServer(t)
			lib := statusLibrary(t)
			lib.Actions["seed"] = runtime.MakeAction[statusSeed, *statusSeedOutput,
				*config.Configuration]()
			inputs := statusInputs(api.url)
			inputs[tc.field] = tc.value
			e := statusExecutor(t, lib, t.TempDir(), "deferred", inputs)
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.take())
			err = restartSavedApply(t, e, p)
			if tc.rule == "" {
				require.NoError(t, err)
				require.Equal(t, []string{
					"GET /apis/apps/v1/namespaces/default/deployments/example",
				}, api.take())
			} else {
				require.ErrorContains(t, err, "action.status:")
				require.ErrorContains(t, err, "schema: "+tc.rule)
				require.Empty(t, api.take())
			}
		})
	}
}

func TestRolloutStatusNativeSafeGeneration(t *testing.T) {
	api := newStatusServer(t)
	api.generation(9007199254740991)
	inputs := statusInputs(api.url)
	inputs["expected-generation"] = int64(9007199254740991)
	e := statusExecutor(t, statusLibrary(t), t.TempDir(), "evaluated", inputs)
	p, err := e.Plan(context.Background())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/deployments/example",
	}, api.take())
}
