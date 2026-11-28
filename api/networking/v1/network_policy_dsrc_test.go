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
	"slices"
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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func newNetworkPolicyLookupServer(t *testing.T) *networkPolicyLookupServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &networkPolicyLookupServer{obj: &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", UID: "uid"},
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

func networkPolicyLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := networkPolicyLookupSchema()
	require.NoError(t, err)
	lib := Library()
	copy := *schema
	copy.DataSources = maps.Clone(schema.DataSources)
	typ := *copy.DataSources["network-policy"]
	typ.EvaluatedInputs = typ.Inputs
	copy.DataSources["network-policy"] = &typ
	lib.Schema = &copy
	lib.Constraints = map[string][]lang.ConstraintSpec{"data-source.network-policy": typ.Constraints}
	lib.Defaults = map[string][]lang.DefaultSpec{"data-source.network-policy": typ.Defaults}
	return lib
}

func networkPolicyLookupSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/network-policy-lookup", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func TestNetworkPolicyLookupPublicCompiler(t *testing.T) {
	lib := networkPolicyLookupLibrary(t)
	for _, tc := range []struct{ fixture, reference, rule string }{
		{"valid", "", ""}, {"null-namespace-valid", "", ""}, {"maximums-valid", "", ""},
		{"required-invalid", "missing required field", ""},
		{"null-name-invalid", "", "constraints[0]"},
		{"type-invalid", "type mismatch", ""}, {"namespace-type-invalid", "type mismatch", ""},
		{"extra-invalid", "", ""},
		{"empty-invalid", "", "constraints[0]"}, {"name-invalid", "", "constraints[0]"},
		{"slash-invalid", "", "constraints[0]"}, {"name-length-invalid", "", "constraints[0]"},
		{"namespace-invalid", "", "constraints[1]"},
		{"namespace-slash-invalid", "", "constraints[1]"},
		{"namespace-empty-invalid", "", "constraints[1]"},
		{"namespace-upper-invalid", "", "constraints[1]"},
		{"namespace-length-invalid", "", "constraints[1]"},
		{"metadata-required-invalid", "missing required input", ""},
		{"metadata-null-invalid", "", "constraints[0]"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			checker := check.NewSyntax(networkPolicyLookupSyntax(t, tc.fixture),
				map[string]*runtime.Library{"networking": lib}, nil, "")
			refs, rules := checker.References(nil).Err(), checker.LiteralConstraints().Err()
			if tc.reference != "" {
				require.ErrorContains(t, refs, tc.reference)
				return
			}
			if tc.fixture == "null-name-invalid" || tc.fixture == "extra-invalid" ||
				tc.fixture == "metadata-null-invalid" {
				require.Error(t, refs)
			} else {
				require.NoError(t, refs)
			}
			if tc.rule != "" {
				require.ErrorContains(t, rules, "data-source.lookup")
				require.ErrorContains(t, rules, tc.rule)
			} else {
				require.NoError(t, rules)
			}
		})
	}
}

type networkPolicyLookupAnchor struct{ Value string }
type networkPolicyLookupAnchorOutput struct{ Value string }
type networkPolicyLookupPrior = runtime.Prior[
	networkPolicyLookupAnchor, *networkPolicyLookupAnchorOutput, *config.Configuration,
]

func (a *networkPolicyLookupAnchor) Create(context.Context, *config.Configuration) (
	*networkPolicyLookupAnchorOutput, error,
) {
	return &networkPolicyLookupAnchorOutput{Value: a.Value}, nil
}

func (a *networkPolicyLookupAnchor) Read(
	_ context.Context, _ *config.Configuration, prior networkPolicyLookupPrior,
) (*networkPolicyLookupAnchorOutput, error) {
	return prior.Outputs, nil
}

func (a *networkPolicyLookupAnchor) Update(
	context.Context, *config.Configuration, networkPolicyLookupPrior,
) (*networkPolicyLookupAnchorOutput, error) {
	return &networkPolicyLookupAnchorOutput{Value: a.Value}, nil
}

func (a *networkPolicyLookupAnchor) Delete(
	context.Context, *config.Configuration, networkPolicyLookupPrior,
) error {
	return nil
}

func networkPolicyLookupExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	lib.Resources["anchor"] = runtime.MakeResource[
		networkPolicyLookupAnchor, *networkPolicyLookupAnchorOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		networkPolicyLookupAnchor, *networkPolicyLookupAnchorOutput, *config.Configuration,
	]{SchemaVersion: 1})
	if lib.Actions == nil {
		lib.Actions = map[string]runtime.ActionRegistration{}
	}
	lib.Actions["seed"] = runtime.MakeAction[
		networkPolicyLookupSeed, *networkPolicyLookupSeedOutput, *config.Configuration,
	]()
	body := networkPolicyLookupSyntax(t, fixture)
	libs := map[string]*runtime.Library{"networking": lib, "helper": lib}
	store, err := local.NewStore(dir, "network-policy-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "network-policy-lookup", Version: "v0", ContentRevision: "c0"}}
}

func TestNetworkPolicyLookupNativeSteadyAndSaved(t *testing.T) {
	api := newNetworkPolicyLookupServer(t)
	lib, dir := networkPolicyLookupLibrary(t), t.TempDir()
	inputs := map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}}
	e := networkPolicyLookupExecutor(t, lib, dir, "steady", inputs)
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
	get := "GET /apis/networking.k8s.io/v1/namespaces/default/networkpolicies/example"
	require.Equal(t, []string{get, get}, api.take())
	for _, tc := range []struct {
		field      string
		value      any
		diagnostic string
	}{
		{"name", nil, "required but is null"}, {"name", false, "expected string"},
		{"name", "", "constraints[0]"}, {"name", "BAD", "constraints[0]"},
		{"name", "bad/name", "constraints[0]"},
		{"name", strings.Repeat("a", 254), "constraints[0]"},
		{"namespace", "", "constraints[1]"}, {"namespace", "bad.name", "constraints[1]"},
		{"namespace", "BAD", "constraints[1]"},
		{"namespace", strings.Repeat("a", 64), "constraints[1]"},
		{"namespace", false, "expected string"},
	} {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			valid := networkPolicyLookupExecutor(t, lib, dir, "steady", inputs)
			saved, err := valid.Plan(t.Context())
			require.NoError(t, err)
			api.take()
			metadata := map[string]any{"name": "example", tc.field: tc.value}
			bad := networkPolicyLookupExecutor(t, lib, dir, "steady",
				map[string]any{"server": api.url, "metadata": metadata})
			_, err = bad.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.lookup")
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, api.take())
			err = networkPolicyLookupSavedApply(t, bad, saved)
			require.ErrorContains(t, err, "data-source.lookup")
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, api.take())
		})
	}
}

func TestNetworkPolicyLookupNativeDeferred(t *testing.T) {
	for _, tc := range []struct {
		metadata   map[string]any
		diagnostic string
	}{
		{map[string]any{"name": "example"}, ""},
		{map[string]any{"name": "example", "namespace": nil}, ""},
		{map[string]any{"name": "example", "namespace": "selected"}, ""},
		{map[string]any{"name": "BAD"}, "constraints[0]"},
		{map[string]any{"name": nil}, "required but is null"},
		{map[string]any{"name": "example", "namespace": "bad.name"}, "constraints[1]"},
	} {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			api := newNetworkPolicyLookupServer(t)
			e := networkPolicyLookupExecutor(t, networkPolicyLookupLibrary(t), t.TempDir(), "deferred",
				map[string]any{"server": api.url, "metadata": tc.metadata})
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.take())
			err = networkPolicyLookupSavedApply(t, e, p)
			if tc.diagnostic != "" {
				require.ErrorContains(t, err, "data-source.lookup")
				require.ErrorContains(t, err, tc.diagnostic)
				require.Empty(t, api.take())
			} else {
				require.NoError(t, err)
				namespace := "default"
				if selected, ok := tc.metadata["namespace"].(string); ok {
					namespace = selected
				}
				require.Equal(t, []string{
					"GET /apis/networking.k8s.io/v1/namespaces/" + namespace + "/networkpolicies/example",
				}, api.take())
			}
		})
	}
}

