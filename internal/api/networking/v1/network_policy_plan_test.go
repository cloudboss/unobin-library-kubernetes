package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNetworkPolicyStateBackedReconciliation(t *testing.T) {
	r := networkPolicy()
	r.Metadata.Name, r.Metadata.GenerateName = nil, new("generated-")
	r.Spec.Ingress = new([]NetworkPolicyIngress{{
		Ports: new([]NetworkPolicyPort{{Port: new("0080")}}),
	}})
	var stored *networkingv1.NetworkPolicy
	var mutations []string
	var credentials []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		switch req.Method {
		case "POST":
			mutations = append(mutations, req.Method)
			stored = &networkingv1.NetworkPolicy{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.Name, stored.UID = "generated-actual", "uid"
		case "PATCH":
			mutations = append(mutations, req.Method)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			p, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			after, err := p.Apply(before)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(after, stored))
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
	executor := networkPolicyInputExecutor(t, extractedNetworkPolicyConstraints(t), *r)
	executor.Inputs["server"] = *c.Server
	inputs := func() {
		body, err := ub.Marshal(r)
		require.NoError(t, err)
		var in map[string]any
		require.NoError(t, ub.Unmarshal(body, &in))
		executor.Inputs["metadata"], executor.Inputs["spec"] = in["metadata"], in["spec"]
	}
	plan := func(want runtime.Decision) *runtime.Plan {
		t.Helper()
		p, err := executor.Plan(t.Context())
		require.NoError(t, err)
		var decisions []runtime.Decision
		for _, step := range p.Steps {
			if step.Address == "resource.policy" {
				decisions = append(decisions, step.Decision)
			}
		}
		require.Equal(t, []runtime.Decision{want}, decisions)
		return p
	}
	apply := func(p *runtime.Plan) {
		t.Helper()
		body, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(body)
		require.NoError(t, err)
		_, err = executor.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
	}
	apply(plan(runtime.DecisionCreate))
	require.Equal(t, []string{"POST"}, mutations)
	apply(plan(runtime.DecisionNoOp))
	stored.Generation, stored.ResourceVersion = 30, "volatile"
	stored.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "external"}}
	plan(runtime.DecisionNoOp)
	r.Metadata.Namespace = new("default")
	(*(*r.Spec.Ingress)[0].Ports)[0].Port = new("80")
	inputs()
	plan(runtime.DecisionNoOp)
	stored.Spec.Ingress = nil
	mutations = nil
	apply(plan(runtime.DecisionUpdate))
	require.Equal(t, []string{"PATCH"}, mutations)
	plan(runtime.DecisionNoOp)
	r.Spec.Ingress = new([]NetworkPolicyIngress{})
	inputs()
	apply(plan(runtime.DecisionUpdate))
	require.Empty(t, stored.Spec.Ingress)
	plan(runtime.DecisionNoOp)
	for _, change := range []string{"name", "generate-name", "namespace"} {
		inputs()
		m := executor.Inputs["metadata"].(map[string]any)
		if change == "name" {
			delete(m, "generate-name")
		}
		m[change] = "different"
		plan(runtime.DecisionReplace)
	}
	inputs()
	executor.Inputs["token"] = "current"
	plan(runtime.DecisionNoOp)
	require.Contains(t, credentials, "Bearer current")
	executor.Inputs["server"] = "https://unused.invalid"
	plan(runtime.DecisionReplace)
	executor.Inputs["server"] = *c.Server
	stored = nil
	plan(runtime.DecisionCreate)
}
