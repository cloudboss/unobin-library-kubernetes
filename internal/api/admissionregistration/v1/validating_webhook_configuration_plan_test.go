package v1

import (
	"context"
	"fmt"
	"maps"
	"os"
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

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const webhookExport = "validating-webhook-configuration"
const webhookConstraintKey = "resource." + webhookExport

var readWebhookSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/admissionregistration/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("admissionregistration schema extraction: %v", warnings)
	}
	return schema, nil
})

type webhookSeed struct{ Echo string }

func (s *webhookSeed) Run(context.Context, *config.Configuration) (any, error) {
	return map[string]any{"value": s.Echo}, nil
}

type webhookPlanner struct {
	t      *testing.T
	libs   map[string]*runtime.Library
	store  *local.Store
	inputs map[string]any
}

func newWebhookPlanner(t *testing.T, cfg *config.Configuration) *webhookPlanner {
	t.Helper()
	schema, err := readWebhookSchema()
	require.NoError(t, err)
	model := *schema.Resources[webhookExport]
	model.EvaluatedInputs, model.Inputs = model.Inputs, nil
	lib := &runtime.Library{
		Name:          "kubernetes-admissionregistration-v1",
		Configuration: config.LibraryConfiguration(),
		Schema: &runtime.LibrarySchema{
			Resources: map[string]*runtime.TypeSchema{webhookExport: &model},
		},
		Constraints: map[string][]lang.ConstraintSpec{webhookConstraintKey: model.Constraints},
		Resources: map[string]runtime.ResourceRegistration{
			webhookExport: runtime.MakeResource[
				ValidatingWebhookConfigurationResource, *ValidatingWebhookConfigurationResourceOutput,
				*config.Configuration,
			]((&ValidatingWebhookConfigurationResource{}).ResourceDefinition()),
		},
		Actions: map[string]runtime.ActionRegistration{
			"seed": runtime.MakeAction[webhookSeed, any, *config.Configuration](),
		},
	}
	store, err := local.NewStore(t.TempDir(), "webhook", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &webhookPlanner{
		t: t, libs: map[string]*runtime.Library{"admission": lib}, store: store,
		inputs: map[string]any{
			"server":   *cfg.Server,
			"metadata": map[string]any{"name": "example"},
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
			"timeout":            int64(30),
			"prefix":             "webhook-",
		},
	}
}

func (p *webhookPlanner) executor(fixture string) *runtime.Executor {
	p.t.Helper()
	body, err := os.ReadFile("testdata/ub/" + fixture + "/factory.ub")
	require.NoError(p.t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(p.t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, p.libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: p.libs, Inputs: p.inputs, Store: p.store,
		Factory: state.FactoryInfo{Name: "webhook", Version: "v0", ContentRevision: "c0"},
	}
}

func (p *webhookPlanner) plan(fixture string, want runtime.Decision) (
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

func saveWebhookPlan(t *testing.T, exec *runtime.Executor) *runtime.PlanFile {
	t.Helper()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	encoded, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	return file
}

func (p *webhookPlanner) apply(fixture string, want runtime.Decision) {
	p.t.Helper()
	executor, file, _ := p.plan(fixture, want)
	_, err := executor.ApplyPlan(p.t.Context(), file)
	require.NoError(p.t, err)
}

func (p *webhookPlanner) metadata() map[string]any {
	p.t.Helper()
	return p.inputs["metadata"].(map[string]any)
}

func (p *webhookPlanner) webhook() map[string]any {
	p.t.Helper()
	return p.inputs["webhook"].([]any)[0].(map[string]any)
}

func TestWebhookPlannedDefaultsSelectorsAndDrift(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
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

	server.change(func(object *admissionv1.ValidatingWebhookConfiguration) {
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
	p.webhook()["timeout-seconds"] = int64(10)
	p.apply("settled", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())

	server.change(func(object *admissionv1.ValidatingWebhookConfiguration) {
		object.Webhooks[0].TimeoutSeconds = new(int32(20))
	})
	p.apply("settled", runtime.DecisionUpdate)
	require.Equal(t, []string{"GET", "GET", "PATCH", "GET", "GET"}, server.takeCalls())
	require.Equal(t, int32(10), *server.snapshot().Webhooks[0].TimeoutSeconds)
	require.Nil(t, server.snapshot().Webhooks[0].NamespaceSelector)
	require.Nil(t, server.snapshot().Webhooks[0].MatchConditions)
	p.apply("settled", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())

	delete(p.webhook(), "object-selector")
	p.apply("settled", runtime.DecisionUpdate)
	require.Nil(t, server.snapshot().Webhooks[0].ObjectSelector)
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

func TestWebhookPendingIdentity(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
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

func TestWebhookSavedPlanMetadataFilterChanges(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
	p.inputs["ignore-annotations"] = []any{"^visible/"}
	p.inputs["ignore-labels"] = p.inputs["ignore-annotations"]
	for _, field := range []string{"annotations", "labels"} {
		p.metadata()[field] = map[string]any{"hidden/remove": "old", "stable": "kept"}
	}
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	server.change(func(obj *admissionv1.ValidatingWebhookConfiguration) {
		for _, values := range []map[string]string{obj.Annotations, obj.Labels} {
			maps.Copy(values, map[string]string{
				"hidden/keep": "external", "hidden/adopt": "external", "visible/remove": "external",
			})
		}
	})
	p.inputs["ignore-annotations"] = []any{"^hidden/"}
	p.inputs["ignore-labels"] = p.inputs["ignore-annotations"]
	for _, field := range []string{"annotations", "labels"} {
		p.metadata()[field] = map[string]any{"hidden/adopt": "managed", "stable": "kept"}
	}
	exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	server.change(func(obj *admissionv1.ValidatingWebhookConfiguration) {
		obj.Annotations["visible/after-plan"] = "external"
		obj.Labels["visible/after-plan"] = "external"
	})
	_, err := exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-original"},
		{"op":"replace","path":"/metadata/annotations/hidden~1adopt","value":"managed"},
		{"op":"remove","path":"/metadata/annotations/hidden~1remove"},
		{"op":"remove","path":"/metadata/annotations/visible~1after-plan"},
		{"op":"remove","path":"/metadata/annotations/visible~1remove"},
		{"op":"replace","path":"/metadata/labels/hidden~1adopt","value":"managed"},
		{"op":"remove","path":"/metadata/labels/hidden~1remove"},
		{"op":"remove","path":"/metadata/labels/visible~1after-plan"},
		{"op":"remove","path":"/metadata/labels/visible~1remove"}
	]`, server.patchBody())
	require.Equal(t, []string{"GET", "PATCH", "GET", "GET"}, server.takeCalls())
	want := map[string]string{
		"hidden/keep": "external", "hidden/adopt": "managed", "stable": "kept",
	}
	live := server.snapshot()
	require.Equal(t, want, live.Annotations)
	require.Equal(t, want, live.Labels)
	require.Equal(t, "uid-original", string(live.UID))
	p.apply("settled", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
}

func TestWebhookSavedPlanMetadataFilterPreservesUnrelatedChanges(t *testing.T) {
	for _, tc := range []struct {
		name           string
		prior, desired map[string]any
		external, want map[string]string
		patch          string
	}{
		{
			name:     "filter only",
			prior:    map[string]any{"stable": "kept"},
			desired:  map[string]any{"stable": "kept"},
			external: map[string]string{"external/added": "external"},
			want:     map[string]string{"stable": "kept", "external/added": "external"},
			patch: `[
				{"op":"test","path":"/metadata/uid","value":"uid-original"}
			]`,
		},
		{
			name:     "first managed annotation",
			prior:    map[string]any{},
			desired:  map[string]any{"stable": "kept"},
			external: map[string]string{"external/added": "external"},
			want:     map[string]string{"stable": "kept", "external/added": "external"},
			patch: `[
				{"op":"test","path":"/metadata/uid","value":"uid-original"},
				{"op":"add","path":"/metadata/annotations/stable","value":"kept"}
			]`,
		},
		{
			name:    "clear annotations",
			prior:   map[string]any{"stable": "kept"},
			desired: map[string]any{},
			external: map[string]string{
				"external/added": "external",
				"visible/added":  "external",
			},
			want: map[string]string{"external/added": "external"},
			patch: `[
				{"op":"test","path":"/metadata/uid","value":"uid-original"},
				{"op":"remove","path":"/metadata/annotations/stable"},
				{"op":"remove","path":"/metadata/annotations/visible~1added"}
			]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, cfg := newWebhookHTTP(t)
			p := newWebhookPlanner(t, cfg)
			p.inputs["ignore-annotations"] = []any{"^visible/"}
			p.metadata()["annotations"] = tc.prior
			p.metadata()["labels"] = map[string]any{"stable": "kept"}
			p.apply("settled", runtime.DecisionCreate)
			server.takeCalls()

			p.inputs["ignore-annotations"] = []any{"^hidden/"}
			p.metadata()["annotations"] = tc.desired
			exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
			require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
			server.change(func(obj *admissionv1.ValidatingWebhookConfiguration) {
				annotations := maps.Clone(tc.external)
				maps.Copy(annotations, obj.Annotations)
				obj.Annotations = annotations
				obj.Labels["external/added"] = "external"
			})

			_, err := exec.ApplyPlan(t.Context(), saved)
			require.NoError(t, err)
			require.JSONEq(t, tc.patch, server.patchBody())
			require.Equal(t, []string{"GET", "PATCH", "GET", "GET"}, server.takeCalls())
			live := server.snapshot()
			require.Equal(t, "uid-original", string(live.UID))
			require.Equal(t, tc.want, live.Annotations)
			require.Equal(t, map[string]string{
				"stable": "kept", "external/added": "external",
			}, live.Labels)
		})
	}
}

func TestWebhookSavedPlanMetadataFilterEmptyProjection(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	server.change(func(obj *admissionv1.ValidatingWebhookConfiguration) {
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
}

func TestWebhookSavedPlanMetadataFilterRejectsNewUID(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	p.inputs["ignore-annotations"] = []any{"^hidden/"}
	exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
	server.takeCalls()
	server.change(func(obj *admissionv1.ValidatingWebhookConfiguration) {
		obj.UID = "different"
	})
	_, err := exec.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "object example UID changed from uid-original to different")
	require.Equal(t, []string{"GET"}, server.takeCalls())
	require.Equal(t, "different", string(server.snapshot().UID))
}

func TestWebhookNativePartialAndSavedDeferred(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		replace         bool
	}{
		{"valid create", "UPDATE", false},
		{"invalid create", "update", false},
		{"invalid replacement", "update", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, cfg := newWebhookHTTP(t)
			p := newWebhookPlanner(t, cfg)
			want := runtime.DecisionCreate
			if tc.replace {
				p.apply("settled", runtime.DecisionCreate)
				p.metadata()["name"] = "changed"
				server.takeCalls()
				want = runtime.DecisionReplace
			}
			p.inputs["operation"] = tc.operation
			p.inputs["timeout"] = int64(0)
			_, err := p.executor("partial").Plan(t.Context())
			require.ErrorContains(t, err, "resource.example: schema: constraints")
			require.ErrorContains(t, err, "timeout-seconds must be between 1 and 30")
			server.takeCalls()

			p.inputs["timeout"] = int64(30)
			exec, file, step := p.plan("partial", want)
			require.Equal(t, map[string][]string{"webhook": {"action.seed.value"}}, step.UnresolvedInputs)
			server.takeCalls()
			_, err = exec.ApplyPlan(t.Context(), file)
			if tc.operation == "UPDATE" {
				require.NoError(t, err)
				require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())
				require.Equal(t, []admissionv1.OperationType{admissionv1.Create, admissionv1.Update},
					server.snapshot().Webhooks[0].Rules[0].Operations)
				return
			}
			require.ErrorContains(t, err, "resource.example: schema: constraints")
			require.ErrorContains(t, err, "invalid rule operation")
			require.Empty(t, server.takeCalls())
			if tc.replace {
				require.Equal(t, "uid-original", string(server.snapshot().UID))
				require.Equal(t, "example", server.snapshot().Name)
			}
		})
	}
}

func TestWebhookNativeOtherwiseNoOpSavedNoOpAndDestroy(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
	lib := p.libs["admission"]
	native := lib.Constraints
	lib.Constraints = nil
	p.webhook()["failure-policy"] = "historical"
	p.apply("settled", runtime.DecisionCreate)
	server.takeCalls()
	exec, stale, _ := p.plan("settled", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	lib.Constraints = native

	_, err := p.executor("settled").Plan(t.Context())
	require.ErrorContains(t, err, "resource.example: schema: constraints")
	require.ErrorContains(t, err, "failure-policy must be Fail or Ignore")
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	_, err = exec.ApplyPlan(t.Context(), stale)
	require.ErrorContains(t, err, "resource.example: schema: constraints")
	require.ErrorContains(t, err, "failure-policy must be Fail or Ignore")
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

func TestWebhookStructuralEvaluatedInputs(t *testing.T) {
	for _, tc := range []struct {
		field string
		edit  func(*webhookPlanner)
	}{
		{"metadata", func(p *webhookPlanner) { p.inputs["metadata"] = nil }},
		{"webhook", func(p *webhookPlanner) { p.inputs["webhook"] = nil }},
		{"webhook[0]", func(p *webhookPlanner) { p.inputs["webhook"] = []any{nil} }},
		{"webhook[0].client-config", func(p *webhookPlanner) { delete(p.webhook(), "client-config") }},
		{"webhook[0].client-config", func(p *webhookPlanner) { p.webhook()["client-config"] = nil }},
		{"webhook[0].client-config.service.name", func(p *webhookPlanner) {
			p.webhook()["client-config"] = map[string]any{"service": map[string]any{"namespace": "hook"}}
		}},
		{"webhook[0].client-config.service.namespace", func(p *webhookPlanner) {
			p.webhook()["client-config"] = map[string]any{
				"service": map[string]any{"name": "hook", "namespace": nil},
			}
		}},
		{"webhook[0].rule[0].api-groups", func(p *webhookPlanner) {
			p.webhook()["rule"] = []any{map[string]any{
				"api-versions": []any{"v1"}, "operations": []any{"CREATE"}, "resources": []any{"pods"},
			}}
		}},
		{"webhook[0].rule[0].operations", func(p *webhookPlanner) {
			p.webhook()["rule"] = []any{map[string]any{
				"api-groups": []any{""}, "api-versions": []any{"v1"},
				"operations": nil, "resources": []any{"pods"},
			}}
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			server, cfg := newWebhookHTTP(t)
			p := newWebhookPlanner(t, cfg)
			tc.edit(p)
			_, err := p.executor("settled").Plan(t.Context())
			require.ErrorContains(t, err, "resource.example")
			require.ErrorContains(t, err, tc.field)
			require.Empty(t, server.takeCalls())
		})
	}
}

func TestWebhookReplacementPreflightsURL(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
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

func TestWebhookApplyRejectsSameNameReplacement(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	p.webhook()["timeout-seconds"] = int64(30)
	exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
	server.takeCalls()
	server.change(func(object *admissionv1.ValidatingWebhookConfiguration) {
		object.UID = "different"
	})
	_, err := exec.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "patch ValidatingWebhookConfiguration")
	require.Equal(t, []string{"PATCH"}, server.takeCalls())
	require.Equal(t, "different", string(server.snapshot().UID))
	require.Equal(t, int32(10), *server.snapshot().Webhooks[0].TimeoutSeconds)
}

func TestWebhookNativeNameBoundaries(t *testing.T) {
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
			server, cfg := newWebhookHTTP(t)
			p := newWebhookPlanner(t, cfg)
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

func TestWebhookDestroyWithHistoricalMalformedIgnorePatterns(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	p := newWebhookPlanner(t, cfg)
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