func TestNetworkPolicyLookupNativeIdentityDefaults(t *testing.T) {
	name, namespace := strings.Repeat("a", 249)+".app", strings.Repeat("n", 63)
	for _, metadata := range []map[string]any{
		{"name": "example"}, {"name": "example", "namespace": nil},
		{"name": name, "namespace": namespace},
	} {
		api := newNetworkPolicyLookupServer(t)
		e := networkPolicyLookupExecutor(t, networkPolicyLookupLibrary(t), t.TempDir(), "steady",
			map[string]any{"server": api.url, "metadata": metadata})
		_, err := e.Plan(t.Context())
		require.NoError(t, err)
		selected := "default"
		if value, ok := metadata["namespace"].(string); ok {
			selected = value
		}
		require.Equal(t, []string{
			"GET /apis/networking.k8s.io/v1/namespaces/" + selected +
				"/networkpolicies/" + metadata["name"].(string),
		}, api.take())
	}
	for _, metadata := range []any{nil, map[string]any{}, map[string]any{
		"name": "example", "labels": map[string]any{},
	}} {
		api := newNetworkPolicyLookupServer(t)
		e := networkPolicyLookupExecutor(t, networkPolicyLookupLibrary(t), t.TempDir(), "steady",
			map[string]any{"server": api.url, "metadata": metadata})
		_, err := e.Plan(t.Context())
		require.ErrorContains(t, err, "data-source.lookup")
		require.Empty(t, api.take())
	}
}

func TestNetworkPolicyLookupHistoricalInvalidTeardown(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint(destroy), func(t *testing.T) {
			api := newNetworkPolicyLookupServer(t)
			lib := networkPolicyLookupLibrary(t)
			rules := lib.Constraints
			lib.Constraints = nil
			e := networkPolicyLookupExecutor(t, lib, t.TempDir(), "steady", map[string]any{
				"server": api.url, "metadata": map[string]any{"name": "BAD"},
			})
			api.obj.Name = "BAD"
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			api.take()
			lib.Constraints = rules
			err = networkPolicyLookupSavedApply(t, e, p)
			require.ErrorContains(t, err, "data-source.lookup: schema: constraints[0]")
			require.Empty(t, api.take())
			if destroy {
				e.Destroy = true
			} else {
				removed := networkPolicyLookupExecutor(t, lib, t.TempDir(), "removed", e.Inputs)
				removed.Store = e.Store
				e = removed
			}
			p, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
			require.Empty(t, api.take())
		})
	}
}

func TestNetworkPolicyLookupObservationConsistency(t *testing.T) {
	for _, field := range []string{
		"unchanged", "name", "namespace", "generate-name", "generation", "metadata", "annotations",
		"labels", "selector", "ingress", "egress", "spec", "uid", "appears", "disappears",
	} {
		t.Run(field, func(t *testing.T) {
			api := newNetworkPolicyLookupServer(t)
			if field == "appears" {
				api.obj = nil
			}
			e := networkPolicyLookupExecutor(t, networkPolicyLookupLibrary(t), t.TempDir(), "steady",
				map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}})
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			api.mu.Lock()
			switch field {
			case "name":
				api.obj.Name = "returned"
			case "namespace":
				api.obj.Namespace = "returned"
			case "generate-name":
				api.obj.GenerateName = "generated-"
			case "generation":
				api.obj.Generation = 2
			case "annotations":
				api.obj.Annotations = map[string]string{"live": "changed"}
			case "selector":
				api.obj.Spec.PodSelector.MatchLabels = map[string]string{"live": "changed"}
			case "ingress":
				api.obj.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
			case "egress":
				api.obj.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}}
			case "metadata":
				api.obj.ResourceVersion = "changed"
			case "spec":
				api.obj.Spec.PolicyTypes = []networkingv1.PolicyType{"Unfamiliar"}
			case "labels":
				api.obj.Labels = map[string]string{"live": "changed"}
			case "uid":
				api.obj.UID = "replacement"
			case "appears":
				api.obj = &networkingv1.NetworkPolicy{}
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
			get := "GET /apis/networking.k8s.io/v1/namespaces/default/networkpolicies/example"
			require.Equal(t, []string{get, get}, api.take())
			_, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{get}, api.take())
		})
	}
}

var networkPolicyLookupSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
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

type networkPolicyLookupServer struct {
	mu    sync.Mutex
	obj   *networkingv1.NetworkPolicy
	calls []string
	url   string
}

func (s *networkPolicyLookupServer) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := slices.Clone(s.calls)
	s.calls = nil
	return calls
}

type networkPolicyLookupSeed struct{ Values map[string]any }
type networkPolicyLookupSeedOutput struct{ Resolved map[string]any }

func (s *networkPolicyLookupSeed) Run(context.Context, *config.Configuration) (
	*networkPolicyLookupSeedOutput, error,
) {
	return &networkPolicyLookupSeedOutput{Resolved: s.Values}, nil
}

func networkPolicyLookupSavedApply(t *testing.T, e *runtime.Executor, p *runtime.Plan) error {
	t.Helper()
	body, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), saved)
	return err
}

func TestNetworkPolicyLookupClosedSchema(t *testing.T) {
	lib := networkPolicyLookupLibrary(t)
	for _, fixture := range []string{
		"spec-input-invalid", "labels-input-invalid", "annotations-input-invalid",
		"generate-name-input-invalid", "status-input-invalid", "ignore-input-invalid",
		"ignore-annotations-input-invalid", "status-output-invalid",
	} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(networkPolicyLookupSyntax(t, fixture),
				map[string]*runtime.Library{"networking": lib}, nil, "")
			require.ErrorContains(t, checker.References(nil).Err(), "unknown field")
		})
	}
}

func TestNetworkPolicyLookupPartialKnownIdentity(t *testing.T) {
	lib := networkPolicyLookupLibrary(t)
	for _, tc := range []struct{ fixture, field, value, rule string }{
		{"partial", "known-name", "example", ""},
		{"partial", "known-name", "BAD", "constraints[0]"},
		{"partial-name", "known-namespace", "selected", ""},
		{"partial-name", "known-namespace", "bad.name", "constraints[1]"},
	} {
		t.Run(tc.fixture+tc.value, func(t *testing.T) {
			api := newNetworkPolicyLookupServer(t)
			e := networkPolicyLookupExecutor(t, lib, t.TempDir(), tc.fixture, map[string]any{
				"server": api.url, tc.field: tc.value,
				"metadata": map[string]any{"name": "example", "namespace": "selected"},
			})
			p, err := e.Plan(t.Context())
			require.Empty(t, api.take())
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.lookup: schema: "+tc.rule)
				return
			}
			require.NoError(t, err)
			require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
			require.Equal(t, []string{
				"GET /apis/networking.k8s.io/v1/namespaces/selected/networkpolicies/example",
			}, api.take())
		})
	}
}

func TestNetworkPolicyLookupRemoval(t *testing.T) {
	api := newNetworkPolicyLookupServer(t)
	lib, dir := networkPolicyLookupLibrary(t), t.TempDir()
	e := networkPolicyLookupExecutor(t, lib, dir, "steady", map[string]any{
		"server": api.url, "metadata": map[string]any{"name": "example"},
	})
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, networkPolicyLookupSavedApply(t, e, p))
	api.take()
	removed := networkPolicyLookupExecutor(t, lib, dir, "removed", e.Inputs)
	p, err = removed.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, networkPolicyLookupSavedApply(t, removed, p))
	require.Empty(t, api.take())
	require.Equal(t, "uid", string(api.obj.UID))
}

func TestNetworkPolicyLookupCompatibility(t *testing.T) {
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

func TestNetworkPolicyLookupDeferredConfiguration(t *testing.T) {
	for _, name := range []string{"example", "BAD"} {
		t.Run(name, func(t *testing.T) {
			api := newNetworkPolicyLookupServer(t)
			e := networkPolicyLookupExecutor(t, networkPolicyLookupLibrary(t), t.TempDir(),
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
			require.Equal(t, []string{
				"GET /apis/networking.k8s.io/v1/namespaces/default/networkpolicies/example",
			}, api.take())
		})
	}
}
