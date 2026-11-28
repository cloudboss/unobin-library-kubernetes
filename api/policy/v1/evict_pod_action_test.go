package v1

import (
	"context"
	"encoding/json"
	"fmt"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

var evictionSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
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
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root},
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module))})
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("schema extraction: %v", warnings)
	}
	return schema, nil
})

func evictionLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := evictionSchema()
	require.NoError(t, err)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.evict-pod": schema.Actions["evict-pod"].Constraints}
	return lib
}

func evictionSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/evict-pod", fixture, "factory.ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func TestEvictPodPublicCompiler(t *testing.T) {
	lib := evictionLibrary(t)
	entries, err := os.ReadDir("testdata/ub/evict-pod")
	require.NoError(t, err)
	for _, entry := range entries {
		fixture := entry.Name()
		if fixture == "deferred" || fixture == "removed" || fixture == "partial" {
			continue
		}
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(evictionSyntax(t, fixture),
				map[string]*runtime.Library{"policy": lib}, nil, "")
			references := checker.References(nil).Err()
			constraints := checker.LiteralConstraints().Err()
			if strings.HasPrefix(fixture, "required-") {
				require.ErrorContains(t, references, "missing required input")
				require.ErrorContains(t, references, "policy.evict-pod")
			} else if strings.HasPrefix(fixture, "type-") {
				require.ErrorContains(t, references, "type mismatch")
			} else {
				require.NoError(t, references)
				if strings.Contains(fixture, "invalid") {
					require.ErrorContains(t, constraints, "action.evict")
					require.ErrorContains(t, constraints, "constraints[")
				} else {
					require.NoError(t, constraints)
				}
			}
		})
	}
}

func TestEvictPodAPIGate(t *testing.T) {
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

func evictionExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	body := evictionSyntax(t, fixture)
	libs := map[string]*runtime.Library{"policy": lib}
	store, err := local.NewStore(dir, "eviction", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "eviction", Version: "v0", ContentRevision: "c0"}}
}

func evictionSavedApply(t *testing.T, e *runtime.Executor, p *runtime.Plan) *runtime.ExecResult {
	t.Helper()
	result, err := evictionApply(t, e, p)
	require.NoError(t, err)
	return result
}

func evictionApply(
	t *testing.T, e *runtime.Executor, p *runtime.Plan,
) (*runtime.ExecResult, error) {
	t.Helper()
	body, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return e.ApplyPlan(t.Context(), saved)
}

func evictionDecision(t *testing.T, p *runtime.Plan, want runtime.Decision) {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "action.evict" {
			require.Equal(t, want, step.Decision)
			return
		}
	}
	t.Fatal("missing eviction action")
}

type evictionAPIServer struct {
	mu     sync.Mutex
	calls  []string
	url    string
	absent bool
}

func newEvictionAPIServer(t *testing.T) *evictionAPIServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &evictionAPIServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && api.absent {
			w.WriteHeader(404)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404}))
		} else if r.Method == "GET" {
			require.NoError(t, json.NewEncoder(w).Encode(corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "example", Namespace: "default", UID: "opaque"}}))
		} else {
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "/api/v1/namespaces/default/pods/example/eviction", r.URL.Path)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
		}
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func (a *evictionAPIServer) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func evictionInputs(server string) map[string]any {
	return map[string]any{"server": server, "name": "example", "namespace": nil,
		"expected-uid": "opaque", "grace-period-seconds": nil, "timeout": nil}
}

