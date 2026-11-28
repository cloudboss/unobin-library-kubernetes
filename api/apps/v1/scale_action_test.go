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
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func scaleLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := restartSchema()
	require.NoError(t, err)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.scale": schema.Actions["scale"].Constraints,
	}
	return lib
}

func scaleSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/scale", fixture, "factory.ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func TestScalePublicCompiler(t *testing.T) {
	lib := scaleLibrary(t)
	entries, err := os.ReadDir("testdata/ub/scale")
	require.NoError(t, err)
	for _, entry := range entries {
		fixture := entry.Name()
		t.Run(fixture, func(t *testing.T) {
			if fixture == "deferred" {
				return
			}
			checker := check.NewSyntax(scaleSyntax(t, fixture),
				map[string]*runtime.Library{"apps": lib}, nil, "")
			references := checker.References(nil).Err()
			constraints := checker.LiteralConstraints().Err()
			if strings.Contains(fixture, "required-invalid") {
				require.ErrorContains(t, references, "missing required input")
				return
			}
			if fixture == "unknown-invalid" {
				require.ErrorContains(t, references, "unknown field")
				return
			}
			if fixture == "replicas-string-invalid" || fixture == "replicas-fraction-invalid" {
				require.ErrorContains(t, references, "type mismatch")
				return
			}
			if strings.HasPrefix(fixture, "null-") {
				require.Error(t, references)
			} else {
				require.NoError(t, references)
			}
			if strings.Contains(fixture, "invalid") {
				require.ErrorContains(t, constraints, "action.scale")
				require.ErrorContains(t, constraints, "constraints[")
			} else {
				require.NoError(t, constraints)
			}
		})
	}
}

func scaleExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	body := scaleSyntax(t, fixture)
	libs := map[string]*runtime.Library{"apps": lib}
	store, err := local.NewStore(dir, "scale", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "scale", Version: "v0", ContentRevision: "c0"}}
}

func scaleStep(t *testing.T, p *runtime.Plan, want runtime.Decision) *runtime.PlanStep {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "action.scale" {
			require.Equal(t, want, step.Decision)
			return step
		}
	}
	t.Fatal("missing scale action")
	return nil
}

type scaleServer struct {
	mu    sync.Mutex
	obj   *autoscalingv1.Scale
	calls []string
	url   string
}

func newScaleServer(t *testing.T) *scaleServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &scaleServer{obj: &autoscalingv1.Scale{
		TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v1", Kind: "Scale"},
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", UID: "uid",
			ResourceVersion: "1"},
		Spec:   autoscalingv1.ScaleSpec{Replicas: 0},
		Status: autoscalingv1.ScaleStatus{Replicas: 0},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.RequestURI())
		require.Contains(t, []string{http.MethodGet, http.MethodPut}, r.Method)
		if r.Method == http.MethodPut {
			require.NoError(t, json.NewDecoder(r.Body).Decode(api.obj))
			api.obj.ResourceVersion = "2"
		}
		require.Equal(t, "/apis/apps/v1/namespaces/default/deployments/example/scale", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(api.obj))
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func (a *scaleServer) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func (a *scaleServer) replicas(replicas int32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.obj.Spec.Replicas = replicas
}

func scaleInputs(server string) map[string]any {
	return map[string]any{"server": server, "kind": "Deployment", "name": "example",
		"namespace": nil, "expected-uid": nil, "replicas": int64(0), "timeout": nil}
}

func TestScaleNativePlansAndTriggers(t *testing.T) {
	get := "GET /apis/apps/v1/namespaces/default/deployments/example/scale"
	put := "PUT /apis/apps/v1/namespaces/default/deployments/example/scale"
	for _, trigger := range []string{"default", "explicit", "always"} {
		t.Run(trigger, func(t *testing.T) {
			api := newScaleServer(t)
			lib, dir := scaleLibrary(t), t.TempDir()
			inputs, fixture := scaleInputs(api.url), "evaluated"
			inputs["replicas"] = int64(1)
			if trigger != "default" {
				fixture = "explicit"
				inputs["trigger"] = "stable"
				if trigger == "always" {
					inputs["trigger"] = "always"
				}
			}
			executor := func() *runtime.Executor { return scaleExecutor(t, lib, dir, fixture, inputs) }
			e := executor()
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			scaleStep(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			require.NoError(t, restartSavedApply(t, e, p))
			require.Equal(t, []string{get, put}, api.take())
			api.replicas(2)
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger != "always" {
				step := scaleStep(t, p, runtime.DecisionSkip)
				require.Equal(t, map[string]any{
					"server": api.url, "api-version": "apps/v1", "kind": "Deployment", "name": "example",
					"namespace": "default", "uid": "uid", "resource-version": "2",
					"replicas": int64(1),
				}, step.PriorOutputs)
				require.NoError(t, restartSavedApply(t, e, p))
				require.Empty(t, api.take())
			} else {
				scaleStep(t, p, runtime.DecisionRerun)
				api.replicas(1)
				require.NoError(t, restartSavedApply(t, e, p))
				require.Equal(t, []string{get, put}, api.take())
				api.replicas(2)
			}
			inputs["replicas"] = int64(2)
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger == "explicit" {
				scaleStep(t, p, runtime.DecisionSkip)
				require.NoError(t, restartSavedApply(t, e, p))
				require.Empty(t, api.take())
				inputs["trigger"] = "revision-2"
				e = executor()
				p, err = e.Plan(t.Context())
				require.NoError(t, err)
			}
			scaleStep(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			require.NoError(t, restartSavedApply(t, e, p))
			require.Equal(t, []string{get, put}, api.take())
			e = executor()
			e.Destroy = true
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, restartSavedApply(t, e, p))
			require.Empty(t, api.take())
		})
	}
}

func TestScaleNativeInvalidKnownAndSaved(t *testing.T) {
	api := newScaleServer(t)
	lib, dir := scaleLibrary(t), t.TempDir()
	valid := scaleInputs(api.url)
	valid["trigger"] = "stable"
	e := scaleExecutor(t, lib, dir, "explicit", valid)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	api.take()
	for _, tc := range scaleInvalidInputs() {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			inputs := maps.Clone(valid)
			inputs[tc.field] = tc.value
			bad := scaleExecutor(t, lib, dir, "explicit", inputs)
			_, err := bad.Plan(t.Context())
			require.ErrorContains(t, err, "action.scale:")
			require.ErrorContains(t, err, "schema: "+tc.rule)
			require.Empty(t, api.take())
			for _, fixture := range []string{"explicit", "evaluated"} {
				good := scaleExecutor(t, lib, dir, fixture, valid)
				saved, err := good.Plan(t.Context())
				require.NoError(t, err)
				want := runtime.DecisionSkip
				if fixture == "evaluated" {
					want = runtime.DecisionRerun
				}
				scaleStep(t, saved, want)
				bad := scaleExecutor(t, lib, dir, fixture, inputs)
				err = restartSavedApply(t, bad, saved)
				require.ErrorContains(t, err, "action.scale:")
				require.ErrorContains(t, err, "schema: "+tc.rule)
				require.Empty(t, api.take())
			}
		})
	}
}

