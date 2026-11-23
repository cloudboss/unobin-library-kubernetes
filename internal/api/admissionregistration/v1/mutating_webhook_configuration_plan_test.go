package v1

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const mutatingWebhookExport = "mutating-webhook-configuration"
const mutatingWebhookConstraintKey = "resource." + mutatingWebhookExport

type mutatingWebhookPlanner struct {
	t      *testing.T
	libs   map[string]*runtime.Library
	store  *local.Store
	inputs map[string]any
}

func newMutatingWebhookPlanner(t *testing.T, cfg *config.Configuration) *mutatingWebhookPlanner {
	t.Helper()
	schema, err := readWebhookSchema()
	require.NoError(t, err)
	model := *schema.Resources[mutatingWebhookExport]
	model.EvaluatedInputs, model.Inputs = model.Inputs, nil
	lib := &runtime.Library{
		Name:          "kubernetes-admissionregistration-v1",
		Configuration: config.LibraryConfiguration(),
		Schema: &runtime.LibrarySchema{
			Resources: map[string]*runtime.TypeSchema{mutatingWebhookExport: &model},
		},
		Constraints: map[string][]lang.ConstraintSpec{mutatingWebhookConstraintKey: model.Constraints},
		Resources: map[string]runtime.ResourceRegistration{
			mutatingWebhookExport: runtime.MakeResource[
				MutatingWebhookConfigurationResource, *MutatingWebhookConfigurationResourceOutput,
				*config.Configuration,
			]((&MutatingWebhookConfigurationResource{}).ResourceDefinition()),
		},
		Actions: map[string]runtime.ActionRegistration{
			"seed": runtime.MakeAction[webhookSeed, any, *config.Configuration](),
		},
	}
	store, err := local.NewStore(t.TempDir(), "webhook", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &mutatingWebhookPlanner{
		t: t, libs: map[string]*runtime.Library{"admission": lib}, store: store,
		inputs: map[string]any{
			"server":          *cfg.Server,
			"kubeconfig":      nil,
			"current-context": nil,
			"metadata":        map[string]any{"name": "example"},
			"webhook": []any{map[string]any{
				"name": "first.example.test", "client-config": map[string]any{
					"url": "https://first.example.test/check",
				},
				"admission-review-versions": []any{"v1", "v1beta1"}, "side-effects": "None",
			}},
			"ignore-annotations": []any{"^ignored"},
			"ignore-labels":      []any{"^ignored"},
			"endpoint":           "https://partial.example.test/check",
			"operation":          "UPDATE",
			"known-operation":    "CREATE",
			"policy":             "Never",
			"prefix":             "webhook-",
		},
	}
}

func (p *mutatingWebhookPlanner) executor(fixture string) *runtime.Executor {
	p.t.Helper()
	body, err := os.ReadFile("testdata/ub/mutating/" + fixture + "/factory.ub")
	require.NoError(p.t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(p.t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, p.libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: p.libs, Inputs: p.inputs, Store: p.store,
		Factory: state.FactoryInfo{Name: "webhook", Version: "v0", ContentRevision: "c0"},
	}
}

func (p *mutatingWebhookPlanner) plan(fixture string, want runtime.Decision) (
	*runtime.Executor, *runtime.PlanFile, *runtime.PlanStep,
) {
	p.t.Helper()
	executor := p.executor(fixture)
	file := saveWebhookPlan(p.t, executor)
	for i := range file.Steps {
		step := &file.Steps[i]
		if step.Address == "resource.example" {
			require.Equal(p.t, want, step.Decision)
			return executor, file, step
		}
	}
	p.t.Fatal("resource.example absent from plan")
	return nil, nil, nil
}

func (p *mutatingWebhookPlanner) apply(fixture string, want runtime.Decision) {
	p.t.Helper()
	executor, file, _ := p.plan(fixture, want)
	_, err := executor.ApplyPlan(p.t.Context(), file)
	require.NoError(p.t, err)
}

func (p *mutatingWebhookPlanner) metadata() map[string]any {
	p.t.Helper()
	return p.inputs["metadata"].(map[string]any)
}

func (p *mutatingWebhookPlanner) webhook() map[string]any {
	p.t.Helper()
	return p.inputs["webhook"].([]any)[0].(map[string]any)
}

func TestMutatingWebhookPlannedDefaultsSelectorsAndDrift(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	p.inputs["metadata"] = map[string]any{
		"generate-name": "webhook-",
		"annotations":   map[string]any{"ignored.example.test/tracked": "kept"},
		"labels":        map[string]any{"ignored/tracked": "kept"},
	}
	p.webhook()["namespace-selector"] = map[string]any{}
	p.webhook()["object-selector"] = map[string]any{"match-expressions": []any{
		map[string]any{"key": "test", "operator": "In", "values": []any{"b", "a", "b"}},
		map[string]any{"key": "other", "operator": "Exists"},
	}}
	p.apply("settled", runtime.DecisionCreate)
	require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())
	require.Equal(t, "webhook-xyz", server.snapshot().Name)
	require.Equal(t, &metav1.LabelSelector{}, server.snapshot().Webhooks[0].NamespaceSelector)
	require.Equal(t, []metav1.LabelSelectorRequirement{
		{Key: "test", Operator: metav1.LabelSelectorOpIn, Values: []string{"a", "b"}},
		{Key: "other", Operator: metav1.LabelSelectorOpExists},
	}, server.snapshot().Webhooks[0].ObjectSelector.MatchExpressions)

	server.change(func(object *admissionv1.MutatingWebhookConfiguration) {
		object.Annotations["internal.kubernetes.io/name"] = "hidden"
		object.Annotations["ignored.example.test/other"] = "hidden"
		object.Labels["ignored/other"] = "hidden"
		object.Generation = 43
		object.ResourceVersion = "controller-update"
		object.Webhooks[0].MatchConditions = []admissionv1.MatchCondition{
			{Name: "inert", Expression: "false"},
		}
		object.Webhooks[0].ObjectSelector.MatchExpressions[0].Values = []string{"b", "a", "a"}
	})
	delete(p.webhook(), "namespace-selector")
	p.webhook()["object-selector"].(map[string]any)["match-expressions"] = []any{
		map[string]any{"key": "test", "operator": "In", "values": []any{"b", "b", "a"}},
		map[string]any{"key": "other", "operator": "Exists", "values": []any{}},
	}
	p.webhook()["failure-policy"] = "Fail"
	p.webhook()["match-policy"] = "Equivalent"
	p.webhook()["reinvocation-policy"] = "Never"
	p.webhook()["timeout-seconds"] = int64(10)
	p.apply("settled", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())

	server.change(func(object *admissionv1.MutatingWebhookConfiguration) {
		object.Webhooks[0].ReinvocationPolicy = new(admissionv1.IfNeededReinvocationPolicy)
		object.Labels["ignored/tracked"] = "drifted"
	})
	p.apply("settled", runtime.DecisionUpdate)
	require.Equal(t, []string{"GET", "GET", "PATCH", "GET", "GET"}, server.takeCalls())
	require.Equal(t, admissionv1.NeverReinvocationPolicy,
		*server.snapshot().Webhooks[0].ReinvocationPolicy)
	require.Equal(t, "kept", server.snapshot().Labels["ignored/tracked"])
	require.Nil(t, server.snapshot().Webhooks[0].NamespaceSelector)
	require.Nil(t, server.snapshot().Webhooks[0].MatchConditions)
	p.apply("settled", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())

	delete(p.webhook(), "object-selector")
	p.webhook()["reinvocation-policy"] = "IfNeeded"
	p.apply("settled", runtime.DecisionUpdate)
	require.Nil(t, server.snapshot().Webhooks[0].ObjectSelector)
	require.Equal(t, admissionv1.IfNeededReinvocationPolicy,
		*server.snapshot().Webhooks[0].ReinvocationPolicy)
	require.Equal(t, "uid-original", string(server.snapshot().UID))
	p.apply("settled", runtime.DecisionNoOp)

	p.metadata()["generate-name"] = "new-prefix-"
	_, _, step := p.plan("settled", runtime.DecisionReplace)
	require.Equal(t, []string{"metadata.generate-name"}, step.ReplacementReasons)
	require.Equal(t, new(*cfg.Server+"/uid-original"), step.ExpectedStableID)
	delete(p.metadata(), "generate-name")
	p.metadata()["name"] = "webhook-xyz"
	_, _, step = p.plan("settled", runtime.DecisionReplace)
	require.Equal(t, []string{"metadata.generate-name", "metadata.name"}, step.ReplacementReasons)
	p.inputs["server"] = "http://127.0.0.1:1"
	_, _, step = p.plan("settled", runtime.DecisionReplace)
	require.Contains(t, step.ReplacementReasons, "server")
}

func TestMutatingWebhookPendingIdentity(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	p.inputs["metadata"] = map[string]any{"generate-name": "webhook-"}
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	exec, saved, step := p.plan("pending-identity", runtime.DecisionReplace)
	require.Equal(t, []runtime.Decision{
		runtime.DecisionNoOp, runtime.DecisionUpdate, runtime.DecisionReplace,
	}, step.AllowedDecisions)
	require.Equal(t, []string{"metadata.generate-name", "metadata.name"},
		step.PendingReplacementReasons)
	_, err := exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	require.Equal(t, "webhook-xyz", server.snapshot().Name)
}

func TestMutatingWebhookSavedPlanMetadataFilterEmptyProjection(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	server.change(func(obj *admissionv1.MutatingWebhookConfiguration) {
		obj.Annotations = map[string]string{"hidden/external": "value"}
		obj.Labels = map[string]string{"hidden/external": "value"}
	})
	p.inputs["ignore-annotations"] = []any{"^hidden/"}
	p.inputs["ignore-labels"] = p.inputs["ignore-annotations"]
	p.metadata()["annotations"] = map[string]any{"first": "annotation"}
	p.metadata()["labels"] = map[string]any{"first": "label"}
	p.apply("settled", runtime.DecisionUpdate)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-original"},
		{"op":"add","path":"/metadata/annotations","value":{"first":"annotation"}},
		{"op":"add","path":"/metadata/labels","value":{"first":"label"}}
	]`, server.patchBody())
	require.Equal(t, []string{"GET", "GET", "GET", "PATCH", "GET", "GET"}, server.takeCalls())
	live := server.snapshot()
	require.Equal(t, map[string]string{"first": "annotation"}, live.Annotations)
	require.Equal(t, map[string]string{"first": "label"}, live.Labels)
	require.Equal(t, "uid-original", string(live.UID))
}

func TestMutatingWebhookSavedPlanMetadataFilterRejectsNewUID(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	p.inputs["ignore-annotations"] = []any{"^hidden/"}
	exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
	server.takeCalls()
	server.change(func(obj *admissionv1.MutatingWebhookConfiguration) {
		obj.UID = "different"
	})
	_, err := exec.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "object example UID changed from uid-original to different")
	require.Equal(t, []string{"GET"}, server.takeCalls())
	require.Equal(t, "different", string(server.snapshot().UID))
}

func TestMutatingWebhookNativePartialAndSavedDeferred(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		replace         bool
	}{
		{"valid create", "UPDATE", false},
		{"invalid create", "update", false},
		{"invalid replacement", "update", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, cfg := newMutatingHTTP(t)
			p := newMutatingWebhookPlanner(t, cfg)
			want := runtime.DecisionCreate
			if tc.replace {
				p.apply("settled", runtime.DecisionCreate)
				p.metadata()["name"] = "changed"
				server.takeCalls()
				want = runtime.DecisionReplace
			}
			p.inputs["operation"] = tc.operation
			p.inputs["policy"] = "ifNeeded"
			_, err := p.executor("partial").Plan(t.Context())
			require.ErrorContains(t, err, "resource.example: schema: constraints")
			require.ErrorContains(t, err, "reinvocation-policy must be Never or IfNeeded")
			server.takeCalls()

			p.inputs["policy"] = "IfNeeded"
			p.inputs["known-operation"] = "create"
			_, err = p.executor("partial").Plan(t.Context())
			require.ErrorContains(t, err, "invalid rule operation")
			require.ErrorContains(t, err, "webhook[1].rule[1].operations[0]")
			server.takeCalls()

			p.inputs["known-operation"] = "CREATE"
			exec, file, step := p.plan("partial", want)
			require.Equal(t, map[string][]string{"webhook": {"action.seed.value"}}, step.UnresolvedInputs)
			server.takeCalls()
			_, err = exec.ApplyPlan(t.Context(), file)
			if tc.operation == "UPDATE" {
				require.NoError(t, err)
				require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())
				require.Equal(t, []admissionv1.OperationType{admissionv1.Create, admissionv1.Update},
					server.snapshot().Webhooks[1].Rules[1].Operations)
				return
			}
			require.ErrorContains(t, err, "resource.example: schema: constraints")
			require.ErrorContains(t, err, "invalid rule operation")
			require.ErrorContains(t, err, "webhook[1].rule[1].operations[1]")
			require.Empty(t, server.takeCalls())
			if tc.replace {
				require.Equal(t, "uid-original", string(server.snapshot().UID))
				require.Equal(t, "example", server.snapshot().Name)
			}
		})
	}
}

func TestMutatingWebhookNativeOtherwiseNoOpSavedNoOpAndDestroy(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	lib := p.libs["admission"]
	native := lib.Constraints
	lib.Constraints = nil
	p.webhook()["reinvocation-policy"] = "historical"
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	exec, stale, _ := p.plan("settled", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	lib.Constraints = native

	_, err := p.executor("settled").Plan(t.Context())
	require.ErrorContains(t, err, "resource.example: schema: constraints")
	require.ErrorContains(t, err, "reinvocation-policy must be Never or IfNeeded")
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	_, err = exec.ApplyPlan(t.Context(), stale)
	require.ErrorContains(t, err, "resource.example: schema: constraints")
	require.ErrorContains(t, err, "reinvocation-policy must be Never or IfNeeded")
	require.Empty(t, server.takeCalls())

	exec = p.executor("settled")
	exec.Destroy = true
	destroy := saveWebhookPlan(t, exec)
	require.Equal(t, runtime.DecisionDestroy, destroy.Steps[0].Decision)
	server.takeCalls()
	_, err = exec.ApplyPlan(t.Context(), destroy)
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "GET", "DELETE"}, server.takeCalls())
	require.Nil(t, server.snapshot())
}

func TestMutatingWebhookStructuralEvaluatedInputs(t *testing.T) {
	for _, tc := range []struct {
		field string
		edit  func(*mutatingWebhookPlanner)
	}{
		{"metadata", func(p *mutatingWebhookPlanner) { p.inputs["metadata"] = nil }},
		{"webhook", func(p *mutatingWebhookPlanner) { p.inputs["webhook"] = nil }},
		{"webhook[0]", func(p *mutatingWebhookPlanner) { p.inputs["webhook"] = []any{nil} }},
		{"webhook[0].name", func(p *mutatingWebhookPlanner) { delete(p.webhook(), "name") }},
		{"webhook[0].name", func(p *mutatingWebhookPlanner) { p.webhook()["name"] = nil }},
		{"webhook[0].client-config", func(p *mutatingWebhookPlanner) {
			delete(p.webhook(), "client-config")
		}},
		{"webhook[0].client-config", func(p *mutatingWebhookPlanner) {
			p.webhook()["client-config"] = nil
		}},
		{"webhook[0].client-config.service.name", func(p *mutatingWebhookPlanner) {
			p.webhook()["client-config"] = map[string]any{"service": map[string]any{"namespace": "hook"}}
		}},
		{"webhook[0].client-config.service.namespace", func(p *mutatingWebhookPlanner) {
			p.webhook()["client-config"] = map[string]any{
				"service": map[string]any{"name": "hook", "namespace": nil},
			}
		}},
		{"webhook[0].rule[0].api-groups", func(p *mutatingWebhookPlanner) {
			p.webhook()["rule"] = []any{map[string]any{
				"api-versions": []any{"v1"}, "operations": []any{"CREATE"}, "resources": []any{"pods"},
			}}
		}},
		{"webhook[0].rule[0].operations", func(p *mutatingWebhookPlanner) {
			p.webhook()["rule"] = []any{map[string]any{
				"api-groups": []any{""}, "api-versions": []any{"v1"},
				"operations": nil, "resources": []any{"pods"},
			}}
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			server, cfg := newMutatingHTTP(t)
			p := newMutatingWebhookPlanner(t, cfg)
			tc.edit(p)
			_, err := p.executor("settled").Plan(t.Context())
			require.ErrorContains(t, err, "resource.example")
			require.ErrorContains(t, err, tc.field)
			require.Empty(t, server.takeCalls())
		})
	}
}

func TestMutatingWebhookReplacementPreflightsURL(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	p.metadata()["name"] = "new-name"
	p.webhook()["client-config"].(map[string]any)["url"] = "http://forbidden.example.test"
	exec, saved, _ := p.plan("settled", runtime.DecisionReplace)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	_, err := exec.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "webhook[0].client-config.url must use HTTPS")
	require.Empty(t, server.takeCalls())
	require.Equal(t, "example", server.snapshot().Name)
}

func TestMutatingWebhookApplyRejectsSameNameReplacement(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	p.webhook()["timeout-seconds"] = int64(30)
	exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
	server.takeCalls()
	server.change(func(object *admissionv1.MutatingWebhookConfiguration) {
		object.UID = "different"
	})
	_, err := exec.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "patch MutatingWebhookConfiguration")
	require.Equal(t, []string{"PATCH"}, server.takeCalls())
	require.Equal(t, "different", string(server.snapshot().UID))
	require.Equal(t, int32(10), *server.snapshot().Webhooks[0].TimeoutSeconds)
}

func TestMutatingWebhookNativeNameBoundaries(t *testing.T) {
	for _, tc := range []struct {
		field, value, failure string
	}{
		{"name", strings.Repeat("a", 253), ""},
		{"name", strings.Repeat("a", 254), "invalid metadata.name"},
		{"name", "a.b", ""},
		{"name", "a/b", "invalid metadata.name"},
		{"generate-name", "", "metadata.generate-name must not be empty"},
		{"generate-name", strings.Repeat("a", 62) + "A-", ""},
		{"generate-name", strings.Repeat("a", 63) + "A-", "invalid metadata.generate-name"},
		{"generate-name", "a.b-", "invalid metadata.generate-name"},
	} {
		t.Run(tc.field+"/"+tc.value, func(t *testing.T) {
			server, cfg := newMutatingHTTP(t)
			p := newMutatingWebhookPlanner(t, cfg)
			p.inputs["metadata"] = map[string]any{tc.field: tc.value}
			_, err := p.executor("settled").Plan(t.Context())
			if tc.failure == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "resource.example: schema: constraints")
				require.ErrorContains(t, err, tc.failure)
			}
			require.Empty(t, server.takeCalls())
		})
	}
}

func TestMutatingWebhookDestroyWithHistoricalMalformedIgnorePatterns(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	snapshot, err := p.store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.example")
	require.NotNil(t, entry)
	entry.Inputs["ignore-annotations"] = []any{"["}
	entry.Inputs["ignore-labels"] = []any{"["}
	rev, err := p.store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, p.store.SetCurrent(rev))

	exec := p.executor("settled")
	exec.Destroy = true
	saved := saveWebhookPlan(t, exec)
	server.takeCalls()
	_, err = exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "GET", "DELETE"}, server.takeCalls())
	require.Nil(t, server.snapshot())
}

func TestMutatingWebhookSavedFilterOwnership(t *testing.T) {
	for _, field := range []string{"annotations", "labels"} {
		t.Run(field, func(t *testing.T) {
			server, cfg := newMutatingHTTP(t)
			p := newMutatingWebhookPlanner(t, cfg)
			p.inputs["ignore-"+field] = []any{"^visible/"}
			p.metadata()["annotations"] = map[string]any{"stable": "annotation"}
			p.metadata()["labels"] = map[string]any{"stable": "label"}
			p.metadata()[field].(map[string]any)["hidden/remove"] = "managed"
			p.apply("settled", runtime.DecisionCreate)
			server.takeCalls()

			server.change(func(current *admissionv1.MutatingWebhookConfiguration) {
				maps.Copy(mutatingMetadata(current, field), map[string]string{
					"hidden/keep": "old-external", "hidden/adopt": "external",
					"visible/remove": "old-external",
				})
			})
			p.inputs["ignore-"+field] = []any{"^hidden/"}
			desired := p.metadata()[field].(map[string]any)
			delete(desired, "hidden/remove")
			desired["hidden/adopt"] = "managed"
			exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
			require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
			server.change(func(current *admissionv1.MutatingWebhookConfiguration) {
				for _, values := range []map[string]string{current.Annotations, current.Labels} {
					values["visible/late"] = "new-external"
					values["hidden/late"] = "new-external"
					values["unaffected/late"] = "new-external"
				}
			})
			_, err := exec.ApplyPlan(t.Context(), saved)
			require.NoError(t, err)
			require.Equal(t, []string{"GET", "PATCH", "GET", "GET"}, server.takeCalls())
			path := "/metadata/" + field
			requireMutatingPatch(t, server, []webhookPatchOperation{
				{Op: "replace", Path: path + "/hidden~1adopt", Value: "managed"},
				{Op: "remove", Path: path + "/hidden~1remove"},
				{Op: "remove", Path: path + "/visible~1late"},
				{Op: "remove", Path: path + "/visible~1remove"},
			})
			want := map[string]string{
				"stable": desired["stable"].(string), "hidden/adopt": "managed",
				"hidden/keep": "old-external", "hidden/late": "new-external",
				"unaffected/late": "new-external",
			}
			live := server.snapshot()
			require.Equal(t, "uid-original", string(live.UID))
			require.Equal(t, want, mutatingMetadata(live, field))
			other := oppositeMutatingMetadata(field)
			require.Equal(t, map[string]string{
				"stable":      p.metadata()[other].(map[string]any)["stable"].(string),
				"hidden/late": "new-external", "unaffected/late": "new-external",
				"visible/late": "new-external",
			}, mutatingMetadata(live, other))
			p.apply("settled", runtime.DecisionNoOp)
			require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
		})
	}
}

func TestMutatingWebhookSavedFilterPreservesUnchangedVisibility(t *testing.T) {
	for _, field := range []string{"annotations", "labels"} {
		for _, tc := range []struct {
			name, op string
			before   map[string]any
			after    map[string]any
		}{
			{"filter only", "", map[string]any{"stable": "kept"}, map[string]any{"stable": "kept"}},
			{"first managed", "add", nil, map[string]any{"stable": "kept"}},
			{"explicit clear", "remove", map[string]any{"stable": "kept"}, map[string]any{}},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				server, cfg := newMutatingHTTP(t)
				p := newMutatingWebhookPlanner(t, cfg)
				other := oppositeMutatingMetadata(field)
				if tc.before != nil {
					p.metadata()[field] = tc.before
				}
				p.metadata()[other] = map[string]any{"other": "kept"}
				p.apply("settled", runtime.DecisionCreate)
				server.takeCalls()

				p.inputs["ignore-"+field] = []any{"^hidden/"}
				p.metadata()[field] = tc.after
				exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
				require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
				server.change(func(current *admissionv1.MutatingWebhookConfiguration) {
					if current.Annotations == nil {
						current.Annotations = map[string]string{}
					}
					if current.Labels == nil {
						current.Labels = map[string]string{}
					}
					current.Annotations["external/late"] = "annotation"
					current.Labels["external/late"] = "label"
				})
				_, err := exec.ApplyPlan(t.Context(), saved)
				require.NoError(t, err)
				require.Equal(t, []string{"GET", "PATCH", "GET", "GET"}, server.takeCalls())
				patch := []webhookPatchOperation{}
				if tc.op != "" {
					patch = append(patch, webhookPatchOperation{
						Op: tc.op, Path: "/metadata/" + field + "/stable", Value: tc.after["stable"],
					})
				}
				requireMutatingPatch(t, server, patch)
				want := map[string]string{"external/late": map[string]string{
					"annotations": "annotation", "labels": "label",
				}[field]}
				for key, value := range tc.after {
					want[key] = value.(string)
				}
				live := server.snapshot()
				require.Equal(t, "uid-original", string(live.UID))
				require.Equal(t, want, mutatingMetadata(live, field))
				otherLate := map[string]string{"annotations": "annotation", "labels": "label"}[other]
				require.Equal(t, map[string]string{"other": "kept", "external/late": otherLate},
					mutatingMetadata(live, other))
			})
		}
	}
}

func mutatingMetadata(current *admissionv1.MutatingWebhookConfiguration,
	field string,
) map[string]string {
	if field == "annotations" {
		return current.Annotations
	}
	return current.Labels
}

func oppositeMutatingMetadata(field string) string {
	if field == "annotations" {
		return "labels"
	}
	return "annotations"
}

func TestMutatingWebhookNativePartialMap(t *testing.T) {
	for _, tc := range []struct {
		name          string
		comprehension bool
		key, value    string
		planError     string
		applyError    string
	}{
		{
			name: "invalid literal key", key: "Upper.example/label", value: "good",
			planError: "invalid metadata.labels key (metadata.labels['Upper.example/label'])",
		},
		{
			name: "invalid comprehension key", comprehension: true,
			key: "Upper.example/label", value: "good",
			planError: "invalid metadata.labels key (metadata.labels['Upper.example/label'])",
		},
		{
			name: "valid comprehension map", comprehension: true,
			key: "lower.example/label", value: "good",
		},
		{
			name: "invalid resolved value", comprehension: true,
			key: "lower.example/label", value: "bad value",
			applyError: "invalid metadata.labels value (metadata.labels['pending'])",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, cfg := newMutatingHTTP(t)
			p := newMutatingWebhookPlanner(t, cfg)
			p.inputs["comprehension"] = tc.comprehension
			p.inputs["label"] = tc.key
			p.inputs["label-value"] = tc.value
			if tc.planError != "" {
				_, err := p.executor("partial-labels").Plan(t.Context())
				require.ErrorContains(t, err, "resource.example: schema: constraints")
				require.ErrorContains(t, err, tc.planError)
				require.Empty(t, server.takeCalls())
				return
			}

			exec, saved, step := p.plan("partial-labels", runtime.DecisionCreate)
			require.Equal(t, map[string][]string{
				"metadata": {"action.seed.value"},
			}, step.UnresolvedInputs)
			require.Empty(t, server.takeCalls())
			_, err := exec.ApplyPlan(t.Context(), saved)
			if tc.applyError != "" {
				require.ErrorContains(t, err, "resource.example: schema: constraints")
				require.ErrorContains(t, err, tc.applyError)
				require.Empty(t, server.takeCalls())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())
			require.Equal(t, map[string]string{tc.key: "", "pending": "good"},
				server.snapshot().Labels)
		})
	}
}

func TestMutatingWebhookTargetConfigurationReplacement(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	p := newMutatingWebhookPlanner(t, cfg)
	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{"test": {Server: *cfg.Server}},
		Contexts: map[string]*clientcmdapi.Context{
			"first":  {Cluster: "test"},
			"second": {Cluster: "test"},
		},
		CurrentContext: "first",
	}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, clientcmd.WriteToFile(kubeconfig, path))
	p.inputs["kubeconfig"] = path
	p.apply("settled", runtime.DecisionCreate)
	require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())

	p.inputs["current-context"] = "second"
	_, _, step := p.plan("settled", runtime.DecisionReplace)
	require.Equal(t, []string{"current-context"}, step.ReplacementReasons)
	p.inputs["current-context"] = nil

	otherPath := filepath.Join(t.TempDir(), "config")
	require.NoError(t, clientcmd.WriteToFile(kubeconfig, otherPath))
	p.inputs["kubeconfig"] = otherPath
	_, _, step = p.plan("settled", runtime.DecisionReplace)
	require.Equal(t, []string{"kubeconfig"}, step.ReplacementReasons)
	require.Equal(t, "uid-original", string(server.snapshot().UID))
}