func TestEvictPodNativePlansAndTriggers(t *testing.T) {
	get := "GET /api/v1/namespaces/default/pods/example"
	post := "POST /api/v1/namespaces/default/pods/example/eviction"
	for _, trigger := range []string{"default", "explicit", "always"} {
		t.Run(trigger, func(t *testing.T) {
			api := newEvictionAPIServer(t)
			lib := evictionLibrary(t)
			dir := t.TempDir()
			inputs := evictionInputs(api.url)
			fixture := "evaluated"
			if trigger != "default" {
				fixture = "explicit"
				inputs["trigger"] = "stable"
				if trigger == "always" {
					inputs["trigger"] = "always"
				}
			}
			executor := func() *runtime.Executor { return evictionExecutor(t, lib, dir, fixture, inputs) }
			e := executor()
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			evictionDecision(t, p, runtime.DecisionRerun)
			require.Empty(t, api.take())
			first := evictionSavedApply(t, e, p)
			require.Equal(t, []string{get, post}, api.take())
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			want := runtime.DecisionSkip
			if trigger == "always" {
				want = runtime.DecisionRerun
			}
			evictionDecision(t, p, want)
			require.Empty(t, api.take())
			unchanged := evictionSavedApply(t, e, p)
			require.Equal(t, first.Outputs, unchanged.Outputs)
			if trigger == "always" {
				require.Equal(t, []string{get, post}, api.take())
			} else {
				require.Empty(t, api.take())
			}
			inputs["grace-period-seconds"] = int64(0)
			e = executor()
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			if trigger == "explicit" {
				evictionDecision(t, p, runtime.DecisionSkip)
			} else {
				evictionDecision(t, p, runtime.DecisionRerun)
			}
			evictionSavedApply(t, e, p)
			if trigger == "explicit" {
				require.Empty(t, api.take())
				inputs["trigger"] = "changed"
				e = executor()
				p, err = e.Plan(t.Context())
				require.NoError(t, err)
				evictionDecision(t, p, runtime.DecisionRerun)
				evictionSavedApply(t, e, p)
			}
			require.Equal(t, []string{get, post}, api.take())
			e = executor()
			e.Destroy = true
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			evictionSavedApply(t, e, p)
			require.Empty(t, api.take())
		})
	}
}

type evictionInvalidInput struct {
	field string
	value any
	rule  string
}

func invalidEvictionInputs() []evictionInvalidInput {
	return []evictionInvalidInput{
		{"name", "", "constraints[0]"}, {"name", "BAD", "constraints[0]"},
		{"name", "bad/name", "constraints[0]"},
		{"name", strings.Repeat("a", 254), "constraints[0]"},
		{"namespace", "", "constraints[1]"}, {"namespace", "bad.namespace", "constraints[1]"},
		{"namespace", "BAD", "constraints[1]"},
		{"namespace", strings.Repeat("a", 64), "constraints[1]"},
		{"expected-uid", "", "constraints[2]"}, {"grace-period-seconds", int64(-1), "constraints[3]"},
		{"timeout", "", "constraints[4]"}, {"timeout", "bad", "constraints[4]"},
		{"timeout", "0s", "constraints[4]"}, {"timeout", "-1s", "constraints[4]"},
		{"timeout", "999999999999999999h", "constraints[4]"},
	}
}

func TestEvictPodNativeKnownAndSavedPlans(t *testing.T) {
	api := newEvictionAPIServer(t)
	lib := evictionLibrary(t)
	dir := t.TempDir()
	valid := evictionInputs(api.url)
	valid["trigger"] = "stable"
	e := evictionExecutor(t, lib, dir, "explicit", valid)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	evictionSavedApply(t, e, p)
	api.take()
	for _, tc := range invalidEvictionInputs() {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			inputs := maps.Clone(valid)
			inputs[tc.field] = tc.value
			bad := evictionExecutor(t, lib, dir, "explicit", inputs)
			_, err := bad.Plan(t.Context())
			require.ErrorContains(t, err, "action.evict: schema: "+tc.rule)
			require.Empty(t, api.take())
			for _, fixture := range []string{"explicit", "evaluated"} {
				good := evictionExecutor(t, lib, dir, fixture, valid)
				saved, err := good.Plan(t.Context())
				require.NoError(t, err)
				if fixture == "explicit" {
					evictionDecision(t, saved, runtime.DecisionSkip)
				} else {
					evictionDecision(t, saved, runtime.DecisionRerun)
				}
				bad = evictionExecutor(t, lib, dir, fixture, inputs)
				_, err = evictionApply(t, bad, saved)
				require.ErrorContains(t, err, "action.evict: schema: "+tc.rule)
				require.Empty(t, api.take())
			}
		})
	}
	// Teardown uses the saved action identity even if its historical inputs are invalid.
	e = evictionExecutor(t, lib, dir, "explicit", valid)
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("action.evict")
	require.NotNil(t, entry)
	entry.Inputs["name"] = "BAD"
	entry.Inputs["expected-uid"] = ""
	entry.Inputs["timeout"] = "bad"
	revision, err := e.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, e.Store.SetCurrent(revision))
	e.Destroy = true
	e.Inputs = maps.Clone(valid)
	e.Inputs["name"] = "BAD"
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	evictionSavedApply(t, e, p)
	require.Empty(t, api.take())
}

