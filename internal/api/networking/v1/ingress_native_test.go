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

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func extractedIngressConstraints(t *testing.T) []lang.ConstraintSpec {
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
	constraints := schema.Resources["ingress"].Constraints
	require.NotEmpty(t, constraints)
	return constraints
}

func ingressInputExecutor(
	t *testing.T, constraints []lang.ConstraintSpec, input IngressResource,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"networking": {Name: "kubernetes-networking-v1",
		Configuration: config.LibraryConfiguration(),
		Constraints:   map[string][]lang.ConstraintSpec{"resource.ingress": constraints},
		Resources: map[string]runtime.ResourceRegistration{"ingress": runtime.MakeResource[
			IngressResource, *IngressResourceOutput, *config.Configuration,
		]((&IngressResource{}).ResourceDefinition())},
	}}
	store, err := local.NewStore(t.TempDir(), "inputs", "test", encrypters.Noop{})
	require.NoError(t, err)
	encoded, err := ub.Marshal(input)
	require.NoError(t, err)
	var inputs map[string]any
	require.NoError(t, ub.Unmarshal(encoded, &inputs))
	timeout := any(nil)
	if input.CreateTimeout != nil {
		timeout = *input.CreateTimeout
	}
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs,
		Inputs: map[string]any{
			"server": "https://unused.invalid", "token": "", "metadata": inputs["metadata"],
			"spec": inputs["spec"], "wait": false, "timeout": timeout,
			"delete-timeout": inputs["delete-timeout"], "ignore": inputs["ignore-labels"],
			"annotations": inputs["ignore-annotations"]}, Store: store,
		Factory: state.FactoryInfo{Name: "inputs", Version: "v0", ContentRevision: "c0"}}
}

func TestIngressNativeSavedPlans(t *testing.T) {
	constraints := extractedIngressConstraints(t)
	var stored *networkingv1.Ingress
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		switch req.Method {
		case "POST":
			stored = &networkingv1.Ingress{}
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
	executor := ingressInputExecutor(t, nil, IngressResource{})
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
				if step.Address == "resource.routing" {
					decisions = append(decisions, step.Decision)
				}
			}
			require.Equal(t, []runtime.Decision{tc.decision}, decisions)
			lib.Constraints = map[string][]lang.ConstraintSpec{"resource.ingress": constraints}
			before := len(calls)
			require.ErrorContains(t, apply(plan), "resource.routing: schema: constraints[0]")
			require.Empty(t, calls[before:])
			_, err = executor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.routing: schema: constraints[0]")
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
	require.Equal(t, []string{"GET", "DELETE", "GET"}, calls[before:])
	require.Nil(t, stored)
}

func TestIngressDeferredConstraints(t *testing.T) {
	constraints := extractedIngressConstraints(t)
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "valid"}[valid], func(t *testing.T) {
			stored := map[string]*networkingv1.Ingress{}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				name := filepath.Base(req.URL.Path)
				if req.Method == "POST" {
					obj := &networkingv1.Ingress{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
					obj.Name = obj.GenerateName + "actual"
					obj.UID = "uid"
					name = obj.Name
					if obj.GenerateName == "seed-" {
						hostname := ""
						if valid {
							hostname = "deferred-"
						}
						obj.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{
							Hostname: hostname,
						}}
					}
					stored[name] = obj
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored[name]))
			})
			executor := ingressInputExecutor(t, constraints, IngressResource{})
			executor.Inputs["server"] = *c.Server
			body, err := os.ReadFile("testdata/ub/state/pending.ub")
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
				if step.Address == "resource.routing" {
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
				require.ErrorContains(t, err, "resource.routing: schema: constraints[")
				require.ErrorContains(t, err, "constraints[2] (predicate)")
				require.Equal(t, []string{"POST", "GET"}, calls)
			}
		})
	}
}

func TestIngressNativeMetadataBounds(t *testing.T) {
	constraints := extractedIngressConstraints(t)
	for _, tc := range []struct {
		name     string
		metadata IngressMetadata
		valid    bool
	}{
		{"dotted name", IngressMetadata{Name: new("valid.dotted.name")}, true},
		{"generated prefix", IngressMetadata{GenerateName: new("valid-")}, true},
		{"empty prefix", IngressMetadata{GenerateName: new("")}, false},
		{"name at maximum", IngressMetadata{Name: new(strings.Repeat("a", 253))}, true},
		{"name too long", IngressMetadata{Name: new(strings.Repeat("a", 254))}, false},
		{"prefix at maximum", IngressMetadata{GenerateName: new(strings.Repeat("a", 63))}, true},
		{"prefix too long", IngressMetadata{GenerateName: new(strings.Repeat("a", 64))}, false},
		{"label key at maximum", IngressMetadata{Name: new("valid"), Labels: &map[string]string{
			strings.Repeat("a", 253) + "/" + strings.Repeat("B", 63): ""}}, true},
		{"label prefix too long", IngressMetadata{Name: new("valid"), Labels: &map[string]string{
			strings.Repeat("a", 254) + "/key": ""}}, false},
		{"label name too long", IngressMetadata{Name: new("valid"), Labels: &map[string]string{
			strings.Repeat("a", 64): ""}}, false},
		{"label value at maximum", IngressMetadata{Name: new("valid"), Labels: &map[string]string{
			"key": strings.Repeat("a", 63)}}, true},
		{"label value too long", IngressMetadata{Name: new("valid"), Labels: &map[string]string{
			"key": strings.Repeat("a", 64)}}, false},
		{"mixed annotation spelling", IngressMetadata{Name: new("valid"), Annotations: &map[string]string{
			"Example.COM/Mixed": "any value"}}, true},
		{"annotation lowercase Unicode", IngressMetadata{Name: new("valid"),
			Annotations: &map[string]string{
				"İK.EXAMPLE/İK": "any value"}}, true},
		{"annotation name too long", IngressMetadata{Name: new("valid"), Annotations: &map[string]string{
			strings.Repeat("a", 64): "any value"}}, false},
		{"namespace API validation", IngressMetadata{
			Name: new("valid"), Namespace: new("BAD NAMESPACE")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := ingressInputExecutor(t, constraints, IngressResource{Metadata: tc.metadata})
			_, err := executor.Plan(t.Context())
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "resource.routing: schema: constraints[")
			}
		})
	}
}
