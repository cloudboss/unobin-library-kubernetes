package v1

import (
	"context"
	"encoding/json"
	"io"
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

const nodeLookupGET = "GET /api/v1/nodes/worker.example"

func nodeLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: unobinSource(t)})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	typ := *schema.DataSources["node"]
	typ.EvaluatedInputs = typ.Inputs
	schema.DataSources["node"] = &typ
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.node": schema.DataSources["node"].Constraints,
	}
	return lib
}

func nodeLookupSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/node", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func nodeLookupExecutor(
	t *testing.T, lib *runtime.Library, fixture string, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body := nodeLookupSyntax(t, fixture)
	libs := map[string]*runtime.Library{"core": lib}
	if fixture == "config-deferred" {
		libs["seed"] = &runtime.Library{Actions: lib.Actions}
	}
	store, err := local.NewStore(coreCompileDirectory(t), "node-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body, Libraries: libs,
		Store: store, Inputs: inputs,
		Factory: state.FactoryInfo{Name: "node-lookup", Version: "v0", ContentRevision: "c0"},
	}
}

func TestNodeLookupCompiledInputs(t *testing.T) {
	lib := nodeLookupLibrary(t)
	for _, fixture := range []string{
		"valid", "valid-max", "invalid-empty", "invalid-uppercase", "invalid-underscore",
		"invalid-dots", "invalid-long", "schema-missing", "schema-null", "schema-extra", "schema-spec",
		"schema-no-metadata", "schema-null-metadata", "schema-name-type",
	} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(nodeLookupSyntax(t, fixture),
				map[string]*runtime.Library{"core": lib}, nil, "")
			if strings.HasPrefix(fixture, "schema-") {
				require.Error(t, checker.References(nil).Err())
				return
			}
			require.NoError(t, checker.References(nil).Err())
			err := checker.LiteralConstraints().Err()
			if strings.HasPrefix(fixture, "invalid-") {
				require.ErrorContains(t, err, "data-source.subject")
				require.ErrorContains(t, err, "constraints[0]")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNodeLookupAPIGate(t *testing.T) {
	descriptor := libraryapi.Descriptor{FormatVersion: 1, ImplementedAPIs: []string{"1.0"},
		GeneratorAPI: "1.0"}
	compatibility, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &descriptor,
	})
	require.NoError(t, err)
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(compatibility,
		func(string) (*runtime.LibrarySchema, []string, error) {
			t.Error("schema extraction ran before API compatibility check")
			return nil, nil, nil
		}, nil)
	_, _, err = cache.Read(".")
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
}

func TestNodeLookupFactoryCompile(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	for _, fixture := range []string{"valid", "invalid-empty", "schema-extra"} {
		t.Run(fixture, func(t *testing.T) {
			err := compile.Run(compile.Options{
				FactoryPath: filepath.Join("testdata/ub/node", fixture),
				OutDir:      coreCompileDirectory(t), StackName: "node-lookup",
				GoVersion: "1.26.2", CLIVersion: "v0.13.1",
				ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
				Stdout:           io.Discard, Stderr: io.Discard,
			})
			switch fixture {
			case "invalid-empty":
				require.ErrorContains(t, err, "data-source.subject")
				require.ErrorContains(t, err, "constraints[0]")
			case "schema-extra":
				require.ErrorContains(t, err, "namespace")
			default:
				require.NoError(t, err)
			}
		})
	}
}

type nodeLookupServer struct {
	url      string
	mu       sync.Mutex
	object   *corev1.Node
	requests []string
}

func newNodeLookupServer(t *testing.T) *nodeLookupServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(coreCompileDirectory(t), "missing"))
	s := &nodeLookupServer{object: &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker.example", UID: "uid", ResourceVersion: "1",
	}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		var value any = s.object
		if s.object == nil {
			w.WriteHeader(http.StatusNotFound)
			value = &metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404}
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	s.url = server.URL
	return s
}

func (s *nodeLookupServer) assertRequests(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, want, s.requests)
	s.requests = nil
}

