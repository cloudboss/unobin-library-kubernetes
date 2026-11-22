package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestResourceQuotaStateBackedPlanApply(t *testing.T) {
	h := newResourceQuotaPlanTest(t)
	input := ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("initial")},
		Spec: &ResourceQuotaSpec{
			Hard:   &map[string]string{"pods": "1000m"},
			Scopes: &[]string{"NotTerminating", "BestEffort", "BestEffort"},
			ScopeSelector: &ResourceQuotaScopeSelector{MatchExpressions: &[]ResourceQuotaScopeExpression{
				{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"b", "a", "b"}},
			}},
		},
	}
	h.apply(t, input, runtime.DecisionCreate)
	require.Equal(t, []string{"POST"}, h.mutations)
	require.Equal(t, map[string]string{"pods": "1"}, pvcQuantityStrings(h.live.Spec.Hard))
	require.Equal(t, []corev1.ResourceQuotaScope{"BestEffort", "NotTerminating"}, h.live.Spec.Scopes)

	h.live.Status.Hard = corev1.ResourceList{"pods": resource.MustParse("99")}
	h.live.Status.Used = h.live.Status.Hard
	h.live.Generation, h.live.ResourceVersion = 12, "different"
	input.Metadata.Namespace = new("default")
	input.Spec = &ResourceQuotaSpec{
		Hard:   &map[string]string{"pods": "1"},
		Scopes: &[]string{"BestEffort", "NotTerminating"},
		ScopeSelector: &ResourceQuotaScopeSelector{MatchExpressions: &[]ResourceQuotaScopeExpression{
			{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"a", "b"}},
		}},
	}
	input.CreateTimeout, input.UpdateTimeout = new("60s"), new("1m")
	h.apply(t, input, runtime.DecisionNoOp)
	require.Equal(t, []string{"POST"}, h.mutations)

	uid := h.live.UID
	input.Spec.Hard = &map[string]string{"pods": "2"}
	h.apply(t, input, runtime.DecisionUpdate)
	require.Equal(t, uid, h.live.UID)
	input.Spec.Hard = &map[string]string{"services": "2"}
	h.apply(t, input, runtime.DecisionUpdate)
	require.Equal(t, uid, h.live.UID)
	require.Equal(t, map[string]string{"services": "2"}, pvcQuantityStrings(h.live.Spec.Hard))
	input.Spec.ScopeSelector = &ResourceQuotaScopeSelector{}
	h.apply(t, input, runtime.DecisionUpdate)
	require.Equal(t, uid, h.live.UID)
	require.Equal(t, &corev1.ScopeSelector{}, h.live.Spec.ScopeSelector)
	require.Equal(t, []string{"POST", "PATCH", "PATCH", "PATCH"}, h.mutations)

	for _, change := range []func(*ResourceQuotaResource){
		func(r *ResourceQuotaResource) { r.Metadata.Name = new("renamed") },
		func(r *ResourceQuotaResource) { r.Metadata.Namespace = new("other") },
		func(r *ResourceQuotaResource) { r.Spec.Scopes = &[]string{"NotBestEffort"} },
		func(r *ResourceQuotaResource) {
			r.Metadata.Name, r.Metadata.GenerateName = nil, new("first-")
		},
		func(r *ResourceQuotaResource) { r.Metadata.GenerateName = new("second-") },
	} {
		change(&input)
		prior := h.live.UID
		before := len(h.mutations)
		h.apply(t, input, runtime.DecisionReplace)
		require.Equal(t, []string{"DELETE", "POST"}, h.mutations[before:])
		require.NotEqual(t, prior, h.live.UID)
	}
	require.Equal(t, "second-actual", h.live.Name)
	require.Equal(t, "other", h.live.Namespace)
	require.Equal(t, []corev1.ResourceQuotaScope{"NotBestEffort"}, h.live.Spec.Scopes)

	exec := h.executor(t, input, "factory.ub")
	exec.Inputs["server"] = "https://127.0.0.1:1"
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	step := quotaSubjectStep(t, plan)
	require.Equal(t, runtime.DecisionReplace, step.Decision)
	require.Equal(t, []string{"server"}, step.ReplacementReasons)
}

