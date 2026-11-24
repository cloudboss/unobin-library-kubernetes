package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const hpaConstraintKey = "resource.horizontal-pod-autoscaler"

type hpaPlanServer struct {
	mu          sync.Mutex
	server      string
	live        map[string]*autoscalingv1.HorizontalPodAutoscaler
	mutations   []string
	patches     []string
	credentials []string
	nextUID     int
}

func newHPAPlanServer(t *testing.T) *hpaPlanServer {
	t.Helper()
	s := &hpaPlanServer{live: map[string]*autoscalingv1.HorizontalPodAutoscaler{}}
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		name := filepath.Base(req.URL.Path)
		live := s.live[name]
		s.credentials = append(s.credentials, req.Header.Get("Authorization"))
		switch req.Method {
		case "POST":
			live = &autoscalingv1.HorizontalPodAutoscaler{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(live))
			if live.Name == "" {
				live.Name = live.GenerateName + "actual"
			}
			s.nextUID++
			live.UID = types.UID(fmt.Sprintf("%s-%d", live.Name, s.nextUID))
			if live.Name == "seed" {
				live.UID = types.UID(live.Spec.ScaleTargetRef.Name)
			}
			if live.Spec.MinReplicas == nil {
				live.Spec.MinReplicas = new(int32(1))
			}
			if live.Spec.TargetCPUUtilizationPercentage == nil {
				live.Spec.TargetCPUUtilizationPercentage = new(int32(82))
			}
			s.live[live.Name] = live
			s.mutations = append(s.mutations, "POST "+live.Name)
		case "GET":
			if live == nil {
				hpaFailure(t, w, 404)
				return
			}
		case "PATCH":
			require.NotNil(t, live)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			applyHPAPatch(t, body, live)
			s.patches = append(s.patches, string(body))
			s.mutations = append(s.mutations, "PATCH "+name)
		case "DELETE":
			require.NotNil(t, live)
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, &live.UID, options.Preconditions.UID)
			delete(s.live, name)
			s.mutations = append(s.mutations, "DELETE "+name)
			hpaRespond(t, w, metav1.Status{Status: "Success"})
			return
		default:
			t.Errorf("unexpected %s %s", req.Method, req.URL.Path)
		}
		hpaRespond(t, w, live)
	})
	s.server = *c.Server
	return s
}

func (s *hpaPlanServer) edit(name string, change func(*autoscalingv1.HorizontalPodAutoscaler)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s.live[name])
}

func (s *hpaPlanServer) snapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.mutations), slices.Clone(s.patches)
}

func hpaNativeSchema(t *testing.T) *runtime.LibrarySchema {
	t.Helper()
	sdk, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/autoscaling/v1"),
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root},
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(sdk))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	resource := schema.Resources["horizontal-pod-autoscaler"]
	resource.EvaluatedInputs = resource.Inputs
	return schema
}

func hpaPlanLibrary(schema *runtime.LibrarySchema) *runtime.Library {
	base := runtime.MakeResource[
		HorizontalPodAutoscalerResource, *HorizontalPodAutoscalerResourceOutput, *config.Configuration,
	]((&HorizontalPodAutoscalerResource{}).ResourceDefinition())
	return &runtime.Library{
		Configuration: config.LibraryConfiguration(),
		Schema:        schema,
		Resources: map[string]runtime.ResourceRegistration{
			"horizontal-pod-autoscaler": HorizontalPodAutoscalerRegistration[
				HorizontalPodAutoscalerResource, *HorizontalPodAutoscalerResourceOutput,
			](base, base.Read, base.KnownReplacementReasons),
		},
		Constraints: map[string][]lang.ConstraintSpec{
			hpaConstraintKey: schema.Resources["horizontal-pod-autoscaler"].Constraints,
		},
	}
}

