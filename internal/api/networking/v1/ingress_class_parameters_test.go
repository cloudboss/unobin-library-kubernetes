package v1

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestIngressClassRequiredEvaluatedParameters(t *testing.T) {
	binary := compileIngressClassFactory(t, "parameters")
	for _, tc := range []struct {
		name       string
		parameters map[string]any
		missing    string
		want       *networkingv1.IngressClassParametersReference
	}{
		{name: "missing kind", parameters: map[string]any{"name": "settings"}, missing: "kind"},
		{name: "missing name", parameters: map[string]any{"kind": "ConfigMap"}, missing: "name"},
		{
			name:       "populated",
			parameters: map[string]any{"kind": "ConfigMap", "name": "settings"},
			want:       &networkingv1.IngressClassParametersReference{Kind: "ConfigMap", Name: "settings"},
		},
		{name: "omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newIngressClassPlanAPI(t)
			work := t.TempDir()
			inputs := map[string]any{"server": api.server}
			if tc.parameters != nil {
				inputs["parameters"] = tc.parameters
			}
			out, err := planCompiledIngressClass(t, binary, work, inputs)
			if tc.missing != "" {
				requireIngressClassRequiredField(t, out, err, tc.missing)
				require.Empty(t, api.requests())
				return
			}
			require.NoError(t, err, "%s", out)
			require.Equal(t, runtime.DecisionCreate, compiledIngressClassStep(t, work).Decision)
			out, err = runIngressClassFactory(t, binary, work, "apply", "plan.ubp")
			require.NoError(t, err, "%s", out)
			created := api.class("parameters")
			require.NotNil(t, created)
			require.Equal(t, tc.want, created.Spec.Parameters)
			require.Equal(t, []string{"POST " + ingressClassPath}, api.requests())
		})
	}
}

