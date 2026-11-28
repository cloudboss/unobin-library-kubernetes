package v1

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func ingressClassLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := networkPolicyLookupSchema()
	require.NoError(t, err)
	lib := Library()
	copy := *schema
	copy.DataSources = maps.Clone(schema.DataSources)
	typ := *copy.DataSources["ingress-class"]
	typ.EvaluatedInputs = typ.Inputs
	copy.DataSources["ingress-class"] = &typ
	lib.Schema = &copy
	lib.Constraints = map[string][]lang.ConstraintSpec{"data-source.ingress-class": typ.Constraints}
	return lib
}

func ingressClassLookupSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/ingress-class-lookup", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func TestIngressClassLookupPublicCompiler(t *testing.T) {
	lib := ingressClassLookupLibrary(t)
	for _, fixture := range []string{
		"valid", "maximum-valid", "empty", "upper", "underscore", "slash", "dot", "dash", "length",
		"null", "type", "name-required", "metadata-required", "metadata-null", "namespace",
		"generate-name", "labels", "annotations", "spec", "status", "ignore-labels",
		"ignore-annotations", "selector", "status-output", "namespace-output",
	} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(ingressClassLookupSyntax(t, fixture),
				map[string]*runtime.Library{"networking": lib}, nil, "")
			refs, rules := checker.References(nil).Err(), checker.LiteralConstraints().Err()
			switch fixture {
			case "valid", "maximum-valid":
				require.NoError(t, refs)
				require.NoError(t, rules)
			case "empty", "upper", "underscore", "slash", "dot", "dash", "length":
				require.NoError(t, refs)
				require.ErrorContains(t, rules, "data-source.lookup")
				require.ErrorContains(t, rules, "constraints[0]")
			case "null", "metadata-null":
				require.Error(t, refs)
				require.ErrorContains(t, rules, "data-source.lookup")
				require.ErrorContains(t, rules, "constraints[0]")
			default:
				require.Error(t, refs)
			}
		})
	}
}

func ingressClassLookupExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	lib.Resources["anchor"] = runtime.MakeResource[
		networkPolicyLookupAnchor, *networkPolicyLookupAnchorOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		networkPolicyLookupAnchor, *networkPolicyLookupAnchorOutput, *config.Configuration,
	]{SchemaVersion: 1})
	lib.Actions = map[string]runtime.ActionRegistration{"seed": runtime.MakeAction[
		networkPolicyLookupSeed, *networkPolicyLookupSeedOutput, *config.Configuration,
	]()}
	body := ingressClassLookupSyntax(t, fixture)
	libs := map[string]*runtime.Library{"networking": lib, "helper": lib}
	store, err := local.NewStore(dir, "ingress-class-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "ingress-class-lookup", Version: "v0", ContentRevision: "c0"}}
}

type ingressClassLookupServer struct {
	mu    sync.Mutex
	obj   *networkingv1.IngressClass
	calls []string
	url   string
}

func newIngressClassLookupServer(t *testing.T) *ingressClassLookupServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &ingressClassLookupServer{obj: &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "uid"},
		Spec: networkingv1.IngressClassSpec{
			Parameters: &networkingv1.IngressClassParametersReference{},
		},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if api.obj == nil {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Reason: metav1.StatusReasonNotFound, Code: 404,
			}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(api.obj))
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func (s *ingressClassLookupServer) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := slices.Clone(s.calls)
	s.calls = nil
	return calls
}

func TestIngressClassLookupNativeSteadyAndSaved(t *testing.T) {
	api := newIngressClassLookupServer(t)
	lib, dir := ingressClassLookupLibrary(t), t.TempDir()
	inputs := map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}}
	e := ingressClassLookupExecutor(t, lib, dir, "steady", inputs)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
	api.take()
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	decisions := map[string]runtime.Decision{}
	for _, step := range p.Steps {
		if step.Address == "resource.anchor" || step.Address == "data-source.lookup" {
			decisions[step.Address] = step.Decision
		}
	}
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionNoOp, "data-source.lookup": runtime.DecisionRead,
	}, decisions)
	require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
	get := "GET /apis/networking.k8s.io/v1/ingressclasses/example"
	require.Equal(t, []string{get, get}, api.take())
	for _, tc := range []struct {
		metadata any
		message  string
	}{
		{nil, "required but is null"}, {map[string]any{}, "required but not provided"},
		{map[string]any{"name": nil}, "required but is null"},
		{map[string]any{"name": false}, "expected string"},
		{map[string]any{"name": ""}, "constraints[0]"},
		{map[string]any{"name": "BAD"}, "constraints[0]"},
		{map[string]any{"name": "bad_name"}, "constraints[0]"},
		{map[string]any{"name": "bad/name"}, "constraints[0]"},
		{map[string]any{"name": "bad..name"}, "constraints[0]"},
		{map[string]any{"name": strings.Repeat("a", 254)}, "constraints[0]"},
		{map[string]any{"name": "example", "namespace": "default"}, "unknown field"},
	} {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			valid := ingressClassLookupExecutor(t, lib, dir, "steady", inputs)
			saved, err := valid.Plan(t.Context())
			require.NoError(t, err)
			api.take()
			bad := ingressClassLookupExecutor(t, lib, dir, "steady",
				map[string]any{"server": api.url, "metadata": tc.metadata})
			_, err = bad.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.lookup")
			require.ErrorContains(t, err, tc.message)
			require.Empty(t, api.take())
			err = networkPolicyLookupSavedApply(t, bad, saved)
			require.ErrorContains(t, err, "data-source.lookup")
			require.ErrorContains(t, err, tc.message)
			require.Empty(t, api.take())
		})
	}
}

