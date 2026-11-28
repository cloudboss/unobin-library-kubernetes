package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func uncordonLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := cordonSchema()
	require.NoError(t, err)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.uncordon-node": schema.Actions["uncordon-node"].Constraints}
	return lib
}

func uncordonSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/uncordon-node",
		strings.TrimSuffix(fixture, ".ub"),
		"factory.ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func uncordonExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	body := uncordonSyntax(t, fixture)
	libs := map[string]*runtime.Library{"core": lib}
	if fixture == "deferred-config" {
		libs["seed"] = &runtime.Library{Actions: lib.Actions}
	}
	store, err := local.NewStore(dir, "uncordon", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "uncordon", Version: "v0", ContentRevision: "c0"}}
}

func uncordonSavedApply(t *testing.T, e *runtime.Executor, p *runtime.Plan) error {
	t.Helper()
	body, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), saved)
	return err
}

func uncordonDecision(t *testing.T, p *runtime.Plan, want runtime.Decision) {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "action.uncordon" {
			require.Equal(t, want, step.Decision)
			return
		}
	}
	t.Fatal("missing uncordon action")
}

type uncordonServer struct {
	mu    sync.Mutex
	obj   *corev1.Node
	calls []string
	url   string
}

func newUncordonNodeServer(t *testing.T) *uncordonServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &uncordonServer{obj: &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "example", UID: "uid", ResourceVersion: "rv1"},
		Spec: corev1.NodeSpec{Unschedulable: true}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.RequestURI())
		require.Equal(t, "/api/v1/nodes/example", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "PATCH" {
			require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			before, err := json.Marshal(api.obj)
			require.NoError(t, err)
			after, err := patch.Apply(before)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(after, api.obj))
			api.obj.ResourceVersion = "rv2"
		}
		require.NoError(t, json.NewEncoder(w).Encode(api.obj))
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func (a *uncordonServer) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func uncordonInputs(server string) map[string]any {
	return map[string]any{"server": server, "name": "example", "expected-uid": "uid", "timeout": nil}
}