func nodeLookupInputs(server string, metadata any) map[string]any {
	return map[string]any{"server": server, "metadata": metadata}
}

func nodeLookupStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "data-source.subject" {
			require.Equal(t, runtime.DecisionRead, step.Decision)
			return step
		}
	}
	t.Fatal("Node read step is missing")
	return nil
}

func TestNodeLookupEvaluatedAndSavedInputs(t *testing.T) {
	lib := nodeLookupLibrary(t)
	s := newNodeLookupServer(t)
	for _, tc := range []struct {
		name     string
		metadata any
		error    string
		path     string
	}{
		{"valid", map[string]any{"name": "worker.example"}, "", nodeLookupGET},
		{"max", map[string]any{"name": strings.Repeat("a", 253)}, "",
			"GET /api/v1/nodes/" + strings.Repeat("a", 253)},
		{"missing metadata", nil, `field "metadata": required but is null`, ""},
		{"name type", map[string]any{"name": true}, `field "metadata.name": expected string`, ""},
		{"missing name", map[string]any{}, `field "metadata.name": required but not provided`, ""},
		{"null", map[string]any{"name": nil}, `field "metadata.name": required but is null`, ""},
		{"empty", map[string]any{"name": ""}, "constraints[0]", ""},
		{"uppercase", map[string]any{"name": "Worker"}, "constraints[0]", ""},
		{"underscore", map[string]any{"name": "bad_name"}, "constraints[0]", ""},
		{"dots", map[string]any{"name": "worker..example"}, "constraints[0]", ""},
		{"long", map[string]any{"name": strings.Repeat("a", 254)}, "constraints[0]", ""},
		{"extra", map[string]any{"name": "worker", "namespace": "ns"},
			`unknown field "metadata.namespace"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := nodeLookupExecutor(t, lib, "runtime", nodeLookupInputs(s.url, tc.metadata))
			plan, err := e.Plan(t.Context())
			if tc.error == "" {
				require.NoError(t, err)
				require.NotNil(t, nodeLookupStep(t, plan).ObservedOutputs)
				s.assertRequests(t, tc.path)
				_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
				require.NoError(t, err)
				s.assertRequests(t, tc.path)
				return
			}
			require.ErrorContains(t, err, "data-source.subject")
			require.ErrorContains(t, err, tc.error)
			s.assertRequests(t)
			e.Inputs = nodeLookupInputs(s.url, map[string]any{"name": "worker.example"})
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			s.assertRequests(t, nodeLookupGET)
			plan.Inputs["metadata"] = tc.metadata
			e.Inputs = nil
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.ErrorContains(t, err, tc.error)
			s.assertRequests(t)
		})
	}
}

type nodeLookupSeed struct{ Value any }
type nodeLookupSeedOutput struct{ Result any }

func (s *nodeLookupSeed) Run(context.Context, any) (*nodeLookupSeedOutput, error) {
	return &nodeLookupSeedOutput{Result: s.Value}, nil
}

func TestNodeLookupDeferredInputs(t *testing.T) {
	lib := nodeLookupLibrary(t)
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[nodeLookupSeed, *nodeLookupSeedOutput, any](),
	}
	s := newNodeLookupServer(t)
	for _, tc := range []struct {
		name     string
		metadata any
		error    string
	}{
		{"valid", map[string]any{"name": "worker.example"}, ""},
		{"invalid", map[string]any{"name": "Bad_Name"}, "constraints[0]"},
		{"null name", map[string]any{"name": nil}, `field "metadata.name": required but is null`},
		{"null metadata", nil, `field "metadata": required but is null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := nodeLookupExecutor(t, lib, "deferred", nodeLookupInputs(s.url, tc.metadata))
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			step := nodeLookupStep(t, plan)
			require.Nil(t, step.ObservedOutputs)
			require.Contains(t, step.UnresolvedInputs, "metadata")
			s.assertRequests(t)
			e.Inputs = nil
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			if tc.error != "" {
				require.ErrorContains(t, err, tc.error)
				s.assertRequests(t)
			} else {
				require.NoError(t, err)
				s.assertRequests(t, nodeLookupGET)
			}
		})
	}
	e := nodeLookupExecutor(t, lib, "config-deferred", map[string]any{"server": s.url})
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Nil(t, nodeLookupStep(t, plan).ObservedOutputs)
	s.assertRequests(t)
	e.Inputs = nil
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
	require.NoError(t, err)
	s.assertRequests(t, nodeLookupGET)
}

