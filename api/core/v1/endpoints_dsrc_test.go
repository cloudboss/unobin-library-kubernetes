package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

//nolint:staticcheck // Test responses use the core/v1 Endpoints API.
type (
	kubeEndpoints       = corev1.Endpoints
	kubeEndpointSubset  = corev1.EndpointSubset
	kubeEndpointAddress = corev1.EndpointAddress
	kubeEndpointPort    = corev1.EndpointPort
)

const (
	endpointsGet          = "GET /api/v1/namespaces/default/endpoints/lookup.test"
	endpointsNameRule     = "constraints[0] (predicate)"
	endpointsPrefixRule   = "constraints[1] (predicate)"
	endpointsLabelKeyRule = "constraints[2] (predicate)"
	endpointsLabelValRule = "constraints[3] (predicate)"
	endpointsAnnRule      = "constraints[4] (predicate)"
	endpointsPortError    = `field "subset[0].port[0].port": expected integer`
)

func TestEndpointsLookupCompiledInputs(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		path   string
		api    *libraryapi.Descriptor
		errors []string
	}{
		{name: "valid", path: "valid"},
		{name: "opaque port expression", path: "resolved-port"},
		{name: "incompatible", path: "valid", api: &libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		}, errors: []string{"required library API 1.1 is newer than implemented API 1.0"}},
		{name: "literal rules", path: "invalid-literals", errors: []string{
			"schema: data-source.empty-name: constraints[0]",
			"schema: data-source.long-name: constraints[0]",
			"schema: data-source.uppercase-name: constraints[0]",
			"schema: data-source.empty-prefix: constraints[1]",
			"schema: data-source.invalid-prefix: constraints[1]",
			"schema: data-source.invalid-label-key: constraints[2]",
			"schema: data-source.invalid-label-value: constraints[3]",
			"schema: data-source.invalid-annotation: constraints[4]",
		}},
		{name: "structural", path: "invalid-inputs", errors: []string{
			`missing required input "metadata" on core.endpoints`,
			":7:17: type: type mismatch: expected object",
			":10:17: type: type mismatch: expected object",
			":14:17: type: type mismatch: expected optional(list",
			":18:18: type: type mismatch: expected object",
			":22:30: type: type mismatch: expected object",
			":26:27: type: type mismatch: expected object",
			`missing required field "ip"`, `missing required field "port"`,
			"type mismatch: expected integer, got number",
			"type mismatch: expected integer, got string",
			"type mismatch: expected integer, got null",
			"type mismatch: expected string, got null",
			`unknown field "uid"`, `unknown field "resource-version"`,
			`unknown field "generateName"`, `unknown field "nodeName"`,
			`unknown field "target-ref"`, `unknown field "status" on core.endpoints`,
		}},
		{name: "excluded output", path: "invalid-outputs", errors: []string{
			`unknown field "id" on core.endpoints`,
			`unknown field "target-ref" on object`,
			`unknown field "app-protocol" on object`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log bytes.Buffer
			_, err := compile.RunResult(compile.Options{
				FactoryPath: filepath.Join("testdata/ub/endpoints", tc.path, "factory.ub"),
				OutDir:      coreCompileDirectory(t), StackName: "endpoints-lookup",
				Version: "v0.0.0", CLIVersion: "v0.13.1", GoVersion: "1.26.2",
				LibraryAPIDescriptor: tc.api,
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				},
				Stdout: &log, Stderr: &log,
			})
			if tc.errors == nil {
				require.NoError(t, err, "%s", log.String())
				return
			}
			for _, message := range tc.errors {
				require.ErrorContains(t, err, message)
			}
		})
	}
}

func endpointsLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: unobinSource(t),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	typ := *schema.DataSources["endpoints"]
	typ.EvaluatedInputs = typ.Inputs
	schema.DataSources["endpoints"] = &typ
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.endpoints": typ.Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.endpoints": typ.Defaults,
	}
	return lib
}

func endpointsExecutor(
	t *testing.T, lib *runtime.Library, fixture string, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub/endpoints", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": lib}
	store, err := local.NewStore(coreCompileDirectory(t), "endpoints-lookup", "test",
		encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store, Inputs: inputs,
		Factory: state.FactoryInfo{Name: "endpoints-lookup", Version: "v0", ContentRevision: "c0"},
	}
}

