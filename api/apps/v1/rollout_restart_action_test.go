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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

var restartSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
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

func restartLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := restartSchema()
	require.NoError(t, err)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.rollout-restart": schema.Actions["rollout-restart"].Constraints}
	return lib
}

func restartSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/rollout-restart",
		strings.TrimSuffix(fixture, ".ub"),
		"factory.ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func TestRolloutRestartPublicCompiler(t *testing.T) {
	lib := restartLibrary(t)
	for _, fixture := range []string{"valid.ub", "kind-invalid.ub", "name-invalid.ub",
		"uid-invalid.ub",
		"timestamp-invalid.ub", "required-invalid.ub", "type-invalid.ub", "evaluated.ub",
		"namespace-invalid.ub", "namespace-length-invalid.ub", "name-length-invalid.ub",
		"timeout-invalid.ub", "false-wait-valid.ub", "maximums-valid.ub"} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(restartSyntax(t, fixture),
				map[string]*runtime.Library{"apps": lib}, nil, "")
			references := checker.References(nil).Err()
			constraints := checker.LiteralConstraints().Err()
			if fixture == "required-invalid.ub" || fixture == "type-invalid.ub" {
				if fixture == "required-invalid.ub" {
					require.ErrorContains(t, references,
						"missing required input \"expected-uid\" on apps.rollout-restart")
				} else {
					require.ErrorContains(t, references, "type mismatch: expected string, got boolean")
				}
				return
			}
			require.NoError(t, references)
			if strings.Contains(fixture, "invalid") {
				require.ErrorContains(t, constraints, "action.restart")
				require.ErrorContains(t, constraints, "constraints[")
			} else {
				require.NoError(t, constraints)
			}
		})
	}
}

func TestRolloutRestartAPIGate(t *testing.T) {
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

func restartExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	body := restartSyntax(t, fixture)
	libs := map[string]*runtime.Library{"apps": lib}
	store, err := local.NewStore(dir, "restart", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "restart", Version: "v0", ContentRevision: "c0"}}
}

func restartSavedApply(t *testing.T, e *runtime.Executor, p *runtime.Plan) error {
	t.Helper()
	body, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), saved)
	return err
}

func restartDecision(t *testing.T, p *runtime.Plan, want runtime.Decision) {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "action.restart" {
			require.Equal(t, want, step.Decision)
			return
		}
	}
	t.Fatal("missing restart action")
}

type restartServer struct {
	mu    sync.Mutex
	obj   *appsv1.Deployment
	calls []string
	url   string
}

func newRestartServer(t *testing.T) *restartServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &restartServer{obj: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "example", Namespace: "default", UID: "uid", ResourceVersion: "rv1", Generation: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.RequestURI())
		require.Equal(t, "/apis/apps/v1/namespaces/default/deployments/example", r.URL.Path)
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
			api.obj.Generation++
		}
		require.NoError(t, json.NewEncoder(w).Encode(api.obj))
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func (a *restartServer) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func restartInputs(server string) map[string]any {
	return map[string]any{
		"server": server, "kind": "Deployment", "name": "example", "namespace": nil,
		"expected-uid": "uid", "restart-at": "2026-11-26T12:00:00Z", "timeout": nil}
}

func TestRolloutRestartNativePlansAndTriggers(t *testing.T) {
	for _, trigger := range []string{"default", "explicit", "always"} {
		t.Run(trigger, func(t *testing.T) {
			api := newRestartServer(t)
			lib := restartLibrary(t)
			dir := t.TempDir()
			inputs := restartInputs(api.url)
			fixture := "evaluated.ub"
			if trigger != "default" {
				fixture = "explicit.ub"
				inputs["trigger"] = "stable"
				if trigger == "always" {
					inputs["trigger"] = "always"
				}
			}
			executor := func() *runtime.Executor { return restartExecutor(t, lib, dir, fixture, inputs) }
			e := executor()
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			restartDecision(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			require.NoError(t, restartSavedApply(t, e, p))
			get := "GET /apis/apps/v1/namespaces/default/deployments/example"
			patch := "PATCH /apis/apps/v1/namespaces/default/deployments/example"
			require.Equal(t, []string{get, patch}, api.take())
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			want := runtime.DecisionSkip
			if trigger == "always" {
				want = runtime.DecisionRerun
			}
			restartDecision(t, p, want)
			require.Empty(t, api.take())
			require.NoError(t, restartSavedApply(t, e, p))
			if trigger == "always" {
				require.Equal(t, []string{get}, api.take())
			} else {
				require.Empty(t, api.take())
			}
			inputs["restart-at"] = "2026-11-27T12:00:00Z"
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger == "explicit" {
				restartDecision(t, p, runtime.DecisionSkip)
			} else {
				restartDecision(t, p, runtime.DecisionRerun)
			}
			require.NoError(t, restartSavedApply(t, e, p))
			if trigger == "explicit" {
				require.Empty(t, api.take())
			} else {
				require.Equal(t, []string{get, patch}, api.take())
			}
			e = executor()
			e.Destroy = true
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, restartSavedApply(t, e, p))
			require.Empty(t, api.take())
		})
	}
}

