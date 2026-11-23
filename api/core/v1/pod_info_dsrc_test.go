package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

const (
	podInfoGet          = "GET /api/v1/namespaces/default/pods/pod"
	podInfoNameRule     = "constraints[0] (predicate)"
	podInfoPrefixRule   = "constraints[1] (predicate)"
	podInfoLabelKeyRule = "constraints[2] (predicate)"
	podInfoLabelRule    = "constraints[3] (predicate)"
	podInfoAnnRule      = "constraints[4] (predicate)"
)

func podInfoLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
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
		"data-source.pod-info": schema.DataSources["pod-info"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.pod-info": schema.DataSources["pod-info"].Defaults,
	}
	return lib
}

func podInfoExecutor(
	t *testing.T, lib *runtime.Library, fixture string, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub", "pod-info-"+fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": lib}
	store, err := local.NewStore(t.TempDir(), "pod-info", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store, Inputs: inputs,
		Factory: state.FactoryInfo{Name: "pod-info", Version: "v0", ContentRevision: "c0"},
	}
}

type podInfoServer struct {
	*httptest.Server
	mu       sync.Mutex
	pod      *corev1.Pod
	requests []string
}

func newPodInfoServer(t *testing.T, exists bool) *podInfoServer {
	t.Helper()
	test := &podInfoServer{}
	if exists {
		test.pod = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default", UID: "uid"},
			Spec:       corev1.PodSpec{NodeName: "node-a"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	test.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		test.mu.Lock()
		defer test.mu.Unlock()
		test.requests = append(test.requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if test.pod == nil {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
				Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
			}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(test.pod))
	}))
	t.Cleanup(test.Close)
	return test
}

func (s *podInfoServer) requestLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *podInfoServer) resetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func (s *podInfoServer) changeObservation(change string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch change {
	case "phase":
		s.pod.Status.Phase = corev1.PodFailed
	case "spec":
		s.pod.Spec.NodeName = "node-b"
	case "disappeared":
		s.pod = nil
	case "appeared":
		s.pod = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"}}
	}
}