func TestResourceQuotaSavedSelectorPresence(t *testing.T) {
	h := newResourceQuotaPlanTest(t)
	input := ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("quota")}}
	checkSavedSelector := func(t *testing.T, want any) {
		t.Helper()
		snapshot, err := h.store.Current()
		require.NoError(t, err)
		entry := snapshot.Find("resource.subject")
		require.NotNil(t, entry)
		require.Equal(t, map[string]any{
			"hard": map[string]any{}, "scopes": []any{}, "scope-selector": want,
		}, entry.Outputs["spec"])
	}

	h.apply(t, input, runtime.DecisionCreate)
	require.Nil(t, h.live.Spec.ScopeSelector)
	checkSavedSelector(t, nil)
	uid := h.live.UID

	input.Spec = &ResourceQuotaSpec{ScopeSelector: &ResourceQuotaScopeSelector{}}
	h.apply(t, input, runtime.DecisionUpdate)
	require.Equal(t, uid, h.live.UID)
	require.Equal(t, &corev1.ScopeSelector{}, h.live.Spec.ScopeSelector)
	checkSavedSelector(t, map[string]any{"match-expressions": []any{}})

	input.Spec.ScopeSelector = nil
	h.apply(t, input, runtime.DecisionUpdate)
	require.Equal(t, uid, h.live.UID)
	require.Nil(t, h.live.Spec.ScopeSelector)
	checkSavedSelector(t, nil)
	require.Equal(t, []string{"POST", "PATCH", "PATCH"}, h.mutations)
}

func TestResourceQuotaChangedIgnorePatterns(t *testing.T) {
	h := newResourceQuotaPlanTest(t)
	const managed = "example.com/managed"
	const newlyIgnored = "example.com/newly-ignored"
	const formerlyIgnored = "example.com/formerly-ignored"
	input := ResourceQuotaResource{
		Metadata: ServiceMetadata{
			Name: new("quota"), Annotations: &map[string]string{managed: "annotation"},
			Labels: &map[string]string{managed: "label"},
		},
		IgnoreAnnotations: &[]string{"formerly-ignored"},
		IgnoreLabels:      &[]string{"formerly-ignored"},
	}
	h.apply(t, input, runtime.DecisionCreate)
	h.live.Annotations[newlyIgnored], h.live.Annotations[formerlyIgnored] = "preserve", "remove"
	h.live.Labels[newlyIgnored], h.live.Labels[formerlyIgnored] = "preserve", "remove"

	input.IgnoreAnnotations = &[]string{"newly-ignored"}
	input.IgnoreLabels = &[]string{"newly-ignored"}
	h.apply(t, input, runtime.DecisionUpdate)
	require.Equal(t, map[string]string{managed: "annotation", newlyIgnored: "preserve"},
		h.live.Annotations)
	require.Equal(t, map[string]string{managed: "label", newlyIgnored: "preserve"}, h.live.Labels)
	require.Equal(t, []string{"POST", "PATCH"}, h.mutations)
	snapshot, err := h.store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.subject")
	require.NotNil(t, entry)
	metadata := entry.Outputs["metadata"].(map[string]any)
	require.Equal(t, map[string]any{managed: "annotation"}, metadata["annotations"])
	require.Equal(t, map[string]any{managed: "label"}, metadata["labels"])
}

