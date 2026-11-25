package v1

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func ingressClassConstraints(t *testing.T) map[string][]lang.ConstraintSpec {
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
	return map[string][]lang.ConstraintSpec{
		"resource.ingress-class": schema.Resources["ingress-class"].Constraints,
	}
}

func ingressClassExecutor(
	t *testing.T, fixture string, constraints map[string][]lang.ConstraintSpec,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/ingress-class", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"networking": {
		Name: "kubernetes-networking-v1", Configuration: config.LibraryConfiguration(),
		Constraints: constraints,
		Resources: map[string]runtime.ResourceRegistration{"ingress-class": runtime.MakeResource[
			IngressClassResource, *IngressClassResourceOutput, *config.Configuration,
		]((&IngressClassResource{}).ResourceDefinition())},
	}}
	store, err := local.NewStore(t.TempDir(), fixture, "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Store: store, Factory: state.FactoryInfo{Name: fixture, Version: "v0"},
		Inputs: map[string]any{
			"server": "https://unused.invalid", "token": "first",
			"kubeconfig": "", "current-context": "", "ignore-labels": []any{},
			"ignore-annotations": []any{},
			"metadata":           map[string]any{"generate-name": "example-"},
			"spec":               map[string]any{"controller": "example.com/controller"},
		},
	}
}

func planIngressClass(t *testing.T, e *runtime.Executor, want runtime.Decision) *runtime.Plan {
	t.Helper()
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	var decisions []runtime.Decision
	for _, step := range p.Steps {
		if step.Address == "resource.class" {
			decisions = append(decisions, step.Decision)
		}
	}
	require.Equal(t, []runtime.Decision{want}, decisions)
	return p
}

func applyIngressClassPlan(t *testing.T, e *runtime.Executor, p *runtime.Plan) error {
	t.Helper()
	raw, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(raw)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), file)
	return err
}

func TestIngressClassLifecyclePlans(t *testing.T) {
	var stored *networkingv1.IngressClass
	var writes []string
	var getStatus int
	var token string
	c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		token = req.Header.Get("Authorization")
		switch req.Method {
		case "POST", "PUT":
			writes = append(writes, req.Method+" "+req.URL.Path)
			var obj networkingv1.IngressClass
			require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
			if req.Method == "PUT" && obj.Spec.Controller != stored.Spec.Controller {
				apiError(t, w, 422)
				return
			}
			if obj.Name == "" {
				obj.Name = obj.GenerateName + "actual"
			}
			obj.UID = types.UID(obj.Name + "-uid")
			obj.Namespace = ""
			stored = &obj
		case "DELETE":
			writes = append(writes, req.Method+" "+req.URL.Path)
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, &metav1.Preconditions{UID: &stored.UID}, options.Preconditions)
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		case "GET":
			if getStatus != 0 {
				apiError(t, w, getStatus)
				return
			}
		}
		if stored == nil {
			apiError(t, w, 404)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	e := ingressClassExecutor(t, "inputs", ingressClassConstraints(t))
	e.Inputs["server"] = *c.Server
	const kubeconfig = "../../../../internal/client/testdata/contexts.yaml"
	e.Inputs["kubeconfig"], e.Inputs["current-context"] = kubeconfig, "dev"
	p := planIngressClass(t, e, runtime.DecisionCreate)
	require.NoError(t, applyIngressClassPlan(t, e, p))
	require.Equal(t, []string{"POST " + ingressClassPath}, writes)
	require.Equal(t, "example-actual", stored.Name)
	require.NoError(t, applyIngressClassPlan(t, e, planIngressClass(t, e, runtime.DecisionNoOp)))
	stored.Generation, stored.ResourceVersion = 80, "volatile"
	planIngressClass(t, e, runtime.DecisionNoOp)
	require.Equal(t, []string{"POST " + ingressClassPath}, writes)

	e.Inputs["metadata"] = map[string]any{"generate-name": "next-"}
	planIngressClass(t, e, runtime.DecisionReplace)
	e.Inputs["metadata"] = map[string]any{"generate-name": "example-"}
	for _, tc := range []struct{ key, changed string }{
		{"server", "https://unused.invalid"},
		{"kubeconfig", "../../../../internal/client/testdata/../testdata/contexts.yaml"},
		{"current-context", "prod"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			old := e.Inputs[tc.key]
			e.Inputs[tc.key] = tc.changed
			planIngressClass(t, e, runtime.DecisionReplace)
			e.Inputs[tc.key] = old
		})
	}

	e.Inputs["spec"] = map[string]any{"controller": "example.com/changed"}
	p = planIngressClass(t, e, runtime.DecisionUpdate)
	err := applyIngressClassPlan(t, e, p)
	require.True(t, apierrors.IsInvalid(err), "%v", err)
	require.Equal(t, "example.com/controller", stored.Spec.Controller)
	require.Equal(t, []string{
		"POST " + ingressClassPath, "PUT " + ingressClassPath + "/example-actual",
	}, writes)

	e.Inputs["spec"] = map[string]any{"controller": "example.com/controller"}
	planIngressClass(t, e, runtime.DecisionNoOp)
	e.Inputs["metadata"] = map[string]any{
		"generate-name": "example-", "labels": map[string]any{"key": "value"},
	}
	e.Inputs["token"] = "current"
	p = planIngressClass(t, e, runtime.DecisionUpdate)
	require.NoError(t, applyIngressClassPlan(t, e, p))
	require.Equal(t, "Bearer current", token)
	require.Equal(t, map[string]string{"key": "value"}, stored.Labels)
	require.Equal(t, types.UID("example-actual-uid"), stored.UID)
	require.Equal(t, []string{
		"POST " + ingressClassPath, "PUT " + ingressClassPath + "/example-actual",
		"PUT " + ingressClassPath + "/example-actual",
	}, writes)

	e.Inputs["metadata"] = map[string]any{"name": "explicit"}
	p = planIngressClass(t, e, runtime.DecisionReplace)
	require.NoError(t, applyIngressClassPlan(t, e, p))
	require.Equal(t, types.UID("explicit-uid"), stored.UID)
	require.Equal(t, []string{
		"POST " + ingressClassPath, "PUT " + ingressClassPath + "/example-actual",
		"PUT " + ingressClassPath + "/example-actual",
		"DELETE " + ingressClassPath + "/example-actual", "POST " + ingressClassPath,
	}, writes)
	e.Inputs["metadata"] = map[string]any{"name": "next-explicit"}
	planIngressClass(t, e, runtime.DecisionReplace)
	e.Inputs["metadata"] = map[string]any{"name": "explicit"}

	getStatus = http.StatusForbidden
	_, err = e.Plan(t.Context())
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	getStatus = 0
	stored.UID = "unrelated"
	_, err = e.Plan(t.Context())
	require.ErrorContains(t, err, "UID changed")
	stored = nil
	planIngressClass(t, e, runtime.DecisionCreate)
}