type endpointsLookupServer struct {
	url      string
	mu       sync.Mutex
	object   *kubeEndpoints
	requests []string
}

func newEndpointsLookupServer(t *testing.T, obj *kubeEndpoints) *endpointsLookupServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(coreCompileDirectory(t), "missing"))
	s := &endpointsLookupServer{object: obj}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if s.object == nil {
			w.WriteHeader(http.StatusNotFound)
			err := json.NewEncoder(w).Encode(&metav1.Status{
				Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
			})
			if err != nil {
				t.Error(err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(s.object); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	s.url = server.URL
	return s
}

func (s *endpointsLookupServer) assertRequests(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, want, s.requests)
	s.requests = nil
}

func endpointsReadStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "data-source.endpoint" {
			require.Equal(t, runtime.DecisionRead, step.Decision)
			return step
		}
	}
	t.Fatal("no read step for data-source.endpoint")
	return nil
}

func endpointsConstraint(t *testing.T, err error, rule string) {
	t.Helper()
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
	require.ErrorContains(t, err, rule)
}

func TestEndpointsLookupSteadyAndSavedNativeInputs(t *testing.T) {
	lib := endpointsLookupLibrary(t)
	lib.Resources["stable"] = runtime.MakeResource[
		configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
	]{SchemaVersion: 1})
	s := newEndpointsLookupServer(t, &kubeEndpoints{
		ObjectMeta: metav1.ObjectMeta{Name: "lookup.test", Namespace: "default", UID: "live"},
	})
	e := endpointsExecutor(t, lib, "steady", map[string]any{
		"server": s.url, "metadata": map[string]any{"name": "lookup.test"}, "port": nil,
	})
	initial, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Nil(t, endpointsReadStep(t, initial).ObservedOutputs)
	s.assertRequests(t)
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, initial))
	require.NoError(t, err)
	s.assertRequests(t, endpointsGet)
	steady, err := e.Plan(t.Context())
	require.NoError(t, err)
	s.assertRequests(t, endpointsGet)
	require.NotNil(t, endpointsReadStep(t, steady).ObservedOutputs)
	var resourceDecisions []runtime.Decision
	for _, step := range steady.Steps {
		if step.Address == "resource.stable" {
			resourceDecisions = append(resourceDecisions, step.Decision)
		}
	}
	require.Equal(t, []runtime.Decision{runtime.DecisionNoOp}, resourceDecisions)

	for _, port := range []float64{1.75, 1 << 63} {
		e.Inputs = map[string]any{
			"server": s.url, "metadata": map[string]any{"name": "lookup.test"}, "port": port,
		}
		_, err := e.Plan(t.Context())
		require.ErrorContains(t, err, "data-source.endpoint")
		require.ErrorContains(t, err, endpointsPortError)
		s.assertRequests(t)

		e.Inputs["port"] = nil
		plan, err := e.Plan(t.Context())
		require.NoError(t, err)
		s.assertRequests(t, endpointsGet)
		plan.Inputs["port"] = port
		e.Inputs = nil
		_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
		require.ErrorContains(t, err, "data-source.endpoint")
		require.ErrorContains(t, err, endpointsPortError)
		s.assertRequests(t)
	}

	key := strings.Repeat("x", 63)
	dns := strings.Repeat("a", 253)
	for _, tc := range []struct {
		name  string
		field string
		value any
		rule  string
		path  string
	}{
		{name: "null namespace", field: "namespace", path: endpointsGet},
		{name: "empty namespace", field: "namespace", value: "",
			path: "GET /api/v1/endpoints/lookup.test"},
		{name: "DNS boundary", field: "name", value: dns,
			path: "GET /api/v1/namespaces/default/endpoints/" + dns},
		{name: "regular prefix boundary", field: "generate-name", value: key, path: endpointsGet},
		{name: "masked prefix boundary", field: "generate-name", value: key + "-", path: endpointsGet},
		{name: "valid maps", field: "labels", value: map[string]any{
			dns + "/" + key: key, "example.com/empty": "", "bad key": nil,
		}, path: endpointsGet},
		{name: "valid annotations", field: "annotations", value: map[string]any{
			strings.ToUpper(dns) + "/" + key: "arbitrary value",
			dns + "/" + key:                  "lowercase equivalent", "bad key": nil,
		}, path: endpointsGet},
		{name: "uppercase name", field: "name", value: "Lookup", rule: endpointsNameRule},
		{name: "oversized name", field: "name", value: dns + "a", rule: endpointsNameRule},
		{name: "unmasked prefix", field: "generate-name", value: "x/y-", rule: endpointsPrefixRule},
		{name: "oversized prefix", field: "generate-name", value: key + "x-", rule: endpointsPrefixRule},
		{name: "oversized label key", field: "labels",
			value: map[string]any{key + "x": ""}, rule: endpointsLabelKeyRule},
		{name: "oversized label prefix", field: "labels",
			value: map[string]any{dns + "x/" + key: ""}, rule: endpointsLabelKeyRule},
		{name: "oversized label value", field: "labels",
			value: map[string]any{key: key + "x"}, rule: endpointsLabelValRule},
		{name: "oversized annotation key", field: "annotations",
			value: map[string]any{key + "x": ""}, rule: endpointsAnnRule},
		{name: "oversized annotation prefix", field: "annotations",
			value: map[string]any{dns + "x/" + key: ""}, rule: endpointsAnnRule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{"name": "lookup.test", tc.field: tc.value}
			e.Inputs = map[string]any{"server": s.url, "metadata": metadata, "port": nil}
			plan, err := e.Plan(t.Context())
			if tc.rule == "" {
				require.NoError(t, err)
				require.NotNil(t, endpointsReadStep(t, plan).ObservedOutputs)
				s.assertRequests(t, tc.path)
				return
			}
			endpointsConstraint(t, err, tc.rule)
			s.assertRequests(t)
			e.Inputs = map[string]any{
				"server": s.url, "metadata": map[string]any{"name": "lookup.test"}, "port": nil,
			}
			saved, err := e.Plan(t.Context())
			require.NoError(t, err)
			s.assertRequests(t, endpointsGet)
			e.Inputs = nil
			saved.Inputs["metadata"] = metadata
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, saved))
			endpointsConstraint(t, err, tc.rule)
			s.assertRequests(t)
		})
	}
	for _, tc := range []struct {
		name     string
		metadata map[string]any
	}{
		{name: "omitted", metadata: map[string]any{}},
		{name: "null", metadata: map[string]any{"name": nil, "generate-name": nil}},
		{name: "prefix only", metadata: map[string]any{"generate-name": "lookup-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.Inputs = map[string]any{"server": s.url, "metadata": tc.metadata, "port": nil}
			_, err := e.Plan(t.Context())
			require.ErrorContains(t, err, "resource name may not be empty")
			s.assertRequests(t)
		})
	}
	e.Inputs = map[string]any{
		"server": s.url, "metadata": map[string]any{"name": "lookup.test"}, "port": nil,
	}
	saved, err := e.Plan(t.Context())
	require.NoError(t, err)
	s.assertRequests(t, endpointsGet)
	e.Inputs = nil
	_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, saved))
	require.NoError(t, err)
	s.assertRequests(t, endpointsGet)

	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("data-source.endpoint")
	require.NotNil(t, entry)
	entry.Inputs["metadata"] = map[string]any{"name": "Invalid_Name"}
	revision, err := e.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, e.Store.SetCurrent(revision))
	e.Inputs = map[string]any{"server": s.url, "metadata": entry.Inputs["metadata"], "port": nil}
	e.Destroy = true
	destroy, err := e.Plan(t.Context())
	require.NoError(t, err)
	result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, destroy))
	require.NoError(t, err)
	require.Equal(t, map[string]any{}, result.Data)
	s.assertRequests(t)
}

