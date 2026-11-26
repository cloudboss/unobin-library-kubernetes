package dynamic

import (
	"context"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func waitLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.wait": schema.Actions["wait"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{"action.wait": schema.Actions["wait"].Defaults}
	return lib
}

func waitSyntax(t *testing.T, fixture string) *syntax.File {
	t.Helper()
	path := filepath.Join("testdata/ub/wait", fixture, "factory.ub")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(path, source)
	require.NoError(t, err)
	return parsed
}

func waitExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	parsed := waitSyntax(t, fixture)
	libs := map[string]*runtime.Library{"dynamic": lib}
	store, err := local.NewStore(t.TempDir(), "wait", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Store: store,
	}
}

func waitSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return saved
}

type waitCase struct {
	Inputs map[string]any
	Valid  bool
}

func waitCases(t *testing.T) map[string]waitCase {
	t.Helper()

	entries, err := os.ReadDir("testdata/ub/wait/literal")
	require.NoError(t, err)
	cases := make(map[string]waitCase, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		parsed := waitSyntax(t, "literal/"+name)
		dag := runtime.BuildSyntaxDAG(parsed.Factory.Body, nil)
		inputs, err := runtime.LiteralConstraintInputs(dag.Nodes["action.target"].Body, nil)
		require.NoError(t, err)
		tc := waitCase{Inputs: inputs, Valid: strings.HasPrefix(name, "valid")}

		for _, key := range []string{
			"expected-uid", "timeout", "exists", "deleted", "condition", "field",
		} {
			if _, ok := tc.Inputs[key]; !ok {
				tc.Inputs[key] = nil
			}
		}
		cases[name] = tc
	}
	return cases
}

func TestWaitNativeLiteralAndEvaluatedInputs(t *testing.T) {
	lib := waitLibrary(t)
	for name, tc := range waitCases(t) {
		t.Run(name, func(t *testing.T) {
			parsed := waitSyntax(t, "literal/"+name)
			checker := check.NewSyntax(parsed.Factory.Body,
				map[string]*runtime.Library{"dynamic": lib}, nil, "")
			diagnostics := checker.LiteralConstraints()
			if tc.Valid {
				require.Empty(t, checker.References(nil).Errors())
				require.Empty(t, diagnostics.Errors())
			} else {
				require.NotEmpty(t, diagnostics.Errors())
				require.Contains(t, diagnostics.Error(), "action.target")
				require.Contains(t, diagnostics.Error(), "constraints[")
			}
			executor := waitExecutor(t, lib, "known")
			executor.Inputs = tc.Inputs
			executor.Inputs["server"] = "http://127.0.0.1:1"
			plan, err := executor.Plan(t.Context())
			if tc.Valid {
				require.NoError(t, err)
				for _, step := range plan.Steps {
					if step.Address == "action.target" {
						require.Equal(t, []string{"field"}, step.SensitiveInputs)
						require.Empty(t, step.SensitiveOutputs)
					}
				}
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
			}
		})
	}
}

type waitSeed struct{ Settings map[string]any }
type waitSeedOutput struct{ Value map[string]any }

func (a *waitSeed) Run(context.Context, *config.Configuration) (*waitSeedOutput, error) {
	return &waitSeedOutput{Value: a.Settings}, nil
}

type waitHistorical struct {
	APIVersion  string
	Kind        string
	Metadata    svc.ObjectLookupMetadata
	ExpectedUID *string
	Timeout     *string
	Exists      *bool
	Deleted     *bool
	Condition   *svc.WaitCondition
	Field       *map[string]any
}

func (a *waitHistorical) Run(
	context.Context, *config.Configuration,
) (*svc.WaitActionOutput, error) {
	return &svc.WaitActionOutput{Server: "historical", APIVersion: a.APIVersion, Kind: a.Kind,
		Name: a.Metadata.Name, Namespace: "default", Predicate: "exists", UID: new("uid")}, nil
}

func TestWaitNativeDeferredAndSavedSkipInputs(t *testing.T) {
	lib := waitLibrary(t)
	real := lib.Actions["wait"]
	rules := lib.Constraints
	lib.Actions["seed"] = runtime.MakeAction[waitSeed, *waitSeedOutput, *config.Configuration]()
	for name, tc := range waitCases(t) {
		t.Run(name, func(t *testing.T) {
			lib.Actions["wait"] = real
			lib.Constraints = rules
			api, calls := newLabelsHTTP(t, func(r *http.Request) (int, any) {
				if tc.Inputs["deleted"] == true {
					return http.StatusNotFound, map[string]any{"kind": "Status", "apiVersion": "v1",
						"status": "Failure", "reason": "NotFound", "code": 404}
				}
				return http.StatusOK, waitMatchingObject(tc.Inputs)
			})
			deferred := waitExecutor(t, lib, "deferred")
			deferred.Inputs = map[string]any{"server": api, "settings": tc.Inputs}
			plan, err := deferred.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, calls())
			_, err = deferred.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
			if tc.Valid {
				require.NoError(t, err)
				require.Equal(t, []string{"GET /api/v1/namespaces/default/configmaps/example"}, calls())
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
				require.Empty(t, calls())
			}
			lib.Actions["wait"] = runtime.MakeAction[
				waitHistorical, *svc.WaitActionOutput, *config.Configuration,
			]()
			lib.Constraints = nil
			executor := waitExecutor(t, lib, "explicit")
			executor.Inputs = tc.Inputs
			executor.Inputs["server"] = "http://127.0.0.1:1"
			executor.Inputs["trigger"] = "fixed"
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
			require.NoError(t, err)
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			var decisions []runtime.Decision
			for _, step := range plan.Steps {
				if step.Address == "action.target" {
					decisions = append(decisions, step.Decision)
				}
			}
			require.Equal(t, []runtime.Decision{runtime.DecisionSkip}, decisions)
			saved := waitSavedPlan(t, plan)
			lib.Actions["wait"] = real
			lib.Constraints = rules
			_, err = executor.Plan(t.Context())
			if tc.Valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
			}
			_, err = executor.ApplyPlan(t.Context(), saved)
			if tc.Valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
			}
		})
	}
}