func podInfoAssertConstraint(t *testing.T, err error, rule string) {
	t.Helper()
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[")
	require.ErrorContains(t, err, rule)
}

func TestPodInfoEvaluatedNativeInputs(t *testing.T) {
	lib := podInfoLibrary(t)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		path     string
		rule     string
	}{
		{name: "omitted namespace", path: podInfoGet},
		{name: "null namespace and maps", metadata: map[string]any{
			"namespace": nil, "labels": nil, "annotations": nil,
		}, path: podInfoGet},
		{name: "empty namespace and maps", metadata: map[string]any{
			"namespace": "", "labels": map[string]any{}, "annotations": map[string]any{},
		}, path: "GET /api/v1/pods/pod"},
		{name: "explicit namespace", metadata: map[string]any{"namespace": "Other"},
			path: "GET /api/v1/namespaces/Other/pods/pod"},
		{name: "dotted name", metadata: map[string]any{"name": "a.b-c"},
			path: "GET /api/v1/namespaces/default/pods/a.b-c"},
		{name: "max name", metadata: map[string]any{"name": strings.Repeat("a", 253)},
			path: "GET /api/v1/namespaces/default/pods/" + strings.Repeat("a", 253)},
		{name: "long name", metadata: map[string]any{"name": strings.Repeat("a", 254)},
			rule: podInfoNameRule},
		{name: "uppercase name", metadata: map[string]any{"name": "Pod"}, rule: podInfoNameRule},
		{name: "empty name", metadata: map[string]any{"name": ""}, rule: podInfoNameRule},
		{name: "max label prefix", metadata: map[string]any{"generate-name": strings.Repeat("a", 63)},
			path: podInfoGet},
		{name: "trailing-dash max", metadata: map[string]any{
			"generate-name": strings.Repeat("a", 62) + "@-",
		}, path: podInfoGet},
		{name: "one masked byte", metadata: map[string]any{"generate-name": "@-"}, path: podInfoGet},
		{name: "ordinary trailing dash", metadata: map[string]any{"generate-name": "pref-"},
			path: podInfoGet},
		{name: "null prefix", metadata: map[string]any{"generate-name": nil}, path: podInfoGet},
		{name: "long prefix", metadata: map[string]any{
			"generate-name": strings.Repeat("a", 64),
		}, rule: podInfoPrefixRule},
		{name: "long masked prefix", metadata: map[string]any{
			"generate-name": strings.Repeat("a", 63) + "@-",
		}, rule: podInfoPrefixRule},
		{name: "uppercase prefix", metadata: map[string]any{"generate-name": "Up-"},
			rule: podInfoPrefixRule},
		{name: "empty prefix", metadata: map[string]any{"generate-name": ""}, rule: podInfoPrefixRule},
		{name: "map values and null entries", metadata: map[string]any{
			"labels":      map[string]any{"example.com/Label": "", "bad key": nil},
			"annotations": map[string]any{"EXAMPLE.İK/Name": "arbitrary whitespace !", "bad key": nil},
		}, path: podInfoGet},
		{name: "map key and value boundaries", metadata: map[string]any{
			"labels": map[string]any{
				strings.Repeat("a", 253) + "/" + strings.Repeat("b", 63): strings.Repeat("z", 63),
			},
			"annotations": map[string]any{strings.Repeat("K", 253) + "/" + strings.Repeat("İ", 63): ""},
		}, path: podInfoGet},
		{name: "label prefix syntax", metadata: map[string]any{
			"labels": map[string]any{"UPPER/valid": ""},
		}, rule: podInfoLabelKeyRule},
		{name: "label prefix too long", metadata: map[string]any{
			"labels": map[string]any{strings.Repeat("a", 254) + "/key": ""},
		}, rule: podInfoLabelKeyRule},
		{name: "label key too long", metadata: map[string]any{
			"labels": map[string]any{strings.Repeat("a", 64): ""},
		}, rule: podInfoLabelKeyRule},
		{name: "label value syntax", metadata: map[string]any{
			"labels": map[string]any{"app": "with space"},
		}, rule: podInfoLabelRule},
		{name: "label value too long", metadata: map[string]any{
			"labels": map[string]any{"app": strings.Repeat("a", 64)},
		}, rule: podInfoLabelRule},
		{name: "annotation key syntax", metadata: map[string]any{
			"annotations": map[string]any{"BAD!/valid": ""},
		}, rule: podInfoAnnRule},
		{name: "annotation prefix too long", metadata: map[string]any{
			"annotations": map[string]any{strings.Repeat("a", 254) + "/key": ""},
		}, rule: podInfoAnnRule},
		{name: "annotation key too long", metadata: map[string]any{
			"annotations": map[string]any{strings.Repeat("İ", 64): ""},
		}, rule: podInfoAnnRule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newPodInfoServer(t, false)
			metadata := map[string]any{"name": "pod"}
			for key, value := range tc.metadata {
				metadata[key] = value
			}
			e := podInfoExecutor(t, lib, "inputs", map[string]any{
				"server": server.URL, "metadata": metadata,
			})
			plan, err := e.Plan(t.Context())
			if tc.rule != "" {
				podInfoAssertConstraint(t, err, tc.rule)
				require.Empty(t, server.requestLog())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{tc.path}, server.requestLog())
			e.Inputs = nil
			result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, map[string]any{"phase": nil, "spec": nil}, result.Outputs)
			require.Equal(t, []string{tc.path, tc.path}, server.requestLog())
		})
	}
}

type podInfoSeed struct {
	MetadataInput svc.PodLookupMetadata
	ValueInput    *string
	run           func()
}

type podInfoSeedOutput struct {
	Metadata svc.PodLookupMetadata
	Value    *string
}

func (s *podInfoSeed) Run(context.Context, any) (*podInfoSeedOutput, error) {
	s.run()
	return &podInfoSeedOutput{Metadata: s.MetadataInput, Value: s.ValueInput}, nil
}

func podInfoRegisterSeed(lib *runtime.Library, run func()) {
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeActionWith[podInfoSeed, *podInfoSeedOutput, any](
			func() *podInfoSeed { return &podInfoSeed{run: run} },
		),
	}
}

func podInfoReadStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "data-source.endpoint" {
			require.Equal(t, runtime.DecisionRead, step.Decision)
			return step
		}
	}
	t.Fatal("no read step for data-source.endpoint")
	return nil
}

func TestPodInfoDeferredNativeInputs(t *testing.T) {
	lib := podInfoLibrary(t)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		rule     string
	}{
		{name: "valid", metadata: map[string]any{"generate-name": "pref-"}},
		{name: "null values", metadata: map[string]any{
			"annotations": map[string]any{"bad key": nil, "GOOD/Key": "annotation"},
			"labels":      map[string]any{"bad key": nil, "app": "label"},
		}},
		{name: "name", metadata: map[string]any{"name": "Bad_name"}, rule: podInfoNameRule},
		{name: "prefix", metadata: map[string]any{"generate-name": "Upper-"}, rule: podInfoPrefixRule},
		{name: "label key", metadata: map[string]any{
			"labels": map[string]any{"bad key": ""},
		}, rule: podInfoLabelKeyRule},
		{name: "label value", metadata: map[string]any{
			"labels": map[string]any{"app": "bad value"},
		}, rule: podInfoLabelRule},
		{name: "annotation", metadata: map[string]any{
			"annotations": map[string]any{"bad key": ""},
		}, rule: podInfoAnnRule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newPodInfoServer(t, false)
			seedRuns := 0
			podInfoRegisterSeed(lib, func() {
				require.Empty(t, server.requestLog())
				seedRuns++
			})
			metadata := map[string]any{"name": "pod"}
			for key, value := range tc.metadata {
				metadata[key] = value
			}
			e := podInfoExecutor(t, lib, "deferred", map[string]any{
				"server": server.URL, "metadata": metadata,
			})
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Zero(t, seedRuns)
			require.Empty(t, server.requestLog())
			step := podInfoReadStep(t, plan)
			require.Nil(t, step.ObservedOutputs)
			require.Contains(t, step.UnresolvedInputs, "metadata")
			e.Inputs = nil
			result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.Equal(t, 1, seedRuns)
			if tc.rule != "" {
				podInfoAssertConstraint(t, err, tc.rule)
				require.Empty(t, server.requestLog())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{podInfoGet}, server.requestLog())
			require.Nil(t, result.Outputs["phase"])
			if tc.name == "null values" {
				out := result.Outputs["metadata"].(map[string]any)
				require.Equal(t, map[string]any{"GOOD/Key": "annotation"}, out["annotations"])
				require.Equal(t, map[string]any{"app": "label"}, out["labels"])
			}
		})
	}
}