func TestIngressClassLookupNativeDeferred(t *testing.T) {
	for _, tc := range []struct {
		name any
		rule string
	}{
		{"example", ""}, {"example.test", ""}, {strings.Repeat("a", 249) + ".app", ""},
		{"BAD", "constraints[0]"}, {nil, "required but is null"},
		{false, "expected string"}, {"", "constraints[0]"},
	} {
		t.Run(fmt.Sprint(tc.name), func(t *testing.T) {
			api := newIngressClassLookupServer(t)
			e := ingressClassLookupExecutor(t, ingressClassLookupLibrary(t), t.TempDir(), "deferred",
				map[string]any{"server": api.url, "metadata": map[string]any{"name": tc.name}})
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.take())
			err = networkPolicyLookupSavedApply(t, e, p)
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.lookup")
				require.ErrorContains(t, err, tc.rule)
				require.Empty(t, api.take())
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{
					"GET /apis/networking.k8s.io/v1/ingressclasses/" + tc.name.(string),
				}, api.take())
			}
		})
	}
}

func TestIngressClassLookupDeferredConfiguration(t *testing.T) {
	for _, name := range []string{"example", "BAD"} {
		t.Run(name, func(t *testing.T) {
			api := newIngressClassLookupServer(t)
			e := ingressClassLookupExecutor(t, ingressClassLookupLibrary(t), t.TempDir(),
				"deferred-config", map[string]any{
					"server": api.url, "metadata": map[string]any{"name": name},
				})
			p, err := e.Plan(t.Context())
			require.Empty(t, api.take())
			if name == "BAD" {
				require.ErrorContains(t, err, "data-source.lookup: schema: constraints[0]")
				return
			}
			require.NoError(t, err)
			require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
			require.Equal(t, []string{"GET /apis/networking.k8s.io/v1/ingressclasses/example"}, api.take())
		})
	}
}

func TestIngressClassLookupChangingDependency(t *testing.T) {
	api := newIngressClassLookupServer(t)
	lib, dir := ingressClassLookupLibrary(t), t.TempDir()
	for _, name := range []string{"example", "changed"} {
		e := ingressClassLookupExecutor(t, lib, dir, "dependency",
			map[string]any{"server": api.url, "name": name})
		p, err := e.Plan(t.Context())
		require.NoError(t, err)
		require.Empty(t, api.take())
		require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
		require.Equal(t, []string{"GET /apis/networking.k8s.io/v1/ingressclasses/" + name}, api.take())
	}
}

func TestIngressClassLookupObservationConsistency(t *testing.T) {
	for _, field := range []string{
		"unchanged", "name", "uid", "resource-version", "generation", "generate-name", "labels",
		"annotations", "controller", "parameters", "api-group", "kind", "reference-name",
		"scope", "namespace", "appears", "disappears",
	} {
		t.Run(field, func(t *testing.T) {
			api := newIngressClassLookupServer(t)
			if field == "appears" {
				api.obj = nil
			}
			e := ingressClassLookupExecutor(t, ingressClassLookupLibrary(t), t.TempDir(), "steady",
				map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}})
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			api.mu.Lock()
			switch field {
			case "name":
				api.obj.Name = "returned"
			case "uid":
				api.obj.UID = "replacement"
			case "resource-version":
				api.obj.ResourceVersion = "2"
			case "generation":
				api.obj.Generation = 2
			case "generate-name":
				api.obj.GenerateName = "prefix-"
			case "labels":
				api.obj.Labels = map[string]string{"live": "changed"}
			case "annotations":
				api.obj.Annotations = map[string]string{"live": "changed"}
			case "controller":
				api.obj.Spec.Controller = "Unknown"
			case "parameters":
				api.obj.Spec.Parameters = nil
			case "api-group":
				api.obj.Spec.Parameters.APIGroup = new("")
			case "kind":
				api.obj.Spec.Parameters.Kind = "ConfigMap"
			case "reference-name":
				api.obj.Spec.Parameters.Name = "parameters"
			case "scope":
				api.obj.Spec.Parameters.Scope = new("")
			case "namespace":
				api.obj.Spec.Parameters.Namespace = new("")
			case "appears":
				api.obj = &networkingv1.IngressClass{}
			case "disappears":
				api.obj = nil
			}
			api.mu.Unlock()
			err = networkPolicyLookupSavedApply(t, e, p)
			if field == "unchanged" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			get := "GET /apis/networking.k8s.io/v1/ingressclasses/example"
			require.Equal(t, []string{get, get}, api.take())
			_, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{get}, api.take())
		})
	}
}

func TestIngressClassLookupHistoricalInvalidTeardown(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint(destroy), func(t *testing.T) {
			api := newIngressClassLookupServer(t)
			lib := ingressClassLookupLibrary(t)
			rules := lib.Constraints
			lib.Constraints = nil
			e := ingressClassLookupExecutor(t, lib, t.TempDir(), "steady", map[string]any{
				"server": api.url, "metadata": map[string]any{"name": "BAD"},
			})
			api.obj.Name = "BAD"
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
			api.take()
			lib.Constraints = rules
			before := api.obj.DeepCopy()
			if destroy {
				e.Destroy = true
			} else {
				removed := ingressClassLookupExecutor(t, lib, t.TempDir(), "removed", e.Inputs)
				removed.Store = e.Store
				e = removed
			}
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
			require.Empty(t, api.take())
			require.Equal(t, before, api.obj)
		})
	}
}
