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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

var drainSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		return nil, err
	}
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	if err != nil {
		return nil, err
	}
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin",
		Dir: strings.TrimSpace(string(module))})
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("schema extraction: %v", warnings)
	}
	return schema, nil
})

func drainLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := drainSchema()
	require.NoError(t, err)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.drain-node": schema.Actions["drain-node"].Constraints}
	return lib
}

func drainSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/drain-node",
		strings.TrimSuffix(fixture, ".ub"),
		"factory.ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func TestDrainNodeAPIGate(t *testing.T) {
	descriptor := libraryapi.Descriptor{FormatVersion: 1, ImplementedAPIs: []string{"1.0"},
		GeneratorAPI: "1.0"}
	compatibility, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &descriptor})
	require.NoError(t, err)
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(compatibility,
		func(string) (*runtime.LibrarySchema, []string, error) {
			t.Error("schema extraction ran before API gate")
			return nil, nil, nil
		}, nil)
	_, _, err = cache.Read(".")
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
}

func drainExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	body := drainSyntax(t, fixture)
	libs := map[string]*runtime.Library{"core": lib}
	if fixture == "deferred-config" {
		libs["seed"] = &runtime.Library{Actions: lib.Actions}
	}
	store, err := local.NewStore(dir, "drain", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "drain", Version: "v0", ContentRevision: "c0"}}
}

func drainSavedApply(t *testing.T, e *runtime.Executor, p *runtime.Plan) error {
	t.Helper()
	body, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), saved)
	return err
}

func drainDecision(t *testing.T, p *runtime.Plan, want runtime.Decision) {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "action.drain" {
			require.Equal(t, want, step.Decision)
			return
		}
	}
	t.Fatal("missing drain action")
}

type drainServer struct {
	mu    sync.Mutex
	obj   *corev1.Node
	calls []string
	url   string
}

func newDrainNodeServer(t *testing.T) *drainServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &drainServer{obj: &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "example", UID: "uid", ResourceVersion: "rv1"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path == "/api/v1/pods" {
			require.Equal(t, "fieldSelector=spec.nodeName%3Dexample&limit=500", r.URL.RawQuery)
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(&corev1.PodList{Items: []corev1.Pod{}}))
			return
		}
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

func (a *drainServer) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func drainInputs(server string) map[string]any {
	return map[string]any{"server": server, "name": "example", "expected-uid": "uid", "timeout": nil}
}