func TestPodInfoPartiallyKnownMaps(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fixture    string
		badKey     string
		pendingKey string
		known      any
		value      any
		planRule   string
		applyRule  string
		labels     map[string]any
	}{
		{name: "literal known violation", fixture: "literal", known: "", planRule: podInfoLabelKeyRule},
		{name: "comprehension known violation", fixture: "comprehension",
			badKey: "bad key", pendingKey: "app", known: "", planRule: podInfoLabelKeyRule},
		{name: "literal known null", fixture: "literal", value: "available",
			labels: map[string]any{"app": "available"}},
		{name: "comprehension known null", fixture: "comprehension",
			badKey: "bad key", pendingKey: "app", value: "available",
			labels: map[string]any{"app": "available"}},
		{name: "comprehension pending invalid guard", fixture: "comprehension",
			badKey: "app", pendingKey: "bad key", known: "", value: "", applyRule: podInfoLabelKeyRule},
		{name: "literal pending invalid guard", fixture: "pending-value",
			known: "", value: "", applyRule: podInfoLabelKeyRule},
		{name: "literal pending null guard", fixture: "pending-value",
			known: "", labels: map[string]any{"app": ""}},
		{name: "pending key", fixture: "pending-key", known: "", value: "bad key",
			applyRule: podInfoLabelKeyRule},
		{name: "pending filter includes invalid key", fixture: "pending-filter",
			known: "", value: "include", applyRule: podInfoLabelKeyRule},
		{name: "pending filter excludes invalid key", fixture: "pending-filter",
			known: "", value: "exclude", labels: map[string]any{"app": ""}},
		{name: "pending membership", fixture: "pending-membership",
			known: "", value: "include", applyRule: podInfoLabelKeyRule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lib := podInfoLibrary(t)
			server := newPodInfoServer(t, false)
			seedRuns := 0
			podInfoRegisterSeed(lib, func() {
				require.Empty(t, server.requestLog())
				seedRuns++
			})
			e := podInfoExecutor(t, lib, tc.fixture, map[string]any{
				"server": server.URL, "bad-key": tc.badKey, "pending-key": tc.pendingKey,
				"known": tc.known, "value": tc.value,
			})
			plan, err := e.Plan(t.Context())
			require.Zero(t, seedRuns)
			require.Empty(t, server.requestLog())
			if tc.planRule != "" {
				podInfoAssertConstraint(t, err, tc.planRule)
				require.ErrorContains(t, err, "metadata.labels['bad key']")
				return
			}
			require.NoError(t, err)
			step := podInfoReadStep(t, plan)
			require.Nil(t, step.ObservedOutputs)
			require.Contains(t, step.UnresolvedInputs, "metadata")
			e.Inputs = nil
			result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.Equal(t, 1, seedRuns)
			if tc.applyRule != "" {
				podInfoAssertConstraint(t, err, tc.applyRule)
				require.ErrorContains(t, err, "metadata.labels['bad key']")
				require.Empty(t, server.requestLog())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{podInfoGet}, server.requestLog())
			require.Equal(t, tc.labels, result.Outputs["metadata"].(map[string]any)["labels"])
		})
	}
}

func TestPodInfoSteadyAndSavedNativeInputs(t *testing.T) {
	for _, tc := range []struct {
		metadata map[string]any
		rule     string
	}{
		{map[string]any{"name": "pod"}, ""},
		{map[string]any{"name": "Bad_Name"}, podInfoNameRule},
		{map[string]any{"name": "pod", "generate-name": "UPPER"}, podInfoPrefixRule},
		{map[string]any{"name": "pod", "labels": map[string]any{"bad key": "label"}},
			podInfoLabelKeyRule},
		{map[string]any{"name": "pod", "annotations": map[string]any{"bad key": "text"}}, podInfoAnnRule},
	} {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			lib := podInfoLibrary(t)
			lib.Resources["stable"] = runtime.MakeResource[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			](runtime.ResourceDefinition[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			]{SchemaVersion: 1})
			server := newPodInfoServer(t, true)
			e := podInfoExecutor(t, lib, "steady", map[string]any{
				"server": server.URL, "metadata": map[string]any{"name": "pod"},
			})
			initial, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Nil(t, podInfoReadStep(t, initial).ObservedOutputs)
			require.Empty(t, server.requestLog())
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, initial))
			require.NoError(t, err)
			require.Equal(t, []string{podInfoGet}, server.requestLog())
			server.resetRequests()

			steady, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{podInfoGet}, server.requestLog())
			require.Equal(t, "Running", podInfoReadStep(t, steady).ObservedOutputs["status"])
			var decisions []runtime.Decision
			for _, step := range steady.Steps {
				if step.Address == "resource.stable" {
					decisions = append(decisions, step.Decision)
				}
			}
			require.Equal(t, []runtime.Decision{runtime.DecisionNoOp}, decisions)
			server.resetRequests()
			e.Inputs["metadata"] = tc.metadata
			_, err = e.Plan(t.Context())
			if tc.rule == "" {
				require.NoError(t, err)
				require.Equal(t, []string{podInfoGet}, server.requestLog())
			} else {
				podInfoAssertConstraint(t, err, tc.rule)
				require.Empty(t, server.requestLog())
			}
			server.resetRequests()

			// Apply must evaluate the saved source again with its persisted inputs.
			e.Inputs = nil
			steady.Inputs["metadata"] = tc.metadata
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, steady))
			if tc.rule == "" {
				require.NoError(t, err)
				require.Equal(t, []string{podInfoGet}, server.requestLog())
			} else {
				podInfoAssertConstraint(t, err, tc.rule)
				require.Empty(t, server.requestLog())
			}
		})
	}
}

