package v1

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func extractedNetworkPolicyConstraints(t *testing.T) []lang.ConstraintSpec {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/networking/v1"),
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root},
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	constraints := schema.Resources["network-policy"].Constraints
	require.NotEmpty(t, constraints)
	return constraints
}

func networkPolicyInputExecutor(
	t *testing.T, constraints []lang.ConstraintSpec, input NetworkPolicyResource,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/network-policy/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"networking": {Name: "kubernetes-networking-v1",
		Configuration: config.LibraryConfiguration(),
		Constraints:   map[string][]lang.ConstraintSpec{"resource.network-policy": constraints},
		Resources: map[string]runtime.ResourceRegistration{"network-policy": runtime.MakeResource[
			NetworkPolicyResource, *NetworkPolicyResourceOutput, *config.Configuration,
		]((&NetworkPolicyResource{}).ResourceDefinition())},
	}}
	store, err := local.NewStore(t.TempDir(), "inputs", "test", encrypters.Noop{})
	require.NoError(t, err)
	if input.Spec.PolicyTypes == nil {
		input.Spec.PolicyTypes = []string{"Ingress"}
	}
	encoded, err := ub.Marshal(input)
	require.NoError(t, err)
	var inputs map[string]any
	require.NoError(t, ub.Unmarshal(encoded, &inputs))
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs,
		Inputs: map[string]any{
			"server": "https://unused.invalid", "token": "", "metadata": inputs["metadata"],
			"spec":        inputs["spec"],
			"ignore":      inputs["ignore-labels"],
			"annotations": inputs["ignore-annotations"]}, Store: store,
		Factory: state.FactoryInfo{Name: "inputs", Version: "v0", ContentRevision: "c0"}}
}

func TestNetworkPolicyNativeSavedPlans(t *testing.T) {
	constraints := extractedNetworkPolicyConstraints(t)
	var stored *networkingv1.NetworkPolicy
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		switch req.Method {
		case "POST":
			stored = &networkingv1.NetworkPolicy{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.Name, stored.UID = "historical", "uid"
		case "DELETE":
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		}
		if stored == nil {
			apiError(t, w, 404)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	executor := networkPolicyInputExecutor(t, nil, NetworkPolicyResource{})
	executor.Inputs["server"] = *c.Server
	lib := executor.Libraries["networking"]
	apply := func(plan *runtime.Plan) error {
		t.Helper()
		encoded, err := runtime.EncodePlan(plan)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		_, err = executor.ApplyPlan(t.Context(), file)
		return err
	}
	create, err := executor.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, apply(create))
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		decision runtime.Decision
	}{
		{"otherwise unchanged", map[string]any{}, runtime.DecisionNoOp},
		{"replacement", map[string]any{"name": "next", "generate-name": "next-"},
			runtime.DecisionReplace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lib.Constraints = nil
			executor.Inputs["metadata"] = tc.metadata
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			var decisions []runtime.Decision
			for _, step := range plan.Steps {
				if step.Address == "resource.policy" {
					decisions = append(decisions, step.Decision)
				}
			}
			require.Equal(t, []runtime.Decision{tc.decision}, decisions)
			lib.Constraints = map[string][]lang.ConstraintSpec{"resource.network-policy": constraints}
			before := len(calls)
			require.ErrorContains(t, apply(plan), "resource.policy: schema: constraints[0]")
			require.Empty(t, calls[before:])
			_, err = executor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.policy: schema: constraints[0]")
			require.NotContains(t, calls[before:], "DELETE")
			require.NotContains(t, calls[before:], "POST")
			require.NotContains(t, calls[before:], "PUT")
		})
	}
	executor.Inputs["metadata"] = map[string]any{}
	executor.Destroy = true
	destroy, err := executor.Plan(t.Context())
	require.NoError(t, err)
	before := len(calls)
	require.NoError(t, apply(destroy))
	require.Equal(t, []string{"GET", "DELETE"}, calls[before:])
	require.Nil(t, stored)
}