func TestDrainNodeCompiledInputs(t *testing.T) {
	lib := drainLibrary(t)
	fixtures, err := os.ReadDir("testdata/ub/drain-node")
	require.NoError(t, err)
	for _, fixture := range fixtures {
		name := fixture.Name()
		if strings.HasPrefix(name, "deferred") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			checker := check.NewSyntax(drainSyntax(t, name),
				map[string]*runtime.Library{"core": lib}, nil, "")
			if strings.HasPrefix(name, "schema-") {
				require.Error(t, checker.References(nil).Err())
				return
			}
			require.NoError(t, checker.References(nil).Err())
			err := checker.LiteralConstraints().Err()
			if strings.HasPrefix(name, "invalid-") {
				require.ErrorContains(t, err, "action.drain")
				require.ErrorContains(t, err, "constraints[")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDrainNodeFactoryCompile(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	for _, fixture := range []string{"valid", "invalid-empty-name", "schema-namespace"} {
		t.Run(fixture, func(t *testing.T) {
			err := compile.Run(compile.Options{
				FactoryPath: filepath.Join("testdata/ub/drain-node", fixture),
				OutDir:      coreCompileDirectory(t), StackName: "drain",
				GoVersion: "1.26.2", CLIVersion: "v0.13.1",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root},
				Stdout: io.Discard, Stderr: io.Discard,
			})
			switch fixture {
			case "invalid-empty-name":
				require.ErrorContains(t, err, "action.drain")
				require.ErrorContains(t, err, "constraints[0]")
			case "schema-namespace":
				require.ErrorContains(t, err, "namespace")
			default:
				require.NoError(t, err)
			}
		})
	}
}

func drainOutput(t *testing.T, e *runtime.Executor, server string) {
	t.Helper()
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	require.Equal(t, map[string]any{"server": server, "api-version": "v1", "kind": "Node",
		"name": "example", "uid": "uid", "unschedulable": true, "drained": true,
		"drained-pods": []any{}, "skipped-pods": []any{}},
		snapshot.Outputs["result"])
}

func TestDrainNodeNativePlansAndTriggers(t *testing.T) {
	for _, trigger := range []string{"default", "explicit", "always"} {
		t.Run(trigger, func(t *testing.T) {
			api, lib, dir := newDrainNodeServer(t), drainLibrary(t), coreCompileDirectory(t)
			inputs, fixture := drainInputs(api.url), "evaluated"
			if trigger != "default" {
				fixture, inputs["trigger"] = "explicit", "stable"
				if trigger == "always" {
					inputs["trigger"] = "always"
				}
			}
			executor := func() *runtime.Executor { return drainExecutor(t, lib, dir, fixture, inputs) }
			e := executor()
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			drainDecision(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			require.NoError(t, drainSavedApply(t, e, p))

			require.Equal(t, drainRunCalls(true), api.take())
			drainOutput(t, e, api.url)
			api.mu.Lock()
			api.obj.Spec.Unschedulable = false
			api.mu.Unlock()
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			want := runtime.DecisionSkip
			if trigger == "always" {
				want = runtime.DecisionRerun
			}
			drainDecision(t, p, want)
			require.Empty(t, api.take())
			require.NoError(t, drainSavedApply(t, e, p))
			if trigger == "always" {
				require.Equal(t, drainRunCalls(true), api.take())
			} else {
				require.Empty(t, api.take())
			}
			drainOutput(t, e, api.url)
			api.mu.Lock()
			require.Equal(t, trigger == "always", api.obj.Spec.Unschedulable)
			api.mu.Unlock()
			inputs["timeout"] = "1m"
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger == "explicit" {
				drainDecision(t, p, runtime.DecisionSkip)
			} else {
				drainDecision(t, p, runtime.DecisionRerun)
			}
			require.NoError(t, drainSavedApply(t, e, p))
			switch trigger {
			case "explicit":
				require.Empty(t, api.take())
			case "always":
				require.Equal(t, drainRunCalls(false), api.take())
			default:
				require.Equal(t, drainRunCalls(true), api.take())
			}
			if trigger == "explicit" {
				inputs["trigger"] = "changed"
				e = executor()
				p, err = e.Plan(t.Context())
				require.NoError(t, err)
				drainDecision(t, p, runtime.DecisionRerun)
				require.NoError(t, drainSavedApply(t, e, p))
				require.Equal(t, drainRunCalls(true), api.take())
			}
			inputs["name"], inputs["expected-uid"] = "BAD", ""
			e = executor()
			snapshot, err := e.Store.Current()
			require.NoError(t, err)
			snapshot.Find("action.drain").Inputs["name"] = "BAD"
			snapshot.Find("action.drain").Inputs["expected-uid"] = ""
			revision, err := e.Store.Write(snapshot)
			require.NoError(t, err)
			require.NoError(t, e.Store.SetCurrent(revision))
			e.Destroy = true
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, drainSavedApply(t, e, p))
			require.Empty(t, api.take())
			api.mu.Lock()
			require.True(t, api.obj.Spec.Unschedulable)
			api.mu.Unlock()
		})
	}
}

func TestDrainNodeNativeInvalidKnownAndSaved(t *testing.T) {
	api, lib, dir := newDrainNodeServer(t), drainLibrary(t), coreCompileDirectory(t)
	valid := drainInputs(api.url)
	valid["trigger"] = "stable"
	e := drainExecutor(t, lib, dir, "explicit", valid)
	first, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, drainSavedApply(t, e, first))
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
			bad := drainExecutor(t, lib, dir, "explicit", inputs)
			_, err := bad.Plan(t.Context())
			require.ErrorContains(t, err, "action.drain")
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, api.take())
			good := drainExecutor(t, lib, dir, "explicit", valid)
			skip, err := good.Plan(t.Context())
			require.NoError(t, err)
			drainDecision(t, skip, runtime.DecisionSkip)
			rerunInputs := maps.Clone(valid)
			rerunInputs["trigger"] = "rerun"
			rerun, err := drainExecutor(t, lib, dir, "explicit", rerunInputs).Plan(t.Context())
			require.NoError(t, err)
			drainDecision(t, rerun, runtime.DecisionRerun)
			for _, plan := range []*runtime.Plan{rerun, skip} {
				err = drainSavedApply(t, bad, plan)
				require.ErrorContains(t, err, "action.drain")
				require.ErrorContains(t, err, tc.diagnostic)
				require.Empty(t, api.take())
			}
		})
	}
}