func hpaExecutor(t *testing.T, file string, library *runtime.Library, store *local.Store,
	inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub", file, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	for k, value := range map[string]any{
		"name": nil, "prefix": nil, "namespace": nil, "labels": nil, "annotations": nil,
		"ignore-labels": nil, "ignore-annotations": nil, "token": "test", "max": 4,
		"min": nil, "cpu": nil,
	} {
		if _, supplied := inputs[k]; !supplied {
			inputs[k] = value
		}
	}
	libs := map[string]*runtime.Library{"hpa": library}
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "hpa", Version: "v0", ContentRevision: "test"},
	}
}

func hpaStateStore(t *testing.T) *local.Store {
	t.Helper()
	store, err := local.NewStore(t.TempDir(), "hpa", "test", encrypters.Noop{})
	require.NoError(t, err)
	return store
}

func hpaPlan(t *testing.T, e *runtime.Executor, decision runtime.Decision) *runtime.Plan {
	t.Helper()
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, decision, hpaStep(t, plan).Decision)
	return plan
}

func hpaStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.hpa" {
			return step
		}
	}
	t.Fatal("missing resource.hpa plan step")
	return nil
}

func hpaApply(t *testing.T, e *runtime.Executor, plan *runtime.Plan) error {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), saved)
	return err
}

func hpaStoredObject(t *testing.T, store *local.Store) map[string]any {
	t.Helper()
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.hpa")
	require.NotNil(t, entry)
	return entry.Outputs["object"].(map[string]any)
}