func TestWaitTriggersAndDeferredExecution(t *testing.T) {
	lib := waitLibrary(t)
	lib.Actions["seed"] = runtime.MakeAction[waitSeed, *waitSeedOutput, *config.Configuration]()
	for _, fixture := range []string{"known", "explicit", "always", "deferred"} {
		t.Run(fixture, func(t *testing.T) {
			var gets atomic.Int32
			api, _ := newLabelsHTTP(t, func(r *http.Request) (int, any) {
				gets.Add(1)
				return http.StatusOK, map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
					"metadata": map[string]any{"name": "example", "namespace": "default", "uid": "uid",
						"resourceVersion": "42"}}
			})
			executor := waitExecutor(t, lib, fixture)
			settings := map[string]any{"api-version": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": "example"}, "exists": true,
				"expected-uid": nil, "timeout": nil, "deleted": nil, "condition": nil, "field": nil}
			executor.Inputs = map[string]any{"server": api, "trigger": "first"}
			maps.Copy(executor.Inputs, settings)
			executor.Inputs["settings"] = settings
			apply := func() {
				t.Helper()
				before := gets.Load()
				plan, err := executor.Plan(t.Context())
				require.NoError(t, err)
				require.Equal(t, before, gets.Load(), "planning contacted Kubernetes")
				result, err := executor.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
				require.NoError(t, err)
				require.Equal(t, map[string]any{"result": map[string]any{
					"server": api, "api-version": "v1", "kind": "ConfigMap", "name": "example",
					"namespace": "default", "predicate": "exists", "uid": "uid", "resource-version": "42",
				}}, result.Outputs)
			}
			apply()
			require.Equal(t, int32(1), gets.Load())
			apply()
			want := int32(1)
			if fixture == "always" {
				want++
			}
			require.Equal(t, want, gets.Load())
			executor.Inputs["timeout"] = "4s"
			settings["timeout"] = "4s"
			apply()
			if fixture != "explicit" {
				want++
			}
			require.Equal(t, want, gets.Load())
			if fixture == "explicit" {
				executor.Inputs["trigger"] = "second"
				apply()
				want++
				require.Equal(t, want, gets.Load())
			}
			executor.Destroy = true
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, want, gets.Load())
		})
	}
}

func waitMatchingObject(inputs map[string]any) map[string]any {
	obj := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "example", "namespace": "default", "uid": "uid",
			"resourceVersion": "42"},
	}
	if condition, ok := inputs["condition"].(map[string]any); ok {
		status := condition["status"]
		if status == nil {
			status = "True"
		}
		obj["status"] = map[string]any{"conditions": []any{
			map[string]any{"type": condition["type"], "status": status},
		}}
	}
	if field, ok := inputs["field"].(map[string]any); ok && field["exists"] != false {
		var value any = true
		for _, key := range []string{"string-equals", "boolean-equals", "integer-equals"} {
			if v, exists := field[key]; exists {
				value = v
			}
		}
		path := field["path"].([]any)
		parent := obj
		for i, part := range path {
			key := part.(string)
			if i == len(path)-1 {
				parent[key] = value
				break
			}
			child := map[string]any{}
			parent[key] = child
			parent = child
		}
	}
	return obj
}

func TestWaitStructuralSchema(t *testing.T) {
	lib := waitLibrary(t)
	for _, fixture := range []string{"unknown", "missing-name", "missing-kind", "missing-path",
		"wrong-selector"} {
		t.Run(fixture, func(t *testing.T) {
			parsed := waitSyntax(t, "structural/"+fixture)
			checker := check.NewSyntax(parsed.Factory.Body,
				map[string]*runtime.Library{"dynamic": lib}, nil, "")
			diagnostics := checker.References(nil)
			require.NotEmpty(t, diagnostics.Errors())
			require.Contains(t, diagnostics.Error(), "factory.ub")
		})
	}
}

func TestWaitRuntimeTimeout(t *testing.T) {
	lib := waitLibrary(t)
	api, calls := newLabelsHTTP(t, func(r *http.Request) (int, any) {
		<-r.Context().Done()
		return http.StatusNotFound, map[string]any{"kind": "Status", "apiVersion": "v1",
			"reason": "NotFound", "status": "Failure", "code": 404}
	})
	executor := waitExecutor(t, lib, "timeout")
	executor.Inputs = map[string]any{
		"server": api, "api-version": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "example"}, "expected-uid": nil, "timeout": "5s",
		"exists": true, "deleted": nil, "condition": nil, "field": nil,
	}
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, calls())
	start := time.Now()
	_, err = executor.ApplyPlan(t.Context(), waitSavedPlan(t, plan))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "timeout")
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, []string{"GET /api/v1/namespaces/default/configmaps/example"}, calls())
}
