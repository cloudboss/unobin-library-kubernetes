package v1_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	public "github.com/cloudboss/unobin-library-kubernetes/api/admissionregistration/v1"
)

func policyLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(
		filepath.Join(root, "api/admissionregistration/v1"),
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root},
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out))},
	)
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := public.Library()
	constraints := schema.Resources["validating-admission-policy"].Constraints
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.validating-admission-policy": constraints,
	}
	return lib
}

func policyExecutor(
	t *testing.T, fixture string, lib *runtime.Library, store *local.Store, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(fixture)
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"admission": lib}
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(source.Factory.Body, libs), Libraries: libs,
		SyntaxSource: &source.Factory.Body, Store: store, Inputs: inputs, Parallelism: 1,
		Factory: state.FactoryInfo{Name: "policy", Version: "v0"},
	}
}

type policyPlanAPI struct {
	mu          sync.Mutex
	objects     map[string]*admissionv1.ValidatingAdmissionPolicy
	calls       []string
	seedName    string
	lastRequest admissionv1.ValidatingAdmissionPolicy
	lastDelete  metav1.DeleteOptions
}

func (s *policyPlanAPI) serve(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	pathName := filepath.Base(req.URL.Path)
	var response any
	switch req.Method {
	case http.MethodPost, http.MethodPut:
		var body admissionv1.ValidatingAdmissionPolicy
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.lastRequest = body
		name := body.Name
		if name == "" {
			name = body.GenerateName + "actual"
		}
		if body.GenerateName == "seed-" {
			name = s.seedName
		}
		if body.UID == "" {
			body.UID = types.UID(name + "-uid")
		}
		body.Name = name
		pathName = name
		s.objects[name] = &body
		response = &body
	case http.MethodGet:
		found := s.objects[pathName]
		if found == nil {
			w.WriteHeader(http.StatusNotFound)
			response = metav1.Status{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
				Status:   metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound,
			}
		} else {
			response = found
		}
	case http.MethodDelete:
		var options metav1.DeleteOptions
		if err := json.NewDecoder(req.Body).Decode(&options); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.lastDelete = options
		delete(s.objects, pathName)
		response = metav1.Status{Status: metav1.StatusSuccess}
	}
	s.calls = append(s.calls, req.Method+" "+pathName)
	_ = json.NewEncoder(w).Encode(response)
}

func (s *policyPlanAPI) operations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.calls = nil }()
	return s.calls
}

func policyPlanServer(t *testing.T) (*policyPlanAPI, string) {
	t.Helper()
	api := &policyPlanAPI{objects: map[string]*admissionv1.ValidatingAdmissionPolicy{}}
	server := httptest.NewTLSServer(http.HandlerFunc(api.serve))
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	t.Cleanup(server.Close)
	return api, server.URL
}

func policyStep(t *testing.T, plan *runtime.Plan, want runtime.Decision) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			require.Equal(t, want, step.Decision)
			return step
		}
	}
	t.Fatal("missing resource.subject")
	return nil
}

func planPolicy(t *testing.T, executor *runtime.Executor, want runtime.Decision) *runtime.Plan {
	t.Helper()
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	policyStep(t, plan, want)
	return plan
}

func applyPolicy(t *testing.T, executor *runtime.Executor, plan *runtime.Plan) error {
	t.Helper()
	encoded, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), saved)
	return err
}