func TestUncordonNodeCompiledInputs(t *testing.T) {
	lib := uncordonLibrary(t)
	fixtures, err := os.ReadDir("testdata/ub/uncordon-node")
	require.NoError(t, err)
	for _, fixture := range fixtures {
		name := fixture.Name()
		if strings.HasPrefix(name, "deferred") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			checker := check.NewSyntax(uncordonSyntax(t, name),
				map[string]*runtime.Library{"core": lib}, nil, "")
			if strings.HasPrefix(name, "schema-") {
				require.Error(t, checker.References(nil).Err())
				return
			}
			require.NoError(t, checker.References(nil).Err())
			err := checker.LiteralConstraints().Err()
			if strings.HasPrefix(name, "invalid-") {
				require.ErrorContains(t, err, "action.uncordon")
				require.ErrorContains(t, err, "constraints[")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUncordonNodeFactoryCompile(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	for _, fixture := range []string{"valid", "invalid-empty-name", "schema-namespace"} {
		t.Run(fixture, func(t *testing.T) {
			err := compile.Run(compile.Options{
				FactoryPath: filepath.Join("testdata/ub/uncordon-node", fixture),
				OutDir:      coreCompileDirectory(t), StackName: "uncordon",
				GoVersion: "1.26.2", CLIVersion: "v0.13.1",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root},
				Stdout: io.Discard, Stderr: io.Discard,
			})
			switch fixture {
			case "invalid-empty-name":
				require.ErrorContains(t, err, "action.uncordon")
				require.ErrorContains(t, err, "constraints[0]")
			case "schema-namespace":
				require.ErrorContains(t, err, "namespace")
			default:
				require.NoError(t, err)
			}
		})
	}
}

func uncordonOutput(t *testing.T, e *runtime.Executor, server string) {
	t.Helper()
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	require.Equal(t, map[string]any{"server": server, "api-version": "v1", "kind": "Node",
		"name": "example", "uid": "uid", "resource-version": "rv2", "unschedulable": false},
		snapshot.Outputs["result"])
}

func TestUncordonNodeNativePlansAndTriggers(t *testing.T) {
	for _, trigger := range []string{"default", "explicit", "always"} {
		t.Run(trigger, func(t *testing.T) {
			api, lib, dir := newUncordonNodeServer(t), uncordonLibrary(t), coreCompileDirectory(t)
			inputs, fixture := uncordonInputs(api.url), "evaluated"
			if trigger != "default" {
				fixture, inputs["trigger"] = "explicit", "stable"
				if trigger == "always" {
					inputs["trigger"] = "always"
				}
			}
			executor := func() *runtime.Executor { return uncordonExecutor(t, lib, dir, fixture, inputs) }
			e := executor()
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			uncordonDecision(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			require.NoError(t, uncordonSavedApply(t, e, p))
			get, patch := "GET /api/v1/nodes/example", "PATCH /api/v1/nodes/example"
			require.Equal(t, []string{get, patch}, api.take())
			uncordonOutput(t, e, api.url)
			api.mu.Lock()
			api.obj.Spec.Unschedulable = true
			api.mu.Unlock()
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			want := runtime.DecisionSkip
			if trigger == "always" {
				want = runtime.DecisionRerun
			}
			uncordonDecision(t, p, want)
			require.Empty(t, api.take())
			require.NoError(t, uncordonSavedApply(t, e, p))
			if trigger == "always" {
				require.Equal(t, []string{get, patch}, api.take())
			} else {
				require.Empty(t, api.take())
			}
			uncordonOutput(t, e, api.url)
			api.mu.Lock()
			require.Equal(t, trigger != "always", api.obj.Spec.Unschedulable)
			api.mu.Unlock()
			inputs["timeout"] = "1m"
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger == "explicit" {
				uncordonDecision(t, p, runtime.DecisionSkip)
			} else {
				uncordonDecision(t, p, runtime.DecisionRerun)
			}
			require.NoError(t, uncordonSavedApply(t, e, p))
			switch trigger {
			case "explicit":
				require.Empty(t, api.take())
			case "always":
				require.Equal(t, []string{get}, api.take())
			default:
				require.Equal(t, []string{get, patch}, api.take())
			}
			if trigger == "explicit" {
				inputs["trigger"] = "changed"
				e = executor()
				p, err = e.Plan(t.Context())
				require.NoError(t, err)
				uncordonDecision(t, p, runtime.DecisionRerun)
				require.NoError(t, uncordonSavedApply(t, e, p))
				require.Equal(t, []string{get, patch}, api.take())
			}
			inputs["name"], inputs["expected-uid"] = "BAD", ""
			e = executor()
			snapshot, err := e.Store.Current()
			require.NoError(t, err)
			snapshot.Find("action.uncordon").Inputs["name"] = "BAD"
			snapshot.Find("action.uncordon").Inputs["expected-uid"] = ""
			revision, err := e.Store.Write(snapshot)
			require.NoError(t, err)
			require.NoError(t, e.Store.SetCurrent(revision))
			e.Destroy = true
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, uncordonSavedApply(t, e, p))
			require.Empty(t, api.take())
			api.mu.Lock()
			require.False(t, api.obj.Spec.Unschedulable)
			api.mu.Unlock()
		})
	}
}

func TestUncordonNodeNativeInvalidKnownAndSaved(t *testing.T) {
	api, lib, dir := newUncordonNodeServer(t), uncordonLibrary(t), coreCompileDirectory(t)
	valid := uncordonInputs(api.url)
	valid["trigger"] = "stable"
	e := uncordonExecutor(t, lib, dir, "explicit", valid)
	first, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, uncordonSavedApply(t, e, first))
	api.take()
	for _, tc := range []struct {
		field      string
		value      any
		diagnostic string
	}{
		{"name", "BAD", "constraints[0]"}, {"name", "", "constraints[0]"},
		{"name", strings.Repeat("a", 254), "constraints[0]"},
		{"expected-uid", "", "constraints[1]"},
		{"timeout", "bad", "constraints[2]"}, {"timeout", "0s", "constraints[2]"},
		{"timeout", "-1s", "constraints[2]"},
		{"timeout", "999999999999999999h", "constraints[2]"},
		{"name", nil, "constraints[0]"}, {"expected-uid", nil, "constraints[1]"},
	} {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			inputs := maps.Clone(valid)
			inputs[tc.field] = tc.value
			bad := uncordonExecutor(t, lib, dir, "explicit", inputs)
			_, err := bad.Plan(t.Context())
			require.ErrorContains(t, err, "action.uncordon")
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, api.take())
			good := uncordonExecutor(t, lib, dir, "explicit", valid)
			skip, err := good.Plan(t.Context())
			require.NoError(t, err)
			uncordonDecision(t, skip, runtime.DecisionSkip)
			rerunInputs := maps.Clone(valid)
			rerunInputs["trigger"] = "rerun"
			rerun, err := uncordonExecutor(t, lib, dir, "explicit", rerunInputs).Plan(t.Context())
			require.NoError(t, err)
			uncordonDecision(t, rerun, runtime.DecisionRerun)
			for _, plan := range []*runtime.Plan{rerun, skip} {
				err = uncordonSavedApply(t, bad, plan)
				require.ErrorContains(t, err, "action.uncordon")
				require.ErrorContains(t, err, tc.diagnostic)
				require.Empty(t, api.take())
			}
		})
	}
}

