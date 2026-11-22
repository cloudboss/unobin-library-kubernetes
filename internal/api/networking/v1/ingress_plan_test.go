package v1

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
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

func TestIngressStateBackedReconciliation(t *testing.T) {
	var stored *networkingv1.Ingress
	var mutations []string
	var credentials []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		switch req.Method {
		case "POST", "PUT":
			mutations = append(mutations, req.Method)
			stored = &networkingv1.Ingress{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.UID = "uid"
			if stored.Name == "" {
				stored.Name = "example-generated"
			}
			if stored.Spec.IngressClassName == nil {
				stored.Spec.IngressClassName = new("assigned")
			}
		case "DELETE":
			mutations = append(mutations, req.Method)
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
	body, err := os.ReadFile("testdata/ub/state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"networking": {
		Name: "kubernetes-networking-v1", Configuration: config.LibraryConfiguration(),
		Constraints: map[string][]lang.ConstraintSpec{"resource.ingress": extractedIngressConstraints(t)},
		Resources: map[string]runtime.ResourceRegistration{"ingress": runtime.MakeResource[
			IngressResource, *IngressResourceOutput, *config.Configuration,
		]((&IngressResource{}).ResourceDefinition())},
	}}
	store, err := local.NewStore(t.TempDir(), "ingress", "test", encrypters.Noop{})
	require.NoError(t, err)
	r := ingress()
	inputs := func() map[string]any {
		encoded, err := ub.Marshal(r)
		require.NoError(t, err)
		var resource map[string]any
		require.NoError(t, ub.Unmarshal(encoded, &resource))
		return map[string]any{"server": *c.Server, "metadata": resource["metadata"],
			"spec": resource["spec"], "token": "first", "wait": false,
			"timeout": "2s", "ignore": []any{}, "annotations": []any{}, "delete-timeout": "2s"}
	}
	plan := func(in map[string]any, want runtime.Decision) (*runtime.Executor, *runtime.Plan) {
		t.Helper()
		exec := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: in, Store: store,
			Factory: state.FactoryInfo{Name: "ingress", Version: "v0", ContentRevision: "c0"}}
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.routing" {
				require.Equal(t, want, step.Decision)
				found = true
			}
		}
		require.True(t, found)
		return exec, p
	}
	apply := func(exec *runtime.Executor, p *runtime.Plan) {
		t.Helper()
		encoded, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
	}
	exec, p := plan(inputs(), runtime.DecisionCreate)
	apply(exec, p)
	require.Equal(t, []string{"POST"}, mutations)
	exec, p = plan(inputs(), runtime.DecisionNoOp)
	apply(exec, p)
	for _, endpoints := range [][]networkingv1.IngressLoadBalancerIngress{
		{{IP: "192.0.2.1"}, {Hostname: "first"}, {}},
		{{}, {Hostname: "first"}, {IP: "192.0.2.1"}},
		{{Hostname: "changed"}}, nil,
	} {
		stored.Status.LoadBalancer.Ingress = endpoints
		exec, p = plan(inputs(), runtime.DecisionUpdate)
		mutations = nil
		apply(exec, p)
		require.Empty(t, mutations)
		plan(inputs(), runtime.DecisionNoOp)
	}
	stored.Spec.IngressClassName = new("new-remote-class")
	exec, p = plan(inputs(), runtime.DecisionUpdate)
	mutations = nil
	apply(exec, p)
	require.Empty(t, mutations)
	plan(inputs(), runtime.DecisionNoOp)
	stored.Generation, stored.ResourceVersion = 50, "volatile"
	plan(inputs(), runtime.DecisionNoOp)
	for _, update := range []string{"wait", "metadata", "spec", "writable drift", "bookkeeping"} {
		in := inputs()
		in["wait"] = true
		switch update {
		case "metadata":
			r.Metadata.Labels = new(map[string]string{"key": "changed"})
			in = inputs()
			in["wait"] = true
		case "spec":
			r.Spec.DefaultBackend = &IngressBackend{}
			in = inputs()
			in["wait"] = true
		case "writable drift":
			stored.Spec.DefaultBackend = nil
		case "bookkeeping":
			in["timeout"], in["ignore"] = "3s", []any{"ignored"}
		}
		exec, p = plan(in, runtime.DecisionUpdate)
		mutations = nil
		apply(exec, p)
		if update == "bookkeeping" {
			require.Empty(t, mutations)
		} else {
			require.Equal(t, []string{"PUT"}, mutations)
		}
		plan(in, runtime.DecisionNoOp)
	}
	for _, key := range []string{"name", "generate-name", "namespace"} {
		in := inputs()
		m := in["metadata"].(map[string]any)
		if key == "generate-name" {
			delete(m, "name")
		}
		m[key] = "different"
		plan(in, runtime.DecisionReplace)
	}
	in := inputs()
	in["token"] = "current"
	in["wait"] = true
	plan(in, runtime.DecisionUpdate)
	require.Contains(t, credentials, "Bearer current")
	in = inputs()
	in["server"] = "https://unused.invalid"
	plan(in, runtime.DecisionReplace)
	stored = nil
	plan(inputs(), runtime.DecisionCreate)
}