type endpointsLookupSeed struct {
	MetadataInput svc.EndpointsLookupMetadata
	ValueInput    *string
	PortInput     any
	run           func()
}

type endpointsLookupSeedOutput struct {
	Metadata svc.EndpointsLookupMetadata
	Value    *string
	Port     any
}

func (s *endpointsLookupSeed) Run(context.Context, any) (*endpointsLookupSeedOutput, error) {
	s.run()
	return &endpointsLookupSeedOutput{
		Metadata: s.MetadataInput, Value: s.ValueInput, Port: s.PortInput,
	}, nil
}

func TestEndpointsLookupDeferredNativeInputs(t *testing.T) {
	lib := endpointsLookupLibrary(t)
	s := newEndpointsLookupServer(t, nil)
	for _, tc := range []struct {
		name        string
		labels      map[string]any
		rule        string
		port        any
		invalidPort bool
	}{
		{name: "valid", labels: map[string]any{"bad key": nil, "app": ""}},
		{name: "invalid", labels: map[string]any{"bad key": ""}, rule: endpointsLabelKeyRule},
		{name: "fractional port", port: 1.75, invalidPort: true},
		{name: "overflowing port", port: float64(1 << 63), invalidPort: true},
		{name: "maximum port integer", port: int64(math.MaxInt64)},
		{name: "minimum port integer", port: int64(math.MinInt64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			lib.Actions = map[string]runtime.ActionRegistration{
				"seed": runtime.MakeActionWith[endpointsLookupSeed, *endpointsLookupSeedOutput, any](
					func() *endpointsLookupSeed {
						return &endpointsLookupSeed{run: func() { calls++ }}
					},
				),
			}
			e := endpointsExecutor(t, lib, "deferred", map[string]any{
				"server": s.url, "port": tc.port, "metadata": map[string]any{
					"name": "lookup.test", "generate-name": "valid-", "labels": tc.labels,
				},
			})
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			step := endpointsReadStep(t, plan)
			require.Nil(t, step.ObservedOutputs)
			require.Contains(t, step.UnresolvedInputs, "metadata")
			require.Contains(t, step.UnresolvedInputs, "subset")
			require.Zero(t, calls)
			s.assertRequests(t)
			e.Inputs = nil
			result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.Equal(t, 1, calls)
			if tc.rule != "" {
				endpointsConstraint(t, err, tc.rule)
				s.assertRequests(t)
				return
			}
			if tc.invalidPort {
				require.ErrorContains(t, err, "data-source.endpoint")
				require.ErrorContains(t, err, endpointsPortError)
				s.assertRequests(t)
				return
			}
			require.NoError(t, err)
			s.assertRequests(t, endpointsGet)
			if tc.port != nil {
				assertEndpointsLookupPort(t, result.Outputs, tc.port.(int64))
				return
			}
			require.Equal(t, []any{}, result.Outputs["subset"])
			meta := result.Outputs["metadata"].(map[string]any)
			require.Equal(t, map[string]any{"app": ""}, meta["labels"])
		})
	}
}