func TestResourceQuotaNoOpConstraintsAndHistoricalDeletion(t *testing.T) {
	h := newResourceQuotaPlanTest(t)
	input := ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("Historical")},
		Spec: &ResourceQuotaSpec{ScopeSelector: &ResourceQuotaScopeSelector{
			MatchExpressions: &[]ResourceQuotaScopeExpression{
				{ScopeName: "PriorityClass", Operator: "former-value"},
			},
		}}}
	lib := h.libraries["core"]
	rules := lib.Constraints["resource.resource-quota"]
	delete(lib.Constraints, "resource.resource-quota")
	h.apply(t, input, runtime.DecisionCreate)
	exec := h.executor(t, input, "factory.ub")
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, quotaSubjectStep(t, plan).Decision)
	saved := saveResourceQuotaPlan(t, plan)
	prior, err := h.store.Current()
	require.NoError(t, err)
	lib.Constraints["resource.resource-quota"] = rules

	_, err = exec.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject: schema: constraints[1] (predicate)")
	_, err = exec.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "resource.subject: schema: constraints[1] (predicate)")
	require.Equal(t, []string{"POST"}, h.mutations)
	after, err := h.store.Current()
	require.NoError(t, err)
	require.Equal(t, prior.Find("resource.subject"), after.Find("resource.subject"))
	exec.Destroy = true
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionDestroy, quotaSubjectStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), saveResourceQuotaPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "DELETE"}, h.mutations)
	require.Nil(t, h.live)
}

func TestResourceQuotaDestroyWithHistoricalInvalidIgnorePatterns(t *testing.T) {
	h := newResourceQuotaPlanTest(t)
	input := ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("quota")}}
	h.apply(t, input, runtime.DecisionCreate)
	h.live.Annotations = map[string]string{"example.com/external": "hidden"}
	h.live.Labels = map[string]string{"example.com/external": "hidden"}
	snapshot, err := h.store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.subject")
	require.NotNil(t, entry)
	entry.Inputs["ignore-annotations"] = []any{"[", "external"}
	entry.Inputs["ignore-labels"] = []any{"[", "external"}
	rev, err := h.store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, h.store.SetCurrent(rev))

	exec := h.executor(t, input, "factory.ub")
	exec.Destroy = true
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	step := quotaSubjectStep(t, plan)
	require.Equal(t, runtime.DecisionDestroy, step.Decision)
	require.Equal(t, new(h.server+"/"+string(h.live.UID)), step.ExpectedStableID)
	_, err = exec.ApplyPlan(t.Context(), saveResourceQuotaPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "DELETE"}, h.mutations)
	require.Nil(t, h.live)
}

func TestResourceQuotaMissingEvaluatedMetadata(t *testing.T) {
	h := newResourceQuotaPlanTest(t)
	exec := h.executor(t, ResourceQuotaResource{}, "missing-metadata/factory.ub")
	_, err := exec.Plan(t.Context())
	require.ErrorContains(t, err, `field "metadata": required but not provided`)
	require.Empty(t, h.mutations)
}

func TestResourceQuotaConversionBeforeMutation(t *testing.T) {
	h := newResourceQuotaPlanTest(t)
	input := ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("original")},
		Spec: &ResourceQuotaSpec{Hard: &map[string]string{"pods": "1"}},
	}
	h.apply(t, input, runtime.DecisionCreate)
	prior, err := h.store.Current()
	require.NoError(t, err)
	input.Metadata.Name = new("replacement")
	for _, test := range []struct {
		change     func(*ResourceQuotaResource)
		diagnostic string
	}{
		{func(r *ResourceQuotaResource) {
			r.Spec = &ResourceQuotaSpec{Hard: &map[string]string{"pods": "bogus"}}
		}, "ResourceQuota hard: parse quantity"},
		{func(r *ResourceQuotaResource) { r.CreateTimeout = new("bogus") }, "parse create-timeout"},
		{func(r *ResourceQuotaResource) { r.UpdateTimeout = new("bogus") }, "parse update-timeout"},
	} {
		changed := input
		test.change(&changed)
		exec := h.executor(t, changed, "factory.ub")
		plan, err := exec.Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, runtime.DecisionReplace, quotaSubjectStep(t, plan).Decision)
		_, err = exec.ApplyPlan(t.Context(), saveResourceQuotaPlan(t, plan))
		require.ErrorContains(t, err, test.diagnostic)
		require.Equal(t, "original", h.live.Name)
		require.Equal(t, []string{"POST"}, h.mutations)
		after, err := h.store.Current()
		require.NoError(t, err)
		require.Equal(t, prior.Find("resource.subject"), after.Find("resource.subject"))
	}
}