func TestValidatingAdmissionPolicyPlans(t *testing.T) {
	lib := policyLibrary(t)
	api, url := policyPlanServer(t)
	store, err := local.NewStore(t.TempDir(), "policy", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := map[string]any{
		"server": url, "token": "current", "prefix": "generated-",
		"name": nil, "match": nil, "label": "initial", "failure": "Fail", "update-timeout": nil,
		"kubeconfig": nil, "current-context": nil,
	}
	executor := policyExecutor(t, "testdata/ub/policy.ub", lib, store, inputs)
	initial := planPolicy(t, executor, runtime.DecisionCreate)
	require.NoError(t, applyPolicy(t, executor, initial))
	require.Equal(t, []string{"POST generated-actual"}, api.operations())
	noop := planPolicy(t, executor, runtime.DecisionNoOp)
	require.NoError(t, applyPolicy(t, executor, noop))
	require.Equal(t, []string{"GET generated-actual"}, api.operations())

	api.mu.Lock()
	live := api.objects["generated-actual"]
	live.Spec.FailurePolicy = new(admissionv1.Ignore)
	live.ResourceVersion, live.Generation, live.Status.ObservedGeneration = "8", 6, 5
	api.mu.Unlock()
	planPolicy(t, executor, runtime.DecisionNoOp)
	require.Equal(t, []string{"GET generated-actual"}, api.operations())

	api.mu.Lock()
	live.Labels = map[string]string{"external": "changed"}
	live.Annotations = nil
	api.mu.Unlock()
	drift := policyStep(t, planPolicy(t, executor, runtime.DecisionUpdate), runtime.DecisionUpdate)
	observed := drift.ObservedOutputs["metadata"].(map[string]any)
	require.Equal(t, map[string]any{"external": "changed"}, observed["labels"])
	require.Equal(t, map[string]any{"retained": "configured"}, observed["annotations"])
	api.mu.Lock()
	live.Labels = nil
	api.mu.Unlock()
	planPolicy(t, executor, runtime.DecisionNoOp)

	for _, tc := range []struct {
		field string
		value any
	}{
		{"label", "updated"},
		{"failure", "Ignore"},
		{"match", "Equivalent"},
		{"update-timeout", "3s"},
	} {
		t.Run(tc.field+" update", func(t *testing.T) {
			before := inputs[tc.field]
			inputs[tc.field] = tc.value
			defer func() { inputs[tc.field] = before }()
			planPolicy(t, executor, runtime.DecisionUpdate)
		})
	}
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "default",
		Contexts: map[string]*clientcmdapi.Context{
			"default": {Cluster: "cluster"}, "other": {Cluster: "cluster"},
		},
		Clusters: map[string]*clientcmdapi.Cluster{"cluster": {Server: url}},
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)
	for _, tc := range []struct{ field, value, reason string }{
		{"prefix", "new-", "metadata"},
		{"server", "https://unused.invalid", "server"},
		{"kubeconfig", kubeconfig, "kubeconfig"},
		{"current-context", "other", "current-context"},
	} {
		t.Run(tc.field+" replacement", func(t *testing.T) {
			before := inputs[tc.field]
			inputs[tc.field] = tc.value
			defer func() { inputs[tc.field] = before }()
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			step := policyStep(t, plan, runtime.DecisionReplace)
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
		})
	}
	inputs["prefix"], inputs["name"] = nil, "generated-actual"
	switchMode := planPolicy(t, executor, runtime.DecisionReplace)
	require.NoError(t, applyPolicy(t, executor, switchMode))
	calls := api.operations()
	require.Equal(t, []string{"GET generated-actual", "DELETE generated-actual",
		"POST generated-actual"}, calls[len(calls)-3:])
	inputs["name"] = "renamed"
	planPolicy(t, executor, runtime.DecisionReplace)
	inputs["name"] = "generated-actual"
	api.mu.Lock()
	delete(api.objects, "generated-actual")
	api.mu.Unlock()
	api.operations()
	planPolicy(t, executor, runtime.DecisionCreate)
	require.Equal(t, []string{"GET generated-actual"}, api.operations())
}