func TestEndpointsLookupPartiallyKnownRule(t *testing.T) {
	lib := endpointsLookupLibrary(t)
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeActionWith[endpointsLookupSeed, *endpointsLookupSeedOutput, any](
			func() *endpointsLookupSeed {
				return &endpointsLookupSeed{run: func() { t.Error("seed ran during plan") }}
			},
		),
	}
	s := newEndpointsLookupServer(t, nil)
	e := endpointsExecutor(t, lib, "partial", map[string]any{
		"server": s.url, "known": "known-invalid-key-value", "value": "pending",
	})
	_, err := e.Plan(t.Context())
	endpointsConstraint(t, err, endpointsLabelKeyRule)
	s.assertRequests(t)
}

func TestEndpointsLookupSavedObservations(t *testing.T) {
	lib := endpointsLookupLibrary(t)
	for _, change := range []string{"equal membership", "metadata changed", "membership changed"} {
		t.Run(change, func(t *testing.T) {
			first := kubeEndpointSubset{
				Addresses: []kubeEndpointAddress{{IP: "192.0.2.2"}, {IP: "192.0.2.1"}},
				Ports:     []kubeEndpointPort{{Name: "http", Port: 80}},
			}
			second := kubeEndpointSubset{
				NotReadyAddresses: []kubeEndpointAddress{{IP: "192.0.2.1"}},
			}
			s := newEndpointsLookupServer(t, &kubeEndpoints{
				ObjectMeta: metav1.ObjectMeta{Name: "lookup.test", Namespace: "default", UID: "uid"},
				Subsets:    []kubeEndpointSubset{first, second},
			})
			e := endpointsExecutor(t, lib, "steady", map[string]any{
				"server": s.url, "metadata": map[string]any{"name": "lookup.test"}, "port": nil,
			})
			lib.Resources["stable"] = runtime.MakeResource[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			](runtime.ResourceDefinition[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			]{SchemaVersion: 1})
			initial, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, initial))
			require.NoError(t, err)
			s.assertRequests(t, endpointsGet)
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			s.assertRequests(t, endpointsGet)
			var observed svc.EndpointsDataSourceOutput
			require.NoError(t, runtime.Decode(&observed, endpointsReadStep(t, plan).ObservedOutputs))
			want := []svc.EndpointsLookupSubsetOutput{
				{
					Address:         []svc.EndpointsLookupAddressOutput{},
					NotReadyAddress: []svc.EndpointsLookupAddressOutput{{IP: "192.0.2.1"}},
					Port:            []svc.EndpointsLookupPortOutput{},
				},
				{
					Address: []svc.EndpointsLookupAddressOutput{
						{IP: "192.0.2.1"}, {IP: "192.0.2.2"},
					},
					NotReadyAddress: []svc.EndpointsLookupAddressOutput{},
					Port:            []svc.EndpointsLookupPortOutput{{Name: "http", Port: 80}},
				},
			}
			require.Equal(t, want, observed.Subset)

			s.mu.Lock()
			switch change {
			case "equal membership":
				slices.Reverse(first.Addresses)
				first.Addresses = append(first.Addresses, first.Addresses[0])
				first.Ports = append(first.Ports, kubeEndpointPort{
					Name: "http", Port: 80, AppProtocol: new("omitted"),
				})
				s.object.Subsets = []kubeEndpointSubset{second, first, second}
			case "metadata changed":
				s.object.ResourceVersion = "2"
			case "membership changed":
				s.object.Subsets = []kubeEndpointSubset{first}
			}
			s.mu.Unlock()
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			s.assertRequests(t, endpointsGet)
			if change == "equal membership" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "data source data-source.endpoint changed since the plan")
				require.ErrorContains(t, err, "plan again")
			}
		})
	}
}