type resourceQuotaPlanTest struct {
	libraries map[string]*runtime.Library
	store     *local.Store
	server    string
	live      *corev1.ResourceQuota
	mutations []string
}

func newResourceQuotaPlanTest(t *testing.T) *resourceQuotaPlanTest {
	t.Helper()
	h := &resourceQuotaPlanTest{}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			h.mutations = append(h.mutations, req.Method)
		}
		switch req.Method {
		case http.MethodPost:
			require.Nil(t, h.live)
			h.live = &corev1.ResourceQuota{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(h.live))
			if h.live.Name == "" {
				h.live.Name = h.live.GenerateName + "actual"
			}
			h.live.UID = types.UID(fmt.Sprintf("quota-%d", len(h.mutations)))
			h.live.Status.Hard = h.live.Spec.Hard
		case http.MethodPatch:
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			old, err := json.Marshal(h.live)
			require.NoError(t, err)
			next, err := patch.Apply(old)
			require.NoError(t, err)
			h.live = &corev1.ResourceQuota{}
			require.NoError(t, json.Unmarshal(next, h.live))
			h.live.Status.Hard = h.live.Spec.Hard
		case http.MethodDelete:
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, &metav1.Preconditions{UID: &h.live.UID}, options.Preconditions)
			h.live = nil
			respond(t, w, &metav1.Status{Status: "Success"})
			return
		case http.MethodGet:
			require.NotNil(t, h.live)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		respond(t, w, h.live)
	})
	h.server = *c.Server
	var err error
	h.store, err = local.NewStore(t.TempDir(), "quota", "test", encrypters.Noop{})
	require.NoError(t, err)
	lib := &runtime.Library{
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"resource-quota": runtime.MakeResource[
				ResourceQuotaResource, *ResourceQuotaResourceOutput, *config.Configuration,
			]((&ResourceQuotaResource{}).ResourceDefinition()),
		},
	}
	addNativeCoreConstraints(t, lib)
	extracted, err := nativeCoreSchema()
	require.NoError(t, err)
	lib.Schema = &runtime.LibrarySchema{Resources: map[string]*runtime.TypeSchema{
		"resource-quota": {EvaluatedInputs: extracted.Resources["resource-quota"].Inputs},
	}}
	h.libraries = map[string]*runtime.Library{"core": lib}
	return h
}

func (h *resourceQuotaPlanTest) executor(
	t *testing.T, input ResourceQuotaResource, fixture string,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/resource-quota-state/" + fixture)
	require.NoError(t, err)
	source, err := syntax.ParseSource(fixture, body)
	require.NoError(t, err)
	encoded, err := ub.Marshal(input)
	require.NoError(t, err)
	var inputs map[string]any
	require.NoError(t, ub.Unmarshal(encoded, &inputs))
	inputs["server"] = h.server
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(source.Factory.Body, h.libraries),
		SyntaxSource: &source.Factory.Body, Libraries: h.libraries, Inputs: inputs, Store: h.store,
		Factory: state.FactoryInfo{Name: "quota", Version: "v0", ContentRevision: "c0"},
	}
}

func (h *resourceQuotaPlanTest) apply(
	t *testing.T, input ResourceQuotaResource, want runtime.Decision,
) {
	t.Helper()
	exec := h.executor(t, input, "factory.ub")
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, want, quotaSubjectStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), saveResourceQuotaPlan(t, plan))
	require.NoError(t, err)
}

func quotaSubjectStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			return step
		}
	}
	require.FailNow(t, "plan has no ResourceQuota subject")
	return nil
}

func saveResourceQuotaPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	data, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(data)
	require.NoError(t, err)
	return file
}