func TestPodInfoSavedObservations(t *testing.T) {
	lib := podInfoLibrary(t)
	for _, change := range []string{"stable", "phase", "spec", "disappeared", "appeared"} {
		t.Run(change, func(t *testing.T) {
			server := newPodInfoServer(t, change != "appeared")
			e := podInfoExecutor(t, lib, "inputs", map[string]any{
				"server": server.URL, "metadata": map[string]any{"name": "pod"},
			})
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{podInfoGet}, server.requestLog())
			observed := podInfoReadStep(t, plan).ObservedOutputs
			if change == "appeared" {
				require.Nil(t, observed["status"])
				require.Nil(t, observed["spec"])
			} else {
				require.Equal(t, "Running", observed["status"])
				require.Equal(t, "node-a", observed["spec"].(map[string]any)["node-name"])
			}
			server.changeObservation(change)
			e.Inputs = nil
			result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.Equal(t, []string{podInfoGet, podInfoGet}, server.requestLog())
			if change == "stable" {
				require.NoError(t, err)
				require.Equal(t, "Running", result.Outputs["phase"])
				require.Equal(t, observed["spec"], result.Outputs["spec"])
			} else {
				require.ErrorContains(t, err, "data source data-source.endpoint changed since the plan")
				require.ErrorContains(t, err, "plan again")
			}
		})
	}
}

func TestPodInfoRemovalAndDestroy(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint(destroy), func(t *testing.T) {
			lib := podInfoLibrary(t)
			lib.Resources["stable"] = runtime.MakeResource[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			](runtime.ResourceDefinition[
				configMapInfoStable, *configMapInfoStableOutput, *config.Configuration,
			]{SchemaVersion: 1})
			server := newPodInfoServer(t, true)
			inputs := map[string]any{"server": server.URL, "metadata": map[string]any{"name": "pod"}}
			e := podInfoExecutor(t, lib, "steady", inputs)
			initial, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, initial))
			require.NoError(t, err)
			require.Equal(t, []string{podInfoGet}, server.requestLog())
			server.resetRequests()
			if destroy {
				e.Destroy = true
			} else {
				removed := podInfoExecutor(t, lib, "removed", inputs)
				removed.Store = e.Store
				e = removed
			}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			result, err := e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
			require.NoError(t, err)
			require.Empty(t, server.requestLog())
			require.Equal(t, map[string]any{}, result.Data)
		})
	}
}

func TestPodInfoRuntimeReadDeadlines(t *testing.T) {
	lib := podInfoLibrary(t)
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply=%t", apply), func(t *testing.T) {
			requests := make(chan string, 10)
			hang := make(chan struct{})
			release := make(chan struct{})
			canceled := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Method + " " + r.URL.RequestURI()
				select {
				case <-hang:
					select {
					case <-r.Context().Done():
						canceled <- struct{}{}
					case <-release:
					}
					return
				default:
					w.Header().Set("Content-Type", "application/json")
					require.NoError(t, json.NewEncoder(w).Encode(&corev1.Pod{}))
				}
			}))
			t.Cleanup(server.Close)
			defer close(release)
			e := podInfoExecutor(t, lib, "timeout", map[string]any{"server": server.URL})
			if apply {
				plan, err := e.Plan(t.Context())
				require.NoError(t, err)
				require.Len(t, requests, 1)
				require.Equal(t, podInfoGet, <-requests)
				close(hang)
				e.Inputs = nil
				_, err = e.ApplyPlan(t.Context(), configMapInfoSavedPlan(t, plan))
				require.ErrorContains(t, err, "context deadline exceeded")
			} else {
				close(hang)
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				_, err := e.Plan(ctx)
				require.ErrorContains(t, err, "context deadline exceeded")
			}
			require.Len(t, requests, 1)
			require.Equal(t, podInfoGet, <-requests)
			require.Empty(t, requests)
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("request context did not end")
			}
		})
	}
}