func TestValidatingAdmissionPolicySavedNoOpAndHistoricalDestroy(t *testing.T) {
	lib := policyLibrary(t)
	api, url := policyPlanServer(t)
	store, err := local.NewStore(t.TempDir(), "policy", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := map[string]any{
		"server": url, "token": "current", "prefix": nil, "name": nil,
		"match": nil, "label": "initial", "failure": "Wrong", "update-timeout": nil,
		"kubeconfig": nil, "current-context": nil,
	}
	previous := lib.Constraints
	lib.Constraints = nil
	executor := policyExecutor(t, "testdata/ub/policy.ub", lib, store, inputs)
	require.NoError(t, applyPolicy(t, executor, planPolicy(t, executor, runtime.DecisionCreate)))
	require.Equal(t, []string{"POST actual"}, api.operations())
	noop := planPolicy(t, executor, runtime.DecisionNoOp)
	require.Equal(t, []string{"GET actual"}, api.operations())
	lib.Constraints = previous
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints[")
	require.ErrorContains(t, err, "spec.failure-policy must be Fail or Ignore")
	require.Equal(t, []string{"GET actual"}, api.operations())
	err = applyPolicy(t, executor, noop)
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints[")
	require.ErrorContains(t, err, "metadata.name")
	require.Empty(t, api.operations())

	inputs["name"], inputs["failure"] = "actual", "Fail"
	reconciled := planPolicy(t, executor, runtime.DecisionUpdate)
	require.Empty(t, policyStep(t, reconciled, runtime.DecisionUpdate).ReplacementReasons)
	require.Equal(t, []string{"GET actual"}, api.operations())

	inputs["name"], inputs["failure"] = nil, "Wrong"
	executor.Destroy = true
	destroy := planPolicy(t, executor, runtime.DecisionDestroy)
	require.NoError(t, applyPolicy(t, executor, destroy))
	require.Equal(t, []string{"GET actual", "GET actual", "DELETE actual"}, api.operations())
}

func TestValidatingAdmissionPolicyHistoricalTimeoutDestroy(t *testing.T) {
	lib := policyLibrary(t)
	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{name: "zero read", field: "read-timeout", value: "0s"},
		{name: "malformed read", field: "read-timeout", value: "bad-duration"},
		{name: "zero delete", field: "delete-timeout", value: "0s"},
		{name: "malformed delete", field: "delete-timeout", value: "bad-duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, url := policyPlanServer(t)
			store, err := local.NewStore(t.TempDir(), "policy", "test", encrypters.Noop{})
			require.NoError(t, err)
			executor := policyExecutor(t, "testdata/ub/policy.ub", lib, store, map[string]any{
				"server": url, "token": "current", "prefix": "historical-", "name": nil,
				"match": nil, "label": "initial", "failure": "Fail", "update-timeout": nil,
				"kubeconfig": nil, "current-context": nil,
			})
			created := planPolicy(t, executor, runtime.DecisionCreate)
			require.NoError(t, applyPolicy(t, executor, created))
			require.Equal(t, []string{"POST historical-actual"}, api.operations())

			snapshot, err := store.Current()
			require.NoError(t, err)
			entry := snapshot.Find("resource.subject")
			require.NotNil(t, entry)
			entry.Inputs[tc.field] = tc.value
			rev, err := store.Write(snapshot)
			require.NoError(t, err)
			require.NoError(t, store.SetCurrent(rev))

			executor.Inputs["server"] = "https://127.0.0.1:1"
			executor.Destroy = true
			destroy := planPolicy(t, executor, runtime.DecisionDestroy)
			step := policyStep(t, destroy, runtime.DecisionDestroy)
			require.Equal(t, tc.value, step.PriorInputs[tc.field])
			require.Equal(t, new(url+"/historical-actual-uid"), step.ExpectedStableID)
			require.Equal(t, []string{"GET historical-actual"}, api.operations())

			require.NoError(t, applyPolicy(t, executor, destroy))
			require.Equal(t, []string{"GET historical-actual", "DELETE historical-actual"},
				api.operations())
			api.mu.Lock()
			gotDelete := api.lastDelete
			api.mu.Unlock()
			gotDelete.TypeMeta = metav1.TypeMeta{}
			require.Equal(t, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: new(types.UID("historical-actual-uid"))},
			}, gotDelete)
			snapshot, err = store.Current()
			require.NoError(t, err)
			require.Nil(t, snapshot.Find("resource.subject"))
		})
	}
}