type drainSeed struct{ Request any }
type drainSeedOutput struct{ Value any }

func (s *drainSeed) Run(context.Context, *config.Configuration) (*drainSeedOutput, error) {
	return &drainSeedOutput{Value: s.Request}, nil
}

func TestDrainNodeNativeDeferred(t *testing.T) {
	for _, field := range []string{"name", "expected-uid", "timeout", "server"} {
		for _, mode := range []string{"valid", "invalid", "null"} {
			if field == "server" && mode != "valid" {
				continue
			}
			t.Run(field+"/"+mode, func(t *testing.T) {
				api, lib := newDrainNodeServer(t), drainLibrary(t)
				lib.Actions["seed"] = runtime.MakeAction[
					drainSeed, *drainSeedOutput, *config.Configuration]()
				inputs := drainInputs(api.url)
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
				e := drainExecutor(t, lib, coreCompileDirectory(t), fixture, inputs)
				p, err := e.Plan(t.Context())
				require.NoError(t, err)
				require.Empty(t, api.take())
				err = drainSavedApply(t, e, p)
				if mode == "valid" || mode == "null" && field == "timeout" {
					require.NoError(t, err)
					require.Equal(t, drainRunCalls(true), api.take())
					drainOutput(t, e, api.url)
				} else {
					require.ErrorContains(t, err, "action.drain")
					require.ErrorContains(t, err, "constraints[")
					require.Empty(t, api.take())
				}
			})
		}
	}
}

func TestDrainNodeConfigurationChangeDoesNotTrigger(t *testing.T) {
	api, other := newDrainNodeServer(t), newDrainNodeServer(t)
	lib, dir := drainLibrary(t), coreCompileDirectory(t)
	inputs := drainInputs(api.url)
	e := drainExecutor(t, lib, dir, "evaluated", inputs)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, drainSavedApply(t, e, p))
	api.take()
	inputs["server"] = other.url
	e = drainExecutor(t, lib, dir, "evaluated", inputs)
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	drainDecision(t, p, runtime.DecisionSkip)
	require.NoError(t, drainSavedApply(t, e, p))
	drainOutput(t, e, api.url)
	require.Empty(t, api.take())
	require.Empty(t, other.take())
}

func drainRunCalls(patch bool) []string {
	calls := []string{"GET /api/v1/nodes/example"}
	if patch {
		calls = append(calls, "PATCH /api/v1/nodes/example")
	}
	return append(calls, "GET /api/v1/pods?fieldSelector=spec.nodeName%3Dexample&limit=500",
		"GET /api/v1/nodes/example")
}

func TestDrainNodePendingKnownInvalid(t *testing.T) {
	api, lib := newDrainNodeServer(t), drainLibrary(t)
	lib.Actions["seed"] = runtime.MakeAction[drainSeed, *drainSeedOutput, *config.Configuration]()
	for _, tc := range []struct {
		fixture, field string
		value          any
		rule           string
	}{
		{"deferred-name", "expected-uid", "", "constraints[1]"},
		{"deferred-uid", "name", "BAD", "constraints[0]"},
		{"deferred-timeout", "expected-uid", "", "constraints[1]"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			inputs := drainInputs(api.url)
			inputs[tc.field] = tc.value
			e := drainExecutor(t, lib, coreCompileDirectory(t), tc.fixture, inputs)
			_, err := e.Plan(t.Context())
			require.ErrorContains(t, err, "action.drain")
			require.ErrorContains(t, err, tc.rule)
			require.Empty(t, api.take())
		})
	}
}

func TestDrainNodeRemovalHistoricalInputs(t *testing.T) {
	api, lib, dir := newDrainNodeServer(t), drainLibrary(t), coreCompileDirectory(t)
	inputs := drainInputs(api.url)
	inputs["trigger"] = "stable"
	e := drainExecutor(t, lib, dir, "explicit", inputs)
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, drainSavedApply(t, e, plan))
	api.take()
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	snapshot.Find("action.drain").Inputs["name"] = "BAD"
	snapshot.Find("action.drain").Inputs["expected-uid"] = ""
	revision, err := e.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, e.Store.SetCurrent(revision))
	e = drainExecutor(t, lib, dir, "removed", inputs)
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, drainSavedApply(t, e, plan))
	require.Empty(t, api.take())
	api.mu.Lock()
	defer api.mu.Unlock()
	require.True(t, api.obj.Spec.Unschedulable)
}