type uncordonSeed struct{ Request any }
type uncordonSeedOutput struct{ Value any }

func (s *uncordonSeed) Run(context.Context, *config.Configuration) (*uncordonSeedOutput, error) {
	return &uncordonSeedOutput{Value: s.Request}, nil
}

func TestUncordonNodeNativeDeferred(t *testing.T) {
	for _, field := range []string{"name", "expected-uid", "timeout", "server"} {
		for _, mode := range []string{"valid", "invalid", "null"} {
			if field == "server" && mode != "valid" {
				continue
			}
			t.Run(field+"/"+mode, func(t *testing.T) {
				api, lib := newUncordonNodeServer(t), uncordonLibrary(t)
				lib.Actions["seed"] = runtime.MakeAction[
					uncordonSeed, *uncordonSeedOutput, *config.Configuration]()
				inputs := uncordonInputs(api.url)
				if field == "timeout" {
					inputs[field] = "1m"
				}
				if mode == "null" {
					inputs[field] = nil
				}
				if mode == "invalid" {
					inputs[field] = map[string]string{"name": "BAD", "expected-uid": "", "timeout": "0s"}[field]
				}
				fixture := map[string]string{"name": "deferred-name", "expected-uid": "deferred-uid",
					"timeout": "deferred-timeout", "server": "deferred-config"}[field]
				e := uncordonExecutor(t, lib, coreCompileDirectory(t), fixture, inputs)
				p, err := e.Plan(t.Context())
				require.NoError(t, err)
				var pending *runtime.PlanStep
				for _, step := range p.Steps {
					if step.Address == "action.uncordon" {
						pending = step
					}
				}
				require.NotNil(t, pending)
				if field == "server" {
					require.Equal(t, "library-config.core", pending.DeferredConfig)
				} else {
					require.Equal(t, map[string][]string{field: {"action.seed.value"}},
						pending.UnresolvedInputs)
					require.True(t, runtime.IsPendingValue(pending.Inputs[field]))
				}
				require.Empty(t, api.take())
				err = uncordonSavedApply(t, e, p)
				if mode == "valid" || mode == "null" && field == "timeout" {
					require.NoError(t, err)
					require.Equal(t, []string{
						"GET /api/v1/nodes/example", "PATCH /api/v1/nodes/example",
					}, api.take())
					uncordonOutput(t, e, api.url)
				} else {
					require.ErrorContains(t, err, "action.uncordon")
					require.ErrorContains(t, err, "constraints[")
					require.Empty(t, api.take())
				}
			})
		}
	}
}

func TestUncordonNodeConfigurationChangeDoesNotTrigger(t *testing.T) {
	api, other := newUncordonNodeServer(t), newUncordonNodeServer(t)
	lib, dir := uncordonLibrary(t), coreCompileDirectory(t)
	inputs := uncordonInputs(api.url)
	e := uncordonExecutor(t, lib, dir, "evaluated", inputs)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, uncordonSavedApply(t, e, p))
	api.take()
	inputs["server"] = other.url
	e = uncordonExecutor(t, lib, dir, "evaluated", inputs)
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	uncordonDecision(t, p, runtime.DecisionSkip)
	require.NoError(t, uncordonSavedApply(t, e, p))
	uncordonOutput(t, e, api.url)
	require.Empty(t, api.take())
	require.Empty(t, other.take())
}

func TestUncordonNodeRemovalWithHistoricalInvalidInputs(t *testing.T) {
	api, lib, dir := newUncordonNodeServer(t), uncordonLibrary(t), coreCompileDirectory(t)
	e := uncordonExecutor(t, lib, dir, "evaluated", uncordonInputs(api.url))
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, uncordonSavedApply(t, e, plan))
	require.Equal(t, []string{
		"GET /api/v1/nodes/example", "PATCH /api/v1/nodes/example",
	}, api.take())
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	snapshot.Find("action.uncordon").Inputs["name"] = "BAD"
	snapshot.Find("action.uncordon").Inputs["expected-uid"] = ""
	snapshot.Find("action.uncordon").Inputs["timeout"] = "invalid"
	revision, err := e.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, e.Store.SetCurrent(revision))
	e = uncordonExecutor(t, lib, dir, "removed", nil)
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, uncordonSavedApply(t, e, plan))
	snapshot, err = e.Store.Current()
	require.NoError(t, err)
	require.Nil(t, snapshot.Find("action.uncordon"))
	require.Empty(t, api.take())
	api.mu.Lock()
	defer api.mu.Unlock()
	require.False(t, api.obj.Spec.Unschedulable)
}