func TestIngressClassSavedPlanConstraintsAndHistoricalDelete(t *testing.T) {
	constraints := ingressClassConstraints(t)
	var stored *networkingv1.IngressClass
	var methods []string
	c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		switch req.Method {
		case "POST":
			stored = &networkingv1.IngressClass{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.Name, stored.UID = "historical", "historical-uid"
		case "DELETE":
			require.Equal(t, ingressClassPath+"/historical", req.URL.Path)
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, &metav1.Preconditions{UID: &stored.UID}, options.Preconditions)
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
	e := ingressClassExecutor(t, "inputs", nil)
	e.Inputs["server"] = *c.Server
	e.Inputs["metadata"] = map[string]any{}
	require.NoError(t, applyIngressClassPlan(t, e, planIngressClass(t, e, runtime.DecisionCreate)))
	for _, tc := range []struct {
		metadata map[string]any
		decision runtime.Decision
	}{
		{map[string]any{}, runtime.DecisionNoOp},
		{map[string]any{"name": "explicit", "generate-name": "next-"}, runtime.DecisionReplace},
	} {
		t.Run(string(tc.decision), func(t *testing.T) {
			lib := e.Libraries["networking"]
			lib.Constraints = nil
			e.Inputs["metadata"] = tc.metadata
			p := planIngressClass(t, e, tc.decision)
			lib.Constraints = constraints
			before := len(methods)
			err := applyIngressClassPlan(t, e, p)
			require.ErrorContains(t, err, "resource.class: schema: constraints[0]")
			require.Empty(t, methods[before:])
			_, err = e.Plan(t.Context())
			require.ErrorContains(t, err, "resource.class: schema: constraints[0]")
			require.Subset(t, []string{"GET"}, methods[before:])
		})
	}
	e.Inputs["metadata"] = map[string]any{}
	e.Destroy = true
	p := planIngressClass(t, e, runtime.DecisionDestroy)
	before := len(methods)
	require.NoError(t, applyIngressClassPlan(t, e, p))
	require.Equal(t, []string{"GET", "DELETE", "GET"}, methods[before:])
	require.Nil(t, stored)
}