func TestIngressClassRequiredParametersSavedNoOpAndHistoricalDestroy(t *testing.T) {
	const class = "parameters"
	const path = ingressClassPath + "/" + class
	binary := compileIngressClassFactory(t, "parameters")
	api := newIngressClassPlanAPI(t)
	work := t.TempDir()
	inputs := map[string]any{
		"server": api.server, "parameters": map[string]any{"kind": "ConfigMap", "name": "settings"},
	}
	out, err := planCompiledIngressClass(t, binary, work, inputs)
	require.NoError(t, err, "%s", out)
	out, err = runIngressClassFactory(t, binary, work, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	original := api.class(class)
	require.NotNil(t, original)
	require.Equal(t, types.UID("parameters-uid"), original.UID)

	out, err = planCompiledIngressClass(t, binary, work, inputs)
	require.NoError(t, err, "%s", out)
	require.Equal(t, runtime.DecisionNoOp, compiledIngressClassStep(t, work).Decision)
	before := len(api.requests())
	out, err = runIngressClassFactory(t, binary, work, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	require.Equal(t, original, api.class(class))
	require.Empty(t, api.requests()[before:])

	store, err := local.NewStore(
		filepath.Join(work, ".unobin", "state"), "ingress-class-parameters", "config",
		encrypters.Noop{},
	)
	require.NoError(t, err)
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.class")
	require.NotNil(t, entry)
	missingKind := map[string]any{"name": "settings"}
	entry.Inputs["spec"].(map[string]any)["parameters"] = missingKind
	rev, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(rev))

	out, err = planCompiledIngressClass(t, binary, work, inputs)
	require.NoError(t, err, "%s", out)
	plan := openIngressClassPlan(t, work)
	step := ingressClassPlanStep(t, plan, "resource.class")
	require.Equal(t, runtime.DecisionUpdate, step.Decision)
	require.Equal(t, missingKind, step.PriorInputs["spec"].(map[string]any)["parameters"])
	require.Equal(t, step.PriorOutputs, step.ObservedOutputs)

	inputs["parameters"] = missingKind
	before = len(api.requests())
	out, err = planCompiledIngressClass(t, binary, work, inputs)
	requireIngressClassRequiredField(t, out, err, "kind")
	require.Empty(t, api.requests()[before:])

	plan.Inputs["parameters"] = missingKind
	for idx := range plan.Steps {
		if plan.Steps[idx].Address == "resource.class" {
			plan.Steps[idx].Decision = runtime.DecisionNoOp
			plan.Steps[idx].Inputs = entry.Inputs
		}
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	sealed, err := state.Seal(raw, state.PayloadTypePlan, encrypters.Noop{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "invalid-noop.ubp"), sealed, 0o600))
	out, err = runIngressClassFactory(t, binary, work, "apply", "invalid-noop.ubp")
	requireIngressClassRequiredField(t, out, err, "kind")
	require.Empty(t, api.requests()[before:])
	require.Equal(t, original, api.class(class))

	out, err = runIngressClassFactory(t, binary, work,
		"plan", "-c", "config.ub", "--destroy", "-o", "plan.ubp")
	require.NoError(t, err, "%s", out)
	require.Equal(t, runtime.DecisionDestroy, compiledIngressClassStep(t, work).Decision)
	before = len(api.requests())
	out, err = runIngressClassFactory(t, binary, work, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	require.Equal(t, []string{"GET " + path, "DELETE " + path, "GET " + path}, api.requests()[before:])
	require.Nil(t, api.class(class))
}

func TestIngressClassRequiredDeferredReplacementParameters(t *testing.T) {
	binary := compileIngressClassFactory(t, "pending-parameters")
	for _, tc := range []struct {
		name, missing string
		parameters    map[string]any
	}{
		{"missing kind", "kind", map[string]any{"name": "replacement"}},
		{"missing name", "name", map[string]any{"kind": "ConfigMap"}},
		{"populated", "", map[string]any{"kind": "ConfigMap", "name": "replacement"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newIngressClassPlanAPI(t)
			work := t.TempDir()
			inputs := map[string]any{
				"server": api.server, "seed-prefix": "seed-", "class-name": "before",
				"parameters":             map[string]any{"kind": "ConfigMap", "name": "settings"},
				"replacement-parameters": tc.parameters,
			}
			out, err := planCompiledIngressClass(t, binary, work, inputs)
			require.NoError(t, err, "%s", out)
			out, err = runIngressClassFactory(t, binary, work, "apply", "plan.ubp")
			require.NoError(t, err, "%s", out)
			original := api.class("before")
			require.NotNil(t, original)
			require.Equal(t, types.UID("before-uid"), original.UID)

			inputs["seed-prefix"], inputs["class-name"] = "updated-", "after"
			out, err = planCompiledIngressClass(t, binary, work, inputs)
			require.NoError(t, err, "%s", out)
			plan := openIngressClassPlan(t, work)
			require.Equal(t, runtime.DecisionReplace,
				ingressClassPlanStep(t, plan, "resource.seed").Decision)
			step := ingressClassPlanStep(t, plan, "resource.class")
			require.Equal(t, runtime.DecisionReplace, step.Decision)
			require.Equal(t, map[string][]string{"spec": {"resource.seed.name"}}, step.UnresolvedInputs)

			before := len(api.requests())
			out, err = runIngressClassFactory(t, binary, work, "apply", "plan.ubp")
			seedRequests := []string{
				"GET " + ingressClassPath + "/seed-actual",
				"DELETE " + ingressClassPath + "/seed-actual",
				"GET " + ingressClassPath + "/seed-actual", "POST " + ingressClassPath,
			}
			if tc.missing != "" {
				requireIngressClassRequiredField(t, out, err, tc.missing)
				require.Equal(t, seedRequests, api.requests()[before:])
				require.Equal(t, original, api.class("before"))
				require.Nil(t, api.class("after"))
				seed := api.class("updated-actual")
				require.NotNil(t, seed)
				require.Equal(t, types.UID("updated-actual-uid"), seed.UID)
				return
			}
			require.NoError(t, err, "%s", out)
			require.Equal(t, append(seedRequests,
				"GET "+ingressClassPath+"/before", "DELETE "+ingressClassPath+"/before",
				"GET "+ingressClassPath+"/before", "POST "+ingressClassPath,
				"GET "+ingressClassPath+"/updated-actual",
			), api.requests()[before:])
			require.Nil(t, api.class("before"))
			created := api.class("after")
			require.NotNil(t, created)
			require.Equal(t, types.UID("after-uid"), created.UID)
			require.Equal(t, &networkingv1.IngressClassParametersReference{
				Kind: "ConfigMap", Name: "replacement",
			}, created.Spec.Parameters)
		})
	}
}

func requireIngressClassRequiredField(t *testing.T, out string, err error, field string) {
	t.Helper()
	require.Error(t, err, "%s", out)
	require.Contains(t, out,
		`resource.class: field "spec.parameters.`+field+`": required but not provided`)
}

type ingressClassPlanAPI struct {
	server string
	mu     sync.Mutex
	calls  []string
	stored map[string]*networkingv1.IngressClass
}

func newIngressClassPlanAPI(t *testing.T) *ingressClassPlanAPI {
	t.Helper()
	api := &ingressClassPlanAPI{stored: map[string]*networkingv1.IngressClass{}}
	c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, req.Method+" "+req.URL.Path)
		name := filepath.Base(req.URL.Path)
		switch req.Method {
		case "POST":
			obj := &networkingv1.IngressClass{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
			if obj.Name == "" {
				obj.Name = obj.GenerateName + "actual"
			}
			obj.UID = types.UID(obj.Name + "-uid")
			name = obj.Name
			api.stored[name] = obj
		case "DELETE":
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			stored := api.stored[name]
			require.NotNil(t, stored)
			require.Equal(t, &metav1.Preconditions{UID: &stored.UID}, options.Preconditions)
			delete(api.stored, name)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		case "GET":
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			apiError(t, w, http.StatusMethodNotAllowed)
			return
		}
		if obj := api.stored[name]; obj != nil {
			require.NoError(t, json.NewEncoder(w).Encode(obj))
		} else {
			apiError(t, w, http.StatusNotFound)
		}
	})
	api.server = *c.Server
	return api
}

func (a *ingressClassPlanAPI) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

func (a *ingressClassPlanAPI) class(name string) *networkingv1.IngressClass {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stored[name].DeepCopy()
}