func TestHPAV1SavedPlanCoreLifecycle(t *testing.T) {
	s := newHPAPlanServer(t)
	library := hpaPlanLibrary(hpaNativeSchema(t))
	store := hpaStateStore(t)
	inputs := map[string]any{"server": s.server, "prefix": "hpa-generated-", "cpu": 70}
	e := hpaExecutor(t, "state", library, store, inputs)
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionCreate)))
	name := "hpa-generated-actual"
	metadata := hpaStoredObject(t, store)["metadata"].(map[string]any)
	uid := metadata["uid"]
	s.edit(name, func(live *autoscalingv1.HorizontalPodAutoscaler) {
		live.ResourceVersion, live.Generation, live.Status.CurrentReplicas = "updated", 3, 10
	})
	inputs["namespace"], inputs["min"], inputs["token"] = "default", 1, "new-auth"
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionNoOp)))
	s.mu.Lock()
	credentials := slices.Clone(s.credentials)
	s.mu.Unlock()
	require.Equal(t, []string{"Bearer test", "Bearer test", "Bearer new-auth"}, credentials)

	inputs["cpu"], inputs["max"] = nil, 6
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionUpdate)))
	mutations, patches := s.snapshot()
	require.Equal(t, []string{"POST " + name, "PATCH " + name}, mutations)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"hpa-generated-actual-1"},
		{"op":"replace","path":"/spec/maxReplicas","value":6}
	]`, patches[0])
	s.edit(name, func(live *autoscalingv1.HorizontalPodAutoscaler) {
		live.Spec.TargetCPUUtilizationPercentage = new(int32(88))
	})
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionUpdate)))
	unchangedMutations, unchangedPatches := s.snapshot()
	require.Equal(t, mutations, unchangedMutations)
	require.Equal(t, patches, unchangedPatches)
	spec := hpaStoredObject(t, store)["spec"].(map[string]any)
	require.EqualValues(t, 88, spec["target-cpu-utilization-percentage"])
	s.edit(name, func(live *autoscalingv1.HorizontalPodAutoscaler) { live.Spec.MaxReplicas = 33 })
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionUpdate)))
	mutations, patches = s.snapshot()
	require.Equal(t, []string{"POST " + name, "PATCH " + name, "PATCH " + name}, mutations)
	require.JSONEq(t, patches[0], patches[1])
	require.Equal(t, uid, hpaStoredObject(t, store)["metadata"].(map[string]any)["uid"])

	inputs["prefix"] = "new-prefix-"
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionReplace)))
	inputs["prefix"], inputs["name"] = nil, "explicit"
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionReplace)))
	inputs["name"] = "renamed"
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionReplace)))
	inputs["namespace"] = "custom"
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionReplace)))
	mutations, _ = s.snapshot()
	require.Equal(t, []string{
		"POST " + name, "PATCH " + name, "PATCH " + name,
		"DELETE " + name, "POST new-prefix-actual",
		"DELETE new-prefix-actual", "POST explicit",
		"DELETE explicit", "POST renamed",
		"DELETE renamed", "POST renamed",
	}, mutations)
	metadata = hpaStoredObject(t, store)["metadata"].(map[string]any)
	require.Equal(t, "custom", metadata["namespace"])
	require.NotEqual(t, uid, metadata["uid"])

	other := newHPAPlanServer(t)
	inputs["server"] = other.server
	replacement := hpaPlan(t, e, runtime.DecisionReplace)
	require.Equal(t, []string{"server"}, hpaStep(t, replacement).ReplacementReasons)
	require.NoError(t, hpaApply(t, e, replacement))
	actual, _ := s.snapshot()
	require.Equal(t, append(mutations, "DELETE renamed"), actual)
	actual, _ = other.snapshot()
	require.Equal(t, []string{"POST renamed"}, actual)
	snapshot, err := store.Current()
	require.NoError(t, err)
	require.Equal(t, other.server, snapshot.Find("resource.hpa").Outputs["server"])
}

func TestHPAV1SavedPlanMetadataPolicy(t *testing.T) {
	s := newHPAPlanServer(t)
	store := hpaStateStore(t)
	inputs := map[string]any{
		"server": s.server, "name": "hpa",
		"labels":        map[string]any{"example.com/tracked": "old"},
		"annotations":   map[string]any{"wanted": "prior"},
		"ignore-labels": []any{"^hidden$", "^reveal$"}, "ignore-annotations": []any{"^hidden$"},
	}
	e := hpaExecutor(t, "state", hpaPlanLibrary(hpaNativeSchema(t)), store, inputs)
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionCreate)))
	s.edit("hpa", func(live *autoscalingv1.HorizontalPodAutoscaler) {
		maps.Copy(live.Labels, map[string]string{
			"hidden": "old", "reveal": "external", "ignore-next": "external",
			"node.kubernetes.io/keep": "external",
		})
		live.Annotations["hidden"] = "external"
	})
	inputs["labels"] = map[string]any{"hidden": "new"}
	inputs["annotations"], inputs["ignore-annotations"] = nil, nil
	inputs["ignore-labels"] = []any{"^hidden$", "^example.com/tracked$", "^ignore-next$"}
	plan := hpaPlan(t, e, runtime.DecisionUpdate)
	priorObject := hpaStep(t, plan).ObservedOutputs["object"].(map[string]any)
	priorMeta := priorObject["metadata"].(map[string]any)
	require.Equal(t, map[string]any{"example.com/tracked": "old", "ignore-next": "external"},
		priorMeta["labels"])
	require.Equal(t, map[string]any{"wanted": "prior"}, priorMeta["annotations"])
	require.NoError(t, hpaApply(t, e, plan))
	mutations, patches := s.snapshot()
	require.Equal(t, []string{"POST hpa", "PATCH hpa"}, mutations)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"hpa-1"},
		{"op":"remove","path":"/metadata/annotations/hidden"},
		{"op":"remove","path":"/metadata/annotations/wanted"},
		{"op":"remove","path":"/metadata/labels/example.com~1tracked"},
		{"op":"remove","path":"/metadata/labels/reveal"},
		{"op":"replace","path":"/metadata/labels/hidden","value":"new"}
	]`, patches[0])
	metadata := hpaStoredObject(t, store)["metadata"].(map[string]any)
	require.Equal(t, map[string]any{"hidden": "new"}, metadata["labels"])
	require.Empty(t, metadata["annotations"])
	s.edit("hpa", func(live *autoscalingv1.HorizontalPodAutoscaler) {
		require.Equal(t, map[string]string{
			"hidden": "new", "ignore-next": "external", "node.kubernetes.io/keep": "external",
		}, live.Labels)
		require.Empty(t, live.Annotations)
	})
	inputs["ignore-labels"] = nil
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionUpdate)))
	mutations, patches = s.snapshot()
	require.Equal(t, []string{"POST hpa", "PATCH hpa", "PATCH hpa"}, mutations)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"hpa-1"},
		{"op":"remove","path":"/metadata/labels/ignore-next"}
	]`, patches[1])
}

type hpaLegacyInputEquality struct{ runtime.ResourceRegistration }

func (hpaLegacyInputEquality) InputsEqual(any, map[string]any) (bool, error) {
	return true, nil
}

func (hpaLegacyInputEquality) KnownInputsEqual(any, map[string]any, map[string][]string) (
	bool, error,
) {
	return true, nil
}

func TestHPAV1EvaluatedNativeAndSavedNoOp(t *testing.T) {
	s := newHPAPlanServer(t)
	store := hpaStateStore(t)
	inputs := map[string]any{"server": s.server, "name": "hpa"}
	library := hpaPlanLibrary(hpaNativeSchema(t))
	e := hpaExecutor(t, "state", library, store, inputs)
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionCreate)))
	before := hpaStoredObject(t, store)
	for _, tc := range []struct {
		name   string
		inputs map[string]any
		rule   string
	}{
		{"missing identity", map[string]any{"name": nil}, "exactly-one-of"},
		{"both identities", map[string]any{"prefix": "extra-"}, "exactly-one-of"},
		{"name", map[string]any{"name": "BAD"}, "metadata.name"},
		{"name limit", map[string]any{"name": strings.Repeat("a", 254)}, "metadata.name"},
		{"prefix", map[string]any{"name": nil, "prefix": "invalid.prefix-"}, "metadata.generate-name"},
		{"label key", map[string]any{"labels": map[string]any{"bad label": "ok"}}, "metadata.labels"},
		{"label value", map[string]any{"labels": map[string]any{"good": "bad value"}}, "metadata.labels"},
		{"annotation", map[string]any{"annotations": map[string]any{"ill?": "ok"}},
			"metadata.annotations"},
		{"label regex", map[string]any{"ignore-labels": []any{"["}}, "ignore-labels"},
		{"annotation regex", map[string]any{"ignore-annotations": []any{"["}}, "ignore-annotations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.Inputs = maps.Clone(inputs)
			maps.Copy(e.Inputs, tc.inputs)
			_, err := e.Plan(t.Context())
			require.ErrorContains(t, err, "resource.hpa: schema: constraints[")
			require.ErrorContains(t, err, tc.rule)
		})
	}
	e.Inputs = inputs
	inputs["max"] = nil
	_, err := e.Plan(t.Context())
	require.ErrorContains(t, err, "field \"object.spec.max-replicas\": required but is null")
	inputs["max"] = 4
	require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionNoOp)))
	library.Resources["horizontal-pod-autoscaler"] = hpaLegacyInputEquality{
		library.Resources["horizontal-pod-autoscaler"],
	}
	inputs["labels"] = map[string]any{"good": "invalid value"}
	_, err = e.Plan(t.Context())
	require.ErrorContains(t, err, "resource.hpa: schema: constraints[")
	require.ErrorContains(t, err, "metadata.labels")
	constraints := library.Constraints
	library.Constraints = nil
	saved := hpaPlan(t, e, runtime.DecisionNoOp)
	library.Constraints = constraints
	err = hpaApply(t, e, saved)
	require.ErrorContains(t, err, "resource.hpa: schema: constraints[")
	require.ErrorContains(t, err, "metadata.labels")
	mutations, patches := s.snapshot()
	require.Equal(t, []string{"POST hpa"}, mutations)
	require.Empty(t, patches)
	require.Equal(t, before, hpaStoredObject(t, store))
}

func TestHPAV1DeferredNative(t *testing.T) {
	schema := hpaNativeSchema(t)
	for _, tc := range []struct {
		name, fixture, rule string
		existing            bool
		validMutations      []string
	}{
		{"create", "deferred-label", "metadata.labels", false, []string{"POST seed", "POST hpa"}},
		{"update", "deferred-label", "metadata.labels", true, []string{"POST seed", "PATCH hpa"}},
		{"replace", "deferred-name", "metadata.name", true,
			[]string{"POST seed", "DELETE hpa", "POST valid"}},
	} {
		for _, candidate := range []string{"valid", "BAD!"} {
			t.Run(tc.name+"/"+candidate, func(t *testing.T) {
				s := newHPAPlanServer(t)
				store := hpaStateStore(t)
				library := hpaPlanLibrary(schema)
				inputs := map[string]any{"server": s.server, "name": "hpa"}
				var before map[string]any
				var wantBefore []string
				decision := runtime.DecisionCreate
				if tc.existing {
					e := hpaExecutor(t, "state", library, store, inputs)
					require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionCreate)))
					before = hpaStoredObject(t, store)
					wantBefore = []string{"POST hpa"}
					decision = runtime.DecisionReplace
				}
				inputs["candidate"], inputs["max"] = candidate, 6
				e := hpaExecutor(t, tc.fixture, library, store, inputs)
				plan := hpaPlan(t, e, decision)
				step := hpaStep(t, plan)
				require.Contains(t, step.UnresolvedInputs, "object")
				if tc.existing {
					require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
					require.Contains(t, step.AllowedDecisions, runtime.DecisionUpdate)
				}
				mutations, _ := s.snapshot()
				require.Equal(t, wantBefore, mutations)
				err := hpaApply(t, e, plan)
				mutations, _ = s.snapshot()
				if candidate == "valid" {
					require.NoError(t, err)
					require.Equal(t, append(wantBefore, tc.validMutations...), mutations)
					metadata := hpaStoredObject(t, store)["metadata"].(map[string]any)
					if tc.fixture == "deferred-label" {
						require.Equal(t, map[string]any{"value": "valid"}, metadata["labels"])
					} else {
						require.Equal(t, "valid", metadata["name"])
					}
					return
				}
				require.ErrorContains(t, err, "resource.hpa: schema: constraints[")
				require.ErrorContains(t, err, tc.rule)
				require.Equal(t, append(wantBefore, "POST seed"), mutations)
				if tc.existing {
					require.Equal(t, before, hpaStoredObject(t, store))
				} else {
					state, err := store.Current()
					require.NoError(t, err)
					require.Nil(t, state.Find("resource.hpa"))
				}
			})
		}
	}
}

func TestHPAV1HistoricalMetadataTeardown(t *testing.T) {
	schema := hpaNativeSchema(t)
	for _, name := range []string{"destroy", "replace"} {
		t.Run(name, func(t *testing.T) {
			s := newHPAPlanServer(t)
			store := hpaStateStore(t)
			library := hpaPlanLibrary(schema)
			constraints := library.Constraints
			library.Constraints = nil
			inputs := map[string]any{
				"server": s.server, "name": "historical", "ignore-labels": []any{"["},
			}
			e := hpaExecutor(t, "state", library, store, inputs)
			require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionCreate)))
			library.Constraints = constraints
			_, err := e.Plan(t.Context())
			require.ErrorContains(t, err, "resource.hpa: schema: constraints[")
			require.ErrorContains(t, err, "ignore-labels")
			want := []string{"POST historical", "DELETE historical"}
			if name == "replace" {
				inputs["name"], inputs["ignore-labels"] = "new", nil
				require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionReplace)))
				want = append(want, "POST new")
			} else {
				e.Destroy = true
				require.NoError(t, hpaApply(t, e, hpaPlan(t, e, runtime.DecisionDestroy)))
				state, err := store.Current()
				require.NoError(t, err)
				require.Nil(t, state.Find("resource.hpa"))
			}
			mutations, patches := s.snapshot()
			require.Equal(t, want, mutations)
			require.Empty(t, patches)
		})
	}
}