func TestEndpointsLookupResolvedPorts(t *testing.T) {
	library := endpointsLookupLibrary(t)
	for _, test := range []struct {
		name    string
		port    any
		invalid bool
	}{
		{name: "fractional", port: 1.75, invalid: true},
		{name: "overflowing", port: float64(1 << 63), invalid: true},
		{name: "maximum integer", port: int64(math.MaxInt64)},
		{name: "minimum integer", port: int64(math.MinInt64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newEndpointsLookupServer(t, nil)
			inputs := map[string]any{"server": api.url, "port": test.port}
			executor := endpointsExecutor(t, library, "resolved-port", inputs)
			plan, err := executor.Plan(t.Context())
			if test.invalid {
				require.ErrorContains(t, err, "data-source.endpoint")
				require.ErrorContains(t, err, endpointsPortError)
				api.assertRequests(t)
				return
			}
			require.NoError(t, err)
			api.assertRequests(t, endpointsGet)
			wantPort := test.port.(int64)
			assertEndpointsLookupPort(t, endpointsReadStep(t, plan).ObservedOutputs, wantPort)

			executor.Inputs = nil
			result, err := executor.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.NoError(t, err)
			api.assertRequests(t, endpointsGet)
			assertEndpointsLookupPort(t, result.Outputs, wantPort)
		})
	}
}

func assertEndpointsLookupPort(t *testing.T, outputs map[string]any, port int64) {
	t.Helper()
	require.Equal(t, []any{map[string]any{
		"address": []any{}, "not-ready-address": []any{},
		"port": []any{map[string]any{"name": "", "port": port, "protocol": "TCP"}},
	}}, outputs["subset"])
}
