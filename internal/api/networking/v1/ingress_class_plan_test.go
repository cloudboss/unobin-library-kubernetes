package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func compileIngressClassFactory(t *testing.T, fixture string) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/networking-v1-ingress-class")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, fixture+"-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	path, err := filepath.Abs(filepath.Join("testdata/ub/ingress-class", fixture, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: path, OutDir: work, StackName: "ingress-class-" + fixture,
		Version: "v0.0.0", CLIVersion: "v0.13.0-a.11", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	return result.BinaryPath
}

func runIngressClassFactory(t *testing.T, binary, work string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func planCompiledIngressClass(
	t *testing.T, binary, work string, inputs map[string]any,
) (string, error) {
	t.Helper()
	config, err := os.ReadFile("testdata/ub/ingress-class/config.ub")
	require.NoError(t, err)
	payload, err := ub.Marshal(inputs)
	require.NoError(t, err)
	config = bytes.Replace(config, []byte("'INGRESS_CLASS_INPUTS'"), payload, 1)
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), config, 0o600))
	out, err := runIngressClassFactory(t, binary, work, "pin", "-c", "config.ub")
	require.NoError(t, err, "%s", out)
	return runIngressClassFactory(t, binary, work, "plan", "-c", "config.ub", "-o", "plan.ubp")
}

func compiledIngressClassStep(t *testing.T, work string) runtime.PlanStep {
	t.Helper()
	return ingressClassPlanStep(t, openIngressClassPlan(t, work), "resource.class")
}

func openIngressClassPlan(t *testing.T, work string) *runtime.PlanFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(data, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
}

func ingressClassPlanStep(t *testing.T, plan *runtime.PlanFile, address string) runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == address {
			return step
		}
	}
	t.Fatalf("plan has no step for %s", address)
	return runtime.PlanStep{}
}

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

func TestIngressClassNativeEvaluatedInputs(t *testing.T) {
	binary := compileIngressClassFactory(t, "inputs")
	evaluatedInputs := func(input string, value any) map[string]any {
		inputs := map[string]any{
			"server":   "https://unused.invalid",
			"metadata": map[string]any{"generate-name": "example-"},
			"spec":     map[string]any{"controller": "example.com/controller"},
		}
		inputs[input] = value
		return inputs
	}
	for _, tc := range []struct {
		name  string
		input string
		value any
	}{
		{"neither identity", "metadata", map[string]any{}},
		{"two identities", "metadata", map[string]any{"name": "explicit", "generate-name": "next-"}},
		{"empty name", "metadata", map[string]any{"name": ""}},
		{"DNS syntax", "metadata", map[string]any{"name": "Example"}},
		{"long name", "metadata", map[string]any{"name": strings.Repeat("a", 254)}},
		{"empty prefix", "metadata", map[string]any{"generate-name": ""}},
		{"long prefix", "metadata", map[string]any{"generate-name": strings.Repeat("a", 64)}},
		{"label key", "metadata", map[string]any{
			"name": "valid", "labels": map[string]any{"Upper/key": "valid"},
		}},
		{"label value", "metadata", map[string]any{
			"name": "valid", "labels": map[string]any{"key": "invalid value"},
		}},
		{"annotation key", "metadata", map[string]any{
			"name": "valid", "annotations": map[string]any{"invalid/key/again": "valid"},
		}},
		{"ignore annotations", "ignore-annotations", []any{"["}},
		{"ignore labels", "ignore-labels", []any{"["}},
		{"empty scope", "spec", ingressClassScopeInput("")},
		{"lowercase scope", "spec", ingressClassScopeInput("namespace")},
		{"unsupported scope", "spec", ingressClassScopeInput("Global")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := evaluatedInputs(tc.input, tc.value)
			out, err := planCompiledIngressClass(t, binary, t.TempDir(), inputs)
			require.Error(t, err, "%s", out)
			require.Contains(t, out, "resource.class: schema: constraints[")
		})
	}
	for _, tc := range []struct {
		name string
		spec map[string]any
	}{
		{"omitted parameters", map[string]any{"controller": "example.com/controller"}},
		{"omitted scope", map[string]any{
			"parameters": map[string]any{"kind": "ConfigMap", "name": "settings"},
		}},
		{"Cluster scope", ingressClassScopeInput("Cluster")},
		{"Namespace scope", ingressClassScopeInput("Namespace")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			out, err := planCompiledIngressClass(t, binary, work, evaluatedInputs("spec", tc.spec))
			require.NoError(t, err, "%s", out)
			step := compiledIngressClassStep(t, work)
			require.Equal(t, runtime.DecisionCreate, step.Decision)
		})
	}
}

func ingressClassScopeInput(scope string) map[string]any {
	return map[string]any{"parameters": map[string]any{
		"kind": "ConfigMap", "name": "settings", "scope": scope,
	}}
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

func TestIngressClassDeferredConstraints(t *testing.T) {
	binary := compileIngressClassFactory(t, "pending")
	t.Run("reject known scope beside pending name", func(t *testing.T) {
		out, err := planCompiledIngressClass(t, binary, t.TempDir(), map[string]any{
			"server": "https://unused.invalid", "seed-prefix": "seed-", "scope": "cluster",
		})
		require.Error(t, err, "%s", out)
		require.Contains(t, out, "resource.class: schema: constraints[")
		require.Contains(t, out, "spec.parameters.scope must be Cluster or Namespace")
	})
	for _, tc := range []struct {
		name       string
		seedPrefix string
		want       []string
	}{
		{"valid", "seed-", []string{
			"POST " + ingressClassPath, "POST " + ingressClassPath,
			"GET " + ingressClassPath + "/seed-actual",
		}},
		{"invalid", "invalid-", []string{
			"POST " + ingressClassPath,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newIngressClassPlanAPI(t)
			work := t.TempDir()
			out, err := planCompiledIngressClass(t, binary, work,
				map[string]any{"server": api.server, "seed-prefix": tc.seedPrefix})
			require.NoError(t, err, "%s", out)
			require.Empty(t, api.requests())
			step := compiledIngressClassStep(t, work)
			require.Equal(t, map[string][]string{
				"spec": {"resource.seed.name"},
			}, step.UnresolvedInputs)
			out, err = runIngressClassFactory(t, binary, work, "apply", "plan.ubp")
			if tc.name == "valid" {
				require.NoError(t, err, "%s", out)
				created := api.class("deferred")
				require.NotNil(t, created)
				require.Equal(t, &networkingv1.IngressClassParametersReference{
					Kind: "ConfigMap", Name: "settings", Scope: new("Cluster"),
				}, created.Spec.Parameters)
			} else {
				require.Error(t, err, "%s", out)
				require.Contains(t, out, "resource.class: schema: constraints[")
				require.Contains(t, out, "spec.parameters.scope must be Cluster or Namespace")
				require.Nil(t, api.class("deferred"))
			}
			require.Equal(t, tc.want, api.requests())
		})
	}
}