func TestValidatingAdmissionPolicyChecksKnownMatchPolicyWithPendingSibling(t *testing.T) {
	lib := policyLibrary(t)
	api, url := policyPlanServer(t)
	store, err := local.NewStore(t.TempDir(), "policy", "test", encrypters.Noop{})
	require.NoError(t, err)
	executor := policyExecutor(t, "testdata/ub/policy-known-match.ub", lib, store,
		map[string]any{"server": url, "known": true})
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints[")
	require.ErrorContains(t, err, "spec.match-constraints.match-policy must be Exact or Equivalent")
	require.Empty(t, api.operations())
}

func TestValidatingAdmissionPolicyDeferredReplacement(t *testing.T) {
	lib := policyLibrary(t)
	for _, tc := range []struct {
		name       string
		seedName   string
		diagnostic string
	}{
		{"invalid match policy", "Wrong", "spec.match-constraints.match-policy"},
		{"invalid generated prefix", "Empty", "metadata.generate-name"},
		{"valid generated prefix and match policy", "Exact", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, url := policyPlanServer(t)
			api.seedName = tc.seedName
			store, err := local.NewStore(t.TempDir(), "policy", "test", encrypters.Noop{})
			require.NoError(t, err)
			initial := policyExecutor(t, "testdata/ub/policy.ub", lib, store, map[string]any{
				"server": url, "token": "current", "prefix": nil, "name": "initial",
				"match": nil, "label": "initial", "failure": "Fail", "update-timeout": nil,
				"kubeconfig": nil, "current-context": nil,
			})
			require.NoError(t, applyPolicy(t, initial, planPolicy(t, initial, runtime.DecisionCreate)))
			require.Equal(t, []string{"POST initial"}, api.operations())
			executor := policyExecutor(t, "testdata/ub/policy-pending.ub", lib, store,
				map[string]any{"server": url})
			plan := planPolicy(t, executor, runtime.DecisionReplace)
			step := policyStep(t, plan, runtime.DecisionReplace)
			require.Equal(t, map[string][]string{
				"metadata": {"resource.seed.name"}, "spec": {"resource.seed.name"},
			}, step.UnresolvedInputs)
			require.Equal(t, []string{"metadata"}, step.PendingReplacementReasons)
			require.Equal(t, []string{"GET initial"}, api.operations())
			err = applyPolicy(t, executor, plan)
			if tc.diagnostic != "" {
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, "schema: constraints[")
				require.ErrorContains(t, err, tc.diagnostic)
				require.Equal(t, []string{"POST " + tc.seedName}, api.operations())
				snapshot, err := store.Current()
				require.NoError(t, err)
				require.Equal(t, "initial", snapshot.Find("resource.subject").Outputs["name"])
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{
				"POST Exact", "GET initial", "DELETE initial", "POST resolved-actual", "GET Exact",
			}, api.operations())
			api.mu.Lock()
			defer api.mu.Unlock()
			require.Equal(t, "resolved-", api.lastRequest.GenerateName)
			require.Equal(t, &admissionv1.MatchResources{MatchPolicy: new(admissionv1.Exact)},
				api.lastRequest.Spec.MatchConstraints)
		})
	}
}

func TestValidatingAdmissionPolicyRejectsUnknownValidationFieldBeforeMutation(t *testing.T) {
	lib := policyLibrary(t)
	api, url := policyPlanServer(t)
	store, err := local.NewStore(t.TempDir(), "policy", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := map[string]any{"server": url}
	valid := policyExecutor(t, "testdata/ub/policy-validations.ub", lib, store, inputs)
	plan := planPolicy(t, valid, runtime.DecisionCreate)
	invalid := policyExecutor(t, "testdata/ub/policy-validation-reason.ub", lib, store, inputs)
	invalidPlan := planPolicy(t, invalid, runtime.DecisionCreate)
	require.Empty(t, api.operations())
	err = applyPolicy(t, invalid, invalidPlan)
	require.ErrorContains(t, err, "Spec.Validations[0]")
	require.ErrorContains(t, err, "reason")
	require.Empty(t, api.operations())
	require.NoError(t, applyPolicy(t, valid, plan))
	require.Equal(t, []string{"POST fixture"}, api.operations())
	api.mu.Lock()
	validations := api.lastRequest.Spec.Validations
	api.mu.Unlock()
	require.Equal(t, []admissionv1.Validation{{Expression: "true", Message: "test"}}, validations)

	_, err = invalid.Plan(t.Context())
	require.ErrorContains(t, err, "Spec.Validations[0]")
	require.ErrorContains(t, err, "reason")
	require.Empty(t, api.operations())
}