func TestNetworkPolicyDeferredConstraints(t *testing.T) {
	constraints := extractedNetworkPolicyConstraints(t)
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "valid"}[valid], func(t *testing.T) {
			stored := map[string]*networkingv1.NetworkPolicy{}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				name := filepath.Base(req.URL.Path)
				if req.Method == "POST" {
					obj := &networkingv1.NetworkPolicy{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
					obj.Name = obj.GenerateName + "actual"
					obj.UID = "uid"
					name = obj.Name
					if obj.GenerateName == "seed-" {
						prefix := "BAD"
						if valid {
							prefix = "deferred-"
						}
						obj.UID = types.UID(prefix)
					}
					stored[name] = obj
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored[name]))
			})
			executor := networkPolicyInputExecutor(t, constraints, NetworkPolicyResource{})
			executor.Inputs["server"] = *c.Server
			body, err := os.ReadFile("testdata/ub/network-policy/pending.ub")
			require.NoError(t, err)
			parsed, err := syntax.ParseSource("factory.ub", body)
			require.NoError(t, err)
			executor.DAG = runtime.BuildSyntaxDAG(parsed.Factory.Body, executor.Libraries)
			executor.SyntaxSource = &parsed.Factory.Body
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, calls)
			pending := false
			for _, step := range plan.Steps {
				if step.Address == "resource.policy" {
					require.Contains(t, step.UnresolvedInputs, "metadata")
					pending = true
				}
			}
			require.True(t, pending)
			encoded, err := runtime.EncodePlan(plan)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(encoded)
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), file)
			if valid {
				require.NoError(t, err)
				require.Equal(t, []string{"POST", "GET", "POST", "GET", "GET"}, calls)
				require.Contains(t, stored, "deferred-actual")
			} else {
				require.ErrorContains(t, err, "resource.policy: schema: constraints[")
				require.ErrorContains(t, err, "constraints[2] (predicate)")
				require.Equal(t, []string{"POST", "GET"}, calls)
			}
		})
	}
}

func TestNetworkPolicyNativeMetadataBounds(t *testing.T) {
	constraints := extractedNetworkPolicyConstraints(t)
	for _, tc := range []struct {
		name     string
		metadata NetworkPolicyMetadata
		valid    bool
	}{
		{"dotted name", NetworkPolicyMetadata{Name: new("valid.dotted.name")}, true},
		{"generated prefix", NetworkPolicyMetadata{GenerateName: new("valid-")}, true},
		{"empty prefix", NetworkPolicyMetadata{GenerateName: new("")}, false},
		{"name at maximum", NetworkPolicyMetadata{Name: new(strings.Repeat("a", 253))}, true},
		{"name too long", NetworkPolicyMetadata{Name: new(strings.Repeat("a", 254))}, false},
		{"prefix at maximum", NetworkPolicyMetadata{GenerateName: new(strings.Repeat("a", 63))}, true},
		{"prefix too long", NetworkPolicyMetadata{GenerateName: new(strings.Repeat("a", 64))}, false},
		{"label key at maximum", NetworkPolicyMetadata{Name: new("valid"), Labels: &map[string]string{
			strings.Repeat("a", 253) + "/" + strings.Repeat("B", 63): ""}}, true},
		{"label prefix too long", NetworkPolicyMetadata{Name: new("valid"), Labels: &map[string]string{
			strings.Repeat("a", 254) + "/key": ""}}, false},
		{"label name too long", NetworkPolicyMetadata{Name: new("valid"), Labels: &map[string]string{
			strings.Repeat("a", 64): ""}}, false},
		{"label value at maximum", NetworkPolicyMetadata{Name: new("valid"), Labels: &map[string]string{
			"key": strings.Repeat("a", 63)}}, true},
		{"label value too long", NetworkPolicyMetadata{Name: new("valid"), Labels: &map[string]string{
			"key": strings.Repeat("a", 64)}}, false},
		{"mixed annotation spelling", NetworkPolicyMetadata{Name: new("valid"),
			Annotations: &map[string]string{"Example.COM/Mixed": "any value"}}, true},
		{"annotation lowercase Unicode", NetworkPolicyMetadata{Name: new("valid"),
			Annotations: &map[string]string{
				"İK.EXAMPLE/İK": "any value"}}, true},
		{"annotation name too long", NetworkPolicyMetadata{Name: new("valid"),
			Annotations: &map[string]string{strings.Repeat("a", 64): "any value"}}, false},
		{"namespace API validation", NetworkPolicyMetadata{
			Name: new("valid"), Namespace: new("BAD NAMESPACE")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := networkPolicyInputExecutor(t, constraints,
				NetworkPolicyResource{Metadata: tc.metadata})
			_, err := executor.Plan(t.Context())
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "resource.policy: schema: constraints[")
			}
		})
	}
}

func TestNetworkPolicyNativePolicyTypes(t *testing.T) {
	constraints := extractedNetworkPolicyConstraints(t)
	for _, types := range [][]string{{}, {"Ingress"}, {"unknown"}, {"unknown", "unknown"},
		{"Ingress", "Egress", "Ingress"}} {
		r := networkPolicy()
		r.Spec.PolicyTypes = types
		executor := networkPolicyInputExecutor(t, constraints, *r)
		_, err := executor.Plan(t.Context())
		if len(types) == 0 || len(types) > 2 {
			require.ErrorContains(t, err, "resource.policy: schema: constraints[8]")
		} else {
			require.NoError(t, err)
		}
	}
}