type evictionSeed struct {
	Name               string
	Namespace          *string
	ExpectedUID        string
	GracePeriodSeconds *int64
	Timeout            *string
}

type evictionSeedOutput struct {
	ResolvedName      string
	ResolvedNamespace *string
	ResolvedUID       string
	ResolvedGrace     *int64
	ResolvedTimeout   *string
}

func (s *evictionSeed) Run(context.Context, *config.Configuration) (*evictionSeedOutput, error) {
	return &evictionSeedOutput{ResolvedName: s.Name, ResolvedNamespace: s.Namespace,
		ResolvedUID: s.ExpectedUID, ResolvedGrace: s.GracePeriodSeconds,
		ResolvedTimeout: s.Timeout}, nil
}

func TestEvictPodNativeDeferred(t *testing.T) {
	for _, tc := range append([]evictionInvalidInput{{}}, invalidEvictionInputs()...) {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			api := newEvictionAPIServer(t)
			lib := evictionLibrary(t)
			lib.Actions["seed"] = runtime.MakeAction[evictionSeed, *evictionSeedOutput,
				*config.Configuration]()
			inputs := evictionInputs(api.url)
			if tc.field != "" {
				inputs[tc.field] = tc.value
			}
			e := evictionExecutor(t, lib, t.TempDir(), "deferred", inputs)
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.take())
			for _, step := range p.Steps {
				if step.Address == "action.evict" {
					require.Equal(t, map[string][]string{
						"name":                 {"action.seed.resolved-name"},
						"namespace":            {"action.seed.resolved-namespace"},
						"expected-uid":         {"action.seed.resolved-uid"},
						"grace-period-seconds": {"action.seed.resolved-grace"},
						"timeout":              {"action.seed.resolved-timeout"},
					}, step.UnresolvedInputs)
				}
			}
			_, err = evictionApply(t, e, p)
			if tc.field != "" {
				require.ErrorContains(t, err, "action.evict: schema: "+tc.rule)
				require.Empty(t, api.take())
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"GET /api/v1/namespaces/default/pods/example",
					"POST /api/v1/namespaces/default/pods/example/eviction"}, api.take())
			}
		})
	}
}

func TestEvictPodNativePartiallyKnownAndRemoval(t *testing.T) {
	api := newEvictionAPIServer(t)
	lib := evictionLibrary(t)
	lib.Actions["seed"] = runtime.MakeAction[evictionSeed, *evictionSeedOutput,
		*config.Configuration]()
	inputs := evictionInputs(api.url)
	inputs["name"] = "BAD"
	e := evictionExecutor(t, lib, t.TempDir(), "partial", inputs)
	_, err := e.Plan(t.Context())
	require.ErrorContains(t, err, "action.evict: schema: constraints[0]")
	require.Empty(t, api.take())
	dir := t.TempDir()
	inputs = evictionInputs(api.url)
	e = evictionExecutor(t, lib, dir, "evaluated", inputs)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	applied := evictionSavedApply(t, e, p)
	require.Equal(t, map[string]any{"accepted": true, "deleted": false, "waited": false,
		"uid": "opaque"}, applied.Outputs)
	api.take()
	e = evictionExecutor(t, lib, dir, "removed", nil)
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	evictionSavedApply(t, e, p)
	require.Empty(t, api.take())
}

func TestEvictPodNativeDefaults(t *testing.T) {
	api := newEvictionAPIServer(t)
	api.absent = true
	lib := evictionLibrary(t)
	e := evictionExecutor(t, lib, t.TempDir(), "defaults", map[string]any{"server": api.url})
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	result := evictionSavedApply(t, e, p)
	require.Equal(t, map[string]any{"accepted": false, "deleted": true, "waited": true,
		"uid": "opaque"}, result.Outputs)
	require.Equal(t, []string{"GET /api/v1/namespaces/default/pods/example"}, api.take())
}