func TestNodeLookupSteadyInputsAndHistoricalDestroy(t *testing.T) {
	lib := nodeLookupLibrary(t)
	lib.Resources["stable"] = runtime.MakeResource[
		configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
	]{SchemaVersion: 1})
	s := newNodeLookupServer(t)
	e := nodeLookupExecutor(t, lib, "steady",
		nodeLookupInputs(s.url, map[string]any{"name": "worker.example"}))
	initial, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Nil(t, nodeLookupStep(t, initial).ObservedOutputs)
	s.assertRequests(t)
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, initial))
	require.NoError(t, err)
	s.assertRequests(t, nodeLookupGET)
	steady, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NotNil(t, nodeLookupStep(t, steady).ObservedOutputs)
	for _, step := range steady.Steps {
		if step.Address == "resource.stable" {
			require.Equal(t, runtime.DecisionNoOp, step.Decision)
		}
	}
	s.assertRequests(t, nodeLookupGET)
	e.Inputs["metadata"] = map[string]any{"name": "Bad_Name"}
	_, err = e.Plan(t.Context())
	require.ErrorContains(t, err, "constraints[0]")
	s.assertRequests(t)
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	snapshot.Find("data-source.subject").Inputs["metadata"] = e.Inputs["metadata"]
	revision, err := e.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, e.Store.SetCurrent(revision))
	e.Destroy = true
	destroy, err := e.Plan(t.Context())
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, destroy))
	require.NoError(t, err)
	s.assertRequests(t)
}

func TestNodeLookupSavedObservationsAndRemoval(t *testing.T) {
	lib := nodeLookupLibrary(t)
	for _, change := range []string{
		"stable", "uid", "version", "metadata", "spec", "status", "disappearance", "appearance", "remove",
	} {
		t.Run(change, func(t *testing.T) {
			s := newNodeLookupServer(t)
			if change == "appearance" {
				s.object = nil
			}
			e := nodeLookupExecutor(t, lib, "runtime",
				nodeLookupInputs(s.url, map[string]any{"name": "worker.example"}))
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			s.assertRequests(t, nodeLookupGET)
			s.mu.Lock()
			switch change {
			case "uid":
				s.object.UID = "replacement"
			case "version":
				s.object.ResourceVersion = "2"
			case "metadata":
				s.object.Annotations = map[string]string{"controller": "changed"}
			case "spec":
				s.object.Spec.Unschedulable = true
			case "status":
				s.object.Status.Phase = "Future"
			case "disappearance":
				s.object = nil
			case "appearance":
				s.object = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker.example", UID: "new"}}
			}
			s.mu.Unlock()
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			s.assertRequests(t, nodeLookupGET)
			if change != "stable" && change != "remove" {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
				fresh, err := e.Plan(t.Context())
				require.NoError(t, err)
				if change == "uid" {
					metadata := nodeLookupStep(t, fresh).ObservedOutputs["metadata"].(map[string]any)
					require.Equal(t, "replacement", metadata["uid"])
				}
				s.assertRequests(t, nodeLookupGET)
				return
			}
			require.NoError(t, err)
			if change == "remove" {
				removed := nodeLookupExecutor(t, lib, "removed", nil)
				removed.Store = e.Store
				plan, err = removed.Plan(t.Context())
				require.NoError(t, err)
				_, err = removed.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
				require.NoError(t, err)
				s.assertRequests(t)
			}
		})
	}
}