type scaleSeed struct{ Values map[string]any }
type scaleSeedOutput struct{ Resolved map[string]any }

func (s *scaleSeed) Run(context.Context, *config.Configuration) (*scaleSeedOutput, error) {
	return &scaleSeedOutput{Resolved: s.Values}, nil
}

func TestScaleNativeDeferred(t *testing.T) {
	cases := append(scaleInvalidInputs(), scaleInvalid{field: "replicas", value: int64(0)})
	for _, tc := range cases {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			api := newScaleServer(t)
			lib := scaleLibrary(t)
			lib.Actions["seed"] = runtime.MakeAction[scaleSeed, *scaleSeedOutput,
				*config.Configuration]()
			inputs := scaleInputs(api.url)
			inputs[tc.field] = tc.value
			e := scaleExecutor(t, lib, t.TempDir(), "deferred", inputs)
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.take())
			err = restartSavedApply(t, e, p)
			if tc.rule == "" {
				require.NoError(t, err)
				require.Equal(t, []string{
					"GET /apis/apps/v1/namespaces/default/deployments/example/scale",
					"PUT /apis/apps/v1/namespaces/default/deployments/example/scale",
				}, api.take())
			} else {
				require.ErrorContains(t, err, "action.scale:")
				require.ErrorContains(t, err, "schema: "+tc.rule)
				require.Empty(t, api.take())
			}
		})
	}
}

func TestScaleNativeMaximumReplicas(t *testing.T) {
	api := newScaleServer(t)
	inputs := scaleInputs(api.url)
	inputs["replicas"] = int64(2147483647)
	e := scaleExecutor(t, scaleLibrary(t), t.TempDir(), "evaluated", inputs)
	p, err := e.Plan(context.Background())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/deployments/example/scale",
		"PUT /apis/apps/v1/namespaces/default/deployments/example/scale",
	}, api.take())
}

type scaleInvalid struct {
	field string
	value any
	rule  string
}

func scaleInvalidInputs() []scaleInvalid {
	return []scaleInvalid{
		{"kind", nil, "constraints[0]"}, {"kind", "deployment", "constraints[0]"},
		{"kind", "DaemonSet", "constraints[0]"},
		{"name", nil, "constraints[1]"}, {"name", "", "constraints[1]"},
		{"name", "BAD", "constraints[1]"}, {"name", "bad/name", "constraints[1]"},
		{"name", strings.Repeat("a", 254), "constraints[1]"},
		{"replicas", nil, "constraints[2]"}, {"replicas", -1, "constraints[2]"},
		{"replicas", 1.5, "constraints[2]"}, {"replicas", int64(2147483648), "constraints[2]"},
		{"namespace", "", "constraints[3]"}, {"namespace", "BAD", "constraints[3]"},
		{"namespace", "bad.namespace", "constraints[3]"},
		{"namespace", strings.Repeat("a", 64), "constraints[3]"},
		{"expected-uid", "", "constraints[4]"},
		{"timeout", "0s", "constraints[5]"}, {"timeout", "-1s", "constraints[5]"},
		{"timeout", "bad", "constraints[5]"},
	}
}

func TestScaleNativeRemoval(t *testing.T) {
	api := newScaleServer(t)
	lib, dir := scaleLibrary(t), t.TempDir()
	e := scaleExecutor(t, lib, dir, "evaluated", scaleInputs(api.url))
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/deployments/example/scale",
		"PUT /apis/apps/v1/namespaces/default/deployments/example/scale",
	}, api.take())
	e = scaleExecutor(t, lib, dir, "removed", nil)
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	require.Empty(t, api.take())
}