func TestRolloutRestartNativeInvalidKnownAndSavedSkip(t *testing.T) {
	api := newRestartServer(t)
	lib := restartLibrary(t)
	dir := t.TempDir()
	valid := restartInputs(api.url)
	valid["trigger"] = "stable"
	e := restartExecutor(t, lib, dir, "explicit.ub", valid)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	api.take()
	for _, tc := range []struct {
		field string
		value any
		rule  string
	}{
		{"kind", "deployment", "constraints[0]"}, {"name", "BAD", "constraints[1]"},
		{"name", strings.Repeat("a", 254), "constraints[1]"},
		{"namespace", "bad.namespace", "constraints[2]"},
		{"namespace", strings.Repeat("a", 64), "constraints[2]"},
		{"expected-uid", "", "constraints[3]"}, {"restart-at", "yesterday", "constraints[4]"},
		{"timeout", "0s", "constraints[5]"}, {"timeout", "bad", "constraints[5]"},
	} {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			inputs := maps.Clone(valid)
			inputs[tc.field] = tc.value
			bad := restartExecutor(t, lib, dir, "explicit.ub", inputs)
			_, err := bad.Plan(t.Context())
			require.ErrorContains(t, err, "action.restart: schema: "+tc.rule)
			require.Empty(t, api.take())
			good := restartExecutor(t, lib, dir, "explicit.ub", valid)
			saved, err := good.Plan(t.Context())
			require.NoError(t, err)
			restartDecision(t, saved, runtime.DecisionSkip)
			err = restartSavedApply(t, bad, saved)
			require.ErrorContains(t, err, "action.restart: schema: "+tc.rule)
			require.Empty(t, api.take())
		})
	}
}

type restartSeed struct{ Request string }
type restartSeedOutput struct{ Value string }

func (s *restartSeed) Run(context.Context, *config.Configuration) (*restartSeedOutput, error) {
	return &restartSeedOutput{Value: s.Request}, nil
}

func TestRolloutRestartNativeDeferred(t *testing.T) {
	for _, value := range []string{"Deployment", "BAD"} {
		t.Run(value, func(t *testing.T) {
			api := newRestartServer(t)
			lib := restartLibrary(t)
			lib.Actions["seed"] = runtime.MakeAction[restartSeed, *restartSeedOutput,
				*config.Configuration]()
			inputs := restartInputs(api.url)
			inputs["kind"] = value
			e := restartExecutor(t, lib, t.TempDir(), "deferred.ub", inputs)
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.take())
			err = restartSavedApply(t, e, p)
			if value == "BAD" {
				require.ErrorContains(t, err, "action.restart: schema: constraints[0]")
				require.Empty(t, api.take())
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"GET /apis/apps/v1/namespaces/default/deployments/example",
					"PATCH /apis/apps/v1/namespaces/default/deployments/example"}, api.take())
			}
		})
	}
}

func TestRolloutRestartNativeSavedRerun(t *testing.T) {
	api := newRestartServer(t)
	lib := restartLibrary(t)
	dir := t.TempDir()
	valid := restartInputs(api.url)
	good := restartExecutor(t, lib, dir, "evaluated.ub", valid)
	plan, err := good.Plan(t.Context())
	require.NoError(t, err)
	restartDecision(t, plan, runtime.DecisionRerun)
	badInputs := maps.Clone(valid)
	badInputs["restart-at"] = "invalid"
	bad := restartExecutor(t, lib, dir, "evaluated.ub", badInputs)
	err = restartSavedApply(t, bad, plan)
	require.ErrorContains(t, err, "action.restart: schema: constraints[4]")
	require.Empty(t, api.take())
	require.NoError(t, restartSavedApply(t, good, plan))
	require.Equal(t, []string{"GET /apis/apps/v1/namespaces/default/deployments/example",
		"PATCH /apis/apps/v1/namespaces/default/deployments/example"}, api.take())
}
