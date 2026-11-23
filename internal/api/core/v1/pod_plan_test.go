package v1

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type podTestServer struct {
	t         *testing.T
	mu        sync.Mutex
	pods      map[string]*corev1.Pod
	creates   map[string]int
	mutations []string
	patches   [][]byte
	seedValue string
	config    *config.Configuration
}

func newPodTestServer(t *testing.T) *podTestServer {
	t.Helper()
	fake := &podTestServer{t: t, pods: map[string]*corev1.Pod{}, creates: map[string]int{}}
	fake.config = configMapAPI(t, fake.serve)
	return fake
}

func (s *podTestServer) serve(w http.ResponseWriter, req *http.Request) {
	t := s.t
	if podTestDiscovery(t, w, req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := path.Base(req.URL.Path)
	if req.Method != http.MethodGet {
		s.mutations = append(s.mutations, req.Method+" "+name)
	}
	switch req.Method {
	case http.MethodPost:
		pod := &corev1.Pod{}
		require.NoError(t, json.NewDecoder(req.Body).Decode(pod))
		s.creates[pod.Name]++
		uid := "uid-" + pod.Name
		if s.creates[pod.Name] > 1 {
			uid += "-" + strconv.Itoa(s.creates[pod.Name])
		}
		pod.UID = types.UID(uid)
		pod.Status.Phase = corev1.PodPending
		if pod.Name == "seed" {
			pod.Annotations = map[string]string{"value": s.seedValue}
		}
		s.pods[pod.Name] = pod
		respond(t, w, pod)
	case http.MethodGet:
		if pod := s.pods[name]; pod != nil {
			respond(t, w, pod)
		} else {
			namespaceStatus(t, w, 404)
		}
	case http.MethodPatch:
		pod := s.pods[name]
		if pod == nil {
			namespaceStatus(t, w, 404)
			return
		}
		require.Equal(t, string(types.JSONPatchType), req.Header.Get("Content-Type"))
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		s.patches = append(s.patches, body)
		patch, err := jsonpatch.DecodePatch(body)
		require.NoError(t, err)
		before, err := json.Marshal(pod)
		require.NoError(t, err)
		after, err := patch.Apply(before)
		require.NoError(t, err)
		var updated corev1.Pod
		require.NoError(t, json.Unmarshal(after, &updated))
		s.pods[name] = &updated
		respond(t, w, &updated)
	case http.MethodDelete:
		pod := s.pods[name]
		if pod == nil {
			namespaceStatus(t, w, 404)
			return
		}
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, metav1.DeleteOptions{
			TypeMeta:      metav1.TypeMeta{APIVersion: "v1", Kind: "DeleteOptions"},
			Preconditions: &metav1.Preconditions{UID: &pod.UID},
		}, options)
		delete(s.pods, name)
		respond(t, w, metav1.Status{Status: "Success"})
	default:
		t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		namespaceStatus(t, w, 400)
	}
}

func (s *podTestServer) takeMutations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutations := s.mutations
	s.mutations = nil
	return mutations
}

func podTestDiscovery(t *testing.T, w http.ResponseWriter, req *http.Request) bool {
	t.Helper()
	switch req.URL.Path {
	case "/api":
		respond(t, w, metav1.APIVersions{Versions: []string{"v1"}})
	case "/apis":
		respond(t, w, metav1.APIGroupList{})
	case "/api/v1":
		respond(t, w, metav1.APIResourceList{
			GroupVersion: "v1", APIResources: []metav1.APIResource{{
				Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"get", "delete"},
			}},
		})
	default:
		return false
	}
	return true
}

func podExecutor(
	t *testing.T, server *podTestServer, store state.Backend, fixture string, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub/pod", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	base := runtime.MakeResource[
		PodResource, *PodResourceOutput, *config.Configuration,
	]((&PodResource{}).ResourceDefinition())
	extracted, err := nativeCoreSchema()
	require.NoError(t, err)
	pod := *extracted.Resources["pod"]
	pod.EvaluatedInputs = pod.Inputs
	library := &runtime.Library{
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"pod": PodRegistration[PodResource, *PodResourceOutput](
				base, base.Read, base.KnownReplacementReasons),
		},
		Schema:      &runtime.LibrarySchema{Resources: map[string]*runtime.TypeSchema{"pod": &pod}},
		Defaults:    map[string][]lang.DefaultSpec{"resource.pod": pod.Defaults},
		Constraints: map[string][]lang.ConstraintSpec{"resource.pod": pod.Constraints},
	}
	libraries := make(map[string]*runtime.Library, len(parsed.Factory.Body.Imports))
	for _, imp := range parsed.Factory.Body.Imports {
		libraries[imp.Alias.Name] = library
	}
	inputs["server"] = *server.config.Server
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libraries), SyntaxSource: &parsed.Factory.Body,
		Libraries: libraries, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "pod", Version: "v0", ContentRevision: "v0"},
	}
}

func podSteadyInputs() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "subject"},
		"spec":     map[string]any{"container": []any{map[string]any{"name": "app", "image": "image"}}},
	}
}

func podSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func podPlanStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for i := range plan.Steps {
		step := plan.Steps[i]
		if step.Address == "resource.subject" {
			return step
		}
	}
	t.Fatal("the plan has no resource.subject step")
	return nil
}

func TestPodSavedNoOpAndHistoricalDestroy(t *testing.T) {
	server := newPodTestServer(t)
	store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := podSteadyInputs()
	exec := podExecutor(t, server, store, "steady", inputs)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionCreate, podPlanStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST pods"}, server.takeMutations())

	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	step := podPlanStep(t, plan)
	require.Equal(t, runtime.DecisionNoOp, step.Decision)
	require.Equal(t, "uid-subject", step.ObservedOutputs["uid"])
	file := podSavedPlan(t, plan)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.Empty(t, server.takeMutations())
	prior, err := store.Current()
	require.NoError(t, err)
	entry := prior.Find("resource.subject")
	require.Equal(t, step.ObservedOutputs, entry.Outputs)

	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	file = podSavedPlan(t, plan)
	inputs["metadata"] = map[string]any{}
	entry.Inputs["metadata"] = map[string]any{"namespace": "default"}
	invalid := podExecutor(t, server, store, "steady", inputs)
	file.Inputs["metadata"] = inputs["metadata"]
	for i := range file.Steps {
		if file.Steps[i].Address == "resource.subject" {
			file.Steps[i].Inputs["metadata"] = entry.Inputs["metadata"]
		}
	}
	_, err = invalid.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.ErrorContains(t, err, "exactly-one-of [metadata.name, metadata.generate-name]")
	require.Empty(t, server.takeMutations())

	revision, err := store.Write(prior)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(revision))
	_, err = invalid.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.Empty(t, server.takeMutations())
	invalid.Destroy = true
	plan, err = invalid.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionDestroy, podPlanStep(t, plan).Decision)
	_, err = invalid.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"DELETE subject"}, server.takeMutations())
}

func TestPodHistoricalCleanupInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		inputs map[string]any
	}{
		{"malformed ignore patterns", map[string]any{
			"ignore-labels":      []any{"[", "^ignored"},
			"ignore-annotations": []any{"^ignored", "("},
		}},
		{"invalid timeout", map[string]any{"delete-timeout": "unknown-unit"}},
		{"zero timeout", map[string]any{"delete-timeout": "0s"}},
		{"negative timeout", map[string]any{"delete-timeout": "-30s"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newPodTestServer(t)
			store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
			require.NoError(t, err)
			exec := podExecutor(t, server, store, "steady", podSteadyInputs())
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"POST pods"}, server.takeMutations())

			snapshot, err := store.Current()
			require.NoError(t, err)
			entry := snapshot.Find("resource.subject")
			for key, value := range test.inputs {
				entry.Inputs[key] = value
			}
			revision, err := store.Write(snapshot)
			require.NoError(t, err)
			require.NoError(t, store.SetCurrent(revision))

			if test.inputs["ignore-labels"] != nil {
				server.mu.Lock()
				live := server.pods["subject"]
				live.Labels = map[string]string{"ignored": "label", "external": "visible"}
				live.Annotations = map[string]string{"ignored": "note", "external": "visible"}
				server.mu.Unlock()
				observed, err := exec.Plan(t.Context())
				require.NoError(t, err)
				metadata := podPlanStep(t, observed).ObservedOutputs["metadata"].(map[string]any)
				for _, field := range []string{"labels", "annotations"} {
					require.Equal(t, map[string]any{"external": "visible"}, metadata[field])
				}
			}

			exec.Destroy = true
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			step := podPlanStep(t, plan)
			require.Equal(t, runtime.DecisionDestroy, step.Decision)
			require.Equal(t, new(*server.config.Server+"/uid-subject"), step.ExpectedStableID)
			require.Empty(t, server.takeMutations())
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"DELETE subject"}, server.takeMutations())
			snapshot, err = store.Current()
			require.NoError(t, err)
			require.Nil(t, snapshot.Find("resource.subject"))
		})
	}
}

func TestPodKnownAndDeferredNativeInputs(t *testing.T) {
	for _, test := range []struct {
		fixture, value, pendingField string
		extra                        map[string]any
		planError, applyError        string
	}{
		{fixture: "deferred", value: "+0"},
		{fixture: "deferred", value: "9223372036854775808", applyError: "constraints["},
		{fixture: "deferred-required", value: "good", extra: podDeferredItems()},
		{fixture: "deferred-required", value: "bad", extra: podDeferredItems(),
			applyError: `field "spec.container[0].name": required but not provided`},
		{fixture: "deferred-required", value: "unknown", extra: podDeferredItems(),
			applyError: `unknown field "spec.container[0].restart-polciy"`},
		{fixture: "deferred-quantity", value: "malformed", applyError: "quantity \"cpu\""},
		{fixture: "known-object", value: "image2", extra: map[string]any{"deadline": int64(0)},
			planError: "constraints["},
		{fixture: "known-map", value: "label", planError: "constraints["},
		{fixture: "known-map-comprehension", value: "label", pendingField: "metadata",
			extra: map[string]any{"keys": []any{"valid"}}},
		{fixture: "known-map-comprehension", value: "bad value", pendingField: "metadata",
			extra: map[string]any{"keys": []any{"valid"}}, applyError: "constraints["},
		{fixture: "unknown-map-key", value: "bad key", pendingField: "metadata",
			applyError: "constraints["},
		{fixture: "known-comprehension", value: "audience",
			extra:     map[string]any{"members": []any{"one"}, "expiration": int64(599)},
			planError: "constraints["},
		{fixture: "known-comprehension", value: "audience",
			extra:     map[string]any{"members": []any{}, "expiration": int64(600)},
			planError: "constraints["},
		{fixture: "unknown-filter", value: "other", applyError: "constraints["},
		{fixture: "unknown-filter", value: "tmp"},
		{fixture: "known-comprehension", value: "valid",
			extra: map[string]any{"members": []any{"one"}, "expiration": int64(600)}},
	} {
		t.Run(test.fixture+"/"+test.value+"/"+test.planError, func(t *testing.T) {
			server := newPodTestServer(t)
			store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
			require.NoError(t, err)
			initial := podExecutor(t, server, store, "steady", podSteadyInputs())
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"POST pods"}, server.takeMutations())

			server.mu.Lock()
			server.seedValue = test.value
			server.mu.Unlock()
			inputs := map[string]any{}
			for key, value := range test.extra {
				inputs[key] = value
			}
			exec := podExecutor(t, server, store, test.fixture, inputs)
			plan, err = exec.Plan(t.Context())
			require.Empty(t, server.takeMutations())
			if test.planError != "" {
				require.ErrorContains(t, err, "resource.subject: schema:")
				require.ErrorContains(t, err, test.planError)
				return
			}
			require.NoError(t, err)
			step := podPlanStep(t, plan)
			pending := test.pendingField
			if pending == "" {
				pending = "spec"
			}
			require.Contains(t, step.UnresolvedInputs, pending)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			if test.applyError != "" {
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, test.applyError)
				require.Equal(t, []string{"POST pods"}, server.takeMutations())
				snapshot, err := store.Current()
				require.NoError(t, err)
				require.Equal(t, "uid-subject", snapshot.Find("resource.subject").Outputs["uid"])
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"POST pods", "DELETE subject", "POST pods"},
				server.takeMutations())
		})
	}
}

func podDeferredItems() map[string]any {
	return map[string]any{"items": map[string]any{
		"good": map[string]any{"name": "app", "image": "image2"},
		"bad":  map[string]any{"image": "image2"},
		"unknown": map[string]any{
			"name": "app", "image": "image2", "restart-polciy": "Always",
		},
	}}
}

func podPlanInput(t *testing.T, value any) map[string]any {
	t.Helper()
	body, err := ub.Marshal(value)
	require.NoError(t, err)
	var inputs map[string]any
	require.NoError(t, ub.Unmarshal(body, &inputs))
	return inputs
}

func TestPodAdoptsLiveComputedNode(t *testing.T) {
	server := newPodTestServer(t)
	store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
	require.NoError(t, err)
	initial := podExecutor(t, server, store, "steady", podSteadyInputs())
	plan, err := initial.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionCreate, podPlanStep(t, plan).Decision)
	_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST pods"}, server.takeMutations())
	prior, err := store.Current()
	require.NoError(t, err)
	entry := prior.Find("resource.subject")
	require.Nil(t, entry.Inputs["spec"].(map[string]any)["node-name"])
	require.Nil(t, entry.Outputs["spec"].(map[string]any)["node-name"])
	uid := entry.Outputs["uid"]
	require.Equal(t, "uid-subject", uid)

	server.mu.Lock()
	live := server.pods["subject"]
	phase := live.Status.Phase
	live.Spec.NodeName = "worker"
	server.mu.Unlock()
	require.Equal(t, corev1.PodPending, phase)
	plan, err = initial.Plan(t.Context())
	require.NoError(t, err)
	step := podPlanStep(t, plan)
	require.Equal(t, runtime.DecisionUpdate, step.Decision)
	require.Empty(t, step.ReplacementReasons)

	inputs := podSteadyInputs()
	spec := inputs["spec"].(map[string]any)
	spec["node-name"] = "other-worker"
	desired := podExecutor(t, server, store, "steady", inputs)
	plan, err = desired.Plan(t.Context())
	require.NoError(t, err)
	step = podPlanStep(t, plan)
	require.Equal(t, runtime.DecisionReplace, step.Decision)
	require.Equal(t, []string{"spec"}, step.ReplacementReasons)

	spec["node-name"] = "worker"
	plan, err = desired.Plan(t.Context())
	require.NoError(t, err)
	step = podPlanStep(t, plan)
	require.Equal(t, runtime.DecisionUpdate, step.Decision)
	require.Empty(t, step.ReplacementReasons)
	require.Equal(t, uid, step.ObservedOutputs["uid"])
	require.Equal(t, "worker", step.ObservedOutputs["spec"].(map[string]any)["node-name"])
	_, err = desired.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Empty(t, server.takeMutations())
	current, err := store.Current()
	require.NoError(t, err)
	entry = current.Find("resource.subject")
	require.Equal(t, uid, entry.Outputs["uid"])
	require.Equal(t, "worker", entry.Inputs["spec"].(map[string]any)["node-name"])
	require.Equal(t, "worker", entry.Outputs["spec"].(map[string]any)["node-name"])

	server.mu.Lock()
	server.pods["subject"].Spec.NodeName = "externally-changed"
	server.mu.Unlock()
	plan, err = desired.Plan(t.Context())
	require.NoError(t, err)
	step = podPlanStep(t, plan)
	require.Equal(t, runtime.DecisionReplace, step.Decision)
	require.Equal(t, []string{"spec"}, step.ReplacementReasons)
	require.Empty(t, server.takeMutations())
	_, err = desired.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"DELETE subject", "POST pods"}, server.takeMutations())
	current, err = store.Current()
	require.NoError(t, err)
	entry = current.Find("resource.subject")
	require.Equal(t, "uid-subject-2", entry.Outputs["uid"])
	require.Equal(t, "worker", entry.Outputs["spec"].(map[string]any)["node-name"])
}

func TestPodPlanReplacementAndDrift(t *testing.T) {
	server := newPodTestServer(t)
	store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
	require.NoError(t, err)
	makeInputs := func(spec PodSpec) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"name": "subject"}, "spec": podPlanInput(t, spec),
		}
	}
	spec := PodSpec{
		Container: &[]PodContainer{
			{Name: "app", Image: new("old"),
				Resources:       &PodResources{Requests: &map[string]string{"cpu": "1000m"}},
				LivenessProbe:   &PodProbe{Exec: &PodExec{Command: &[]string{"test"}}},
				SecurityContext: &PodContainerSecurityContext{}},
			{Name: "peer", Image: new("peer")},
		},
		InitContainer:   &[]PodContainer{{Name: "init", Image: new("init-old")}},
		SecurityContext: &PodSecurityContext{WindowsOptions: &PodWindowsOptions{}},
		DNSConfig: &PodDNSConfig{
			Nameservers: &[]string{"127.0.0.1"},
			Searches:    &[]string{"example.com"},
			Option:      &[]PodDNSOption{{Name: "ndots", Value: new("1")}},
		},
		TopologySpreadConstraint: &[]PodTopologySpreadConstraint{{
			TopologyKey: new("zone"), MaxSkew: new(int64(2)), NodeAffinityPolicy: new("Honor"),
		}},
		Volume: &[]PodVolume{{Name: new("scratch"), EmptyDir: &PodEmptyDirVolume{
			SizeLimit: new("1Gi"),
		}}},
	}
	initial := podExecutor(t, server, store, "steady", makeInputs(spec))
	plan, err := initial.Plan(t.Context())
	require.NoError(t, err)
	_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST pods"}, server.takeMutations())

	for _, test := range []struct {
		name   string
		change func(*PodSpec)
		want   runtime.Decision
	}{
		{"deadline", func(s *PodSpec) { s.ActiveDeadlineSeconds = new(int64(30)) },
			runtime.DecisionUpdate},
		{"regular image", func(s *PodSpec) { (*s.Container)[0].Image = new("new") },
			runtime.DecisionReplace},
		{"init image", func(s *PodSpec) { (*s.InitContainer)[0].Image = new("init-new") },
			runtime.DecisionReplace},
		{"topology", func(s *PodSpec) { *s.Container = nil }, runtime.DecisionReplace},
		{"reorder", func(s *PodSpec) {
			containers := *s.Container
			containers[0], containers[1] = containers[1], containers[0]
		}, runtime.DecisionReplace},
		{"add probe", func(s *PodSpec) {
			(*s.Container)[0].ReadinessProbe = &PodProbe{Exec: &PodExec{}}
		}, runtime.DecisionReplace},
		{"probe command", func(s *PodSpec) {
			(*s.Container)[0].LivenessProbe.Exec.Command = &[]string{"changed"}
		}, runtime.DecisionUpdate},
		{"security context count", func(s *PodSpec) {
			(*s.Container)[0].SecurityContext = nil
		}, runtime.DecisionReplace},
		{"empty capabilities", func(s *PodSpec) {
			(*s.Container)[0].SecurityContext.Capabilities = &PodCapabilities{}
		}, runtime.DecisionUpdate},
		{"capability contents", func(s *PodSpec) {
			(*s.Container)[0].SecurityContext.Capabilities = &PodCapabilities{
				Add: &[]string{"NET_BIND_SERVICE"},
			}
		}, runtime.DecisionReplace},
		{"privileged", func(s *PodSpec) {
			(*s.Container)[0].SecurityContext.Privileged = new(true)
		}, runtime.DecisionReplace},
		{"name", func(s *PodSpec) { (*s.Container)[0].Name = "other" }, runtime.DecisionReplace},
		{"env", func(s *PodSpec) { (*s.Container)[0].Env = &[]PodEnv{{Name: "a", Value: new("b")}} },
			runtime.DecisionReplace},
		{"quantity", func(s *PodSpec) { (*(*s.Container)[0].Resources.Requests)["cpu"] = "1" },
			runtime.DecisionNoOp},
		{"dns nameserver", func(s *PodSpec) { (*s.DNSConfig.Nameservers)[0] = "192.0.2.1" },
			runtime.DecisionReplace},
		{"dns search", func(s *PodSpec) { (*s.DNSConfig.Searches)[0] = "other.example" },
			runtime.DecisionUpdate},
		{"dns option name", func(s *PodSpec) { (*s.DNSConfig.Option)[0].Name = "timeout" },
			runtime.DecisionReplace},
		{"dns option", func(s *PodSpec) { (*s.DNSConfig.Option)[0].Value = new("2") },
			runtime.DecisionReplace},
		{"os", func(s *PodSpec) { s.OS = &PodOS{Name: "windows"} }, runtime.DecisionUpdate},
		{"windows", func(s *PodSpec) { s.SecurityContext.WindowsOptions.HostProcess = new(true) },
			runtime.DecisionUpdate},
		{"max skew", func(s *PodSpec) { (*s.TopologySpreadConstraint)[0].MaxSkew = new(int64(3)) },
			runtime.DecisionUpdate},
		{"node policy", func(s *PodSpec) {
			(*s.TopologySpreadConstraint)[0].NodeAffinityPolicy = new("Ignore")
		}, runtime.DecisionReplace},
		{"empty dir", func(s *PodSpec) { (*s.Volume)[0].EmptyDir.SizeLimit = new("2Gi") },
			runtime.DecisionReplace},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed PodSpec
			require.NoError(t, runtime.Decode(&changed, podPlanInput(t, spec)))
			test.change(&changed)
			exec := podExecutor(t, server, store, "steady", makeInputs(changed))
			p, err := exec.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, test.want, podPlanStep(t, p).Decision)
		})
	}

	server.mu.Lock()
	server.pods["subject"].Status.Phase = corev1.PodRunning
	server.mu.Unlock()
	plan, err = initial.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, podPlanStep(t, plan).Decision)
	for _, metadata := range []map[string]any{
		{"generate-name": "subject-"},
		{"name": "different"},
		{"name": "subject", "namespace": "different"},
	} {
		inputs := makeInputs(spec)
		inputs["metadata"] = metadata
		desired := podExecutor(t, server, store, "steady", inputs)
		plan, err = desired.Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, runtime.DecisionReplace, podPlanStep(t, plan).Decision)
	}
	moved := podExecutor(t, server, store, "steady", makeInputs(spec))
	moved.Inputs["server"] = "https://unreachable.invalid"
	plan, err = moved.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, podPlanStep(t, plan).Decision)

	server.mu.Lock()
	server.pods["subject"].Spec.NodeName = "chosen"
	server.mu.Unlock()
	plan, err = initial.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, podPlanStep(t, plan).Decision)
	_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Empty(t, server.takeMutations())
	plan, err = initial.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, podPlanStep(t, plan).Decision)

	server.mu.Lock()
	server.pods["subject"].Spec.Containers[0].Image = "external"
	server.mu.Unlock()
	plan, err = initial.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, podPlanStep(t, plan).Decision)
	require.Empty(t, server.takeMutations())
	server.mu.Lock()
	delete(server.pods, "subject")
	server.mu.Unlock()
	plan, err = initial.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionCreate, podPlanStep(t, plan).Decision)
}

func TestPodContainerReplacementPaths(t *testing.T) {
	baseline := fullPodInput(t).Spec
	encoded := podPlanInput(t, baseline)
	for _, test := range []struct {
		name    string
		change  func(*PodContainer)
		replace bool
	}{
		{"command", func(c *PodContainer) { c.Command = &[]string{"other"} }, true},
		{"args", func(c *PodContainer) { c.Args = &[]string{"other"} }, true},
		{"pull policy", func(c *PodContainer) { c.ImagePullPolicy = new("Always") }, true},
		{"working directory", func(c *PodContainer) { c.WorkingDir = new("/tmp") }, true},
		{"stdin", func(c *PodContainer) { c.Stdin = new(false) }, true},
		{"stdin once", func(c *PodContainer) { c.StdinOnce = new(false) }, true},
		{"tty", func(c *PodContainer) { c.TTY = new(false) }, true},
		{"restart omission", func(c *PodContainer) { c.RestartPolicy = nil }, false},
		{"message path", func(c *PodContainer) { c.TerminationMessagePath = new("/log") }, true},
		{"message policy", func(c *PodContainer) {
			c.TerminationMessagePolicy = new("FallbackToLogsOnError")
		}, true},
		{"env value", func(c *PodContainer) { (*c.Env)[0].Value = new("other") }, true},
		{"config key", func(c *PodContainer) {
			(*c.Env)[0].ValueFrom.ConfigMapKeyRef.Key = new("other")
		}, true},
		{"secret name", func(c *PodContainer) {
			(*c.Env)[0].ValueFrom.SecretKeyRef.Name = new("other")
		}, true},
		{"field path", func(c *PodContainer) {
			(*c.Env)[0].ValueFrom.FieldRef.FieldPath = new("metadata.namespace")
		}, true},
		{"env selector resource", func(c *PodContainer) {
			(*c.Env)[0].ValueFrom.ResourceFieldRef.Resource = "limits.cpu"
		}, true},
		{"env selector divisor", func(c *PodContainer) {
			(*c.Env)[0].ValueFrom.ResourceFieldRef.Divisor = new("1")
		}, false},
		{"env-from prefix", func(c *PodContainer) { (*c.EnvFrom)[0].Prefix = new("APP_") }, true},
		{"env-from config", func(c *PodContainer) {
			(*c.EnvFrom)[0].ConfigMapRef.Optional = new(false)
		}, true},
		{"env-from secret", func(c *PodContainer) {
			(*c.EnvFrom)[0].SecretRef.Name = "other"
		}, true},
		{"port", func(c *PodContainer) { (*c.Port)[0].ContainerPort = 80 }, true},
		{"host ip", func(c *PodContainer) { (*c.Port)[0].HostIP = new("192.0.2.1") }, true},
		{"post-start count", func(c *PodContainer) { c.Lifecycle.PostStart = nil }, true},
		{"pre-stop count", func(c *PodContainer) { c.Lifecycle.PreStop = nil }, true},
		{"hook leaf", func(c *PodContainer) {
			c.Lifecycle.PreStop.Exec.Command = &[]string{"other"}
		}, false},
		{"liveness count", func(c *PodContainer) { c.LivenessProbe = nil }, true},
		{"readiness count", func(c *PodContainer) { c.ReadinessProbe = nil }, true},
		{"startup count", func(c *PodContainer) { c.StartupProbe = nil }, true},
		{"probe leaf", func(c *PodContainer) {
			c.StartupProbe.FailureThreshold = new(int64(4))
		}, false},
		{"computed resources omitted", func(c *PodContainer) { c.Resources = nil }, false},
		{"limits omitted", func(c *PodContainer) { c.Resources.Limits = nil }, false},
		{"resource limits", func(c *PodContainer) {
			c.Resources.Limits = &map[string]string{"cpu": "1"}
		}, true},
		{"resource requests", func(c *PodContainer) {
			c.Resources.Requests = &map[string]string{"cpu": "1"}
		}, true},
		{"security context count", func(c *PodContainer) { c.SecurityContext = nil }, true},
		{"security privilege", func(c *PodContainer) {
			c.SecurityContext.AllowPrivilegeEscalation = new(false)
		}, true},
		{"security capabilities", func(c *PodContainer) {
			c.SecurityContext.Capabilities.Drop = &[]string{"ALL"}
		}, true},
		{"selinux", func(c *PodContainer) { c.SecurityContext.SELinuxOptions.Role = new("other") },
			true},
		{"seccomp", func(c *PodContainer) {
			c.SecurityContext.SeccompProfile.LocalhostProfile = new("other-profile")
		}, true},
		{"mount count", func(c *PodContainer) { c.VolumeMount = nil }, true},
		{"mount leaf", func(c *PodContainer) { (*c.VolumeMount)[0].MountPath = "/other" }, false},
		{"device count", func(c *PodContainer) { c.VolumeDevice = nil }, true},
		{"device leaf", func(c *PodContainer) { (*c.VolumeDevice)[0].DevicePath = "/dev/other" },
			false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, initContainer := range []bool{false, true} {
				var after PodSpec
				require.NoError(t, runtime.Decode(&after, encoded))
				containers := after.Container
				if initContainer {
					containers = after.InitContainer
				}
				test.change(&(*containers)[0])
				require.Equal(t, test.replace, podSpecChanged(baseline, after, true),
					"init container: %t", initContainer)
			}
		})
	}
}

func TestPodUnpatchableSpecEditsRefreshLiveState(t *testing.T) {
	server := newPodTestServer(t)
	store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
	require.NoError(t, err)
	makeExecutor := func(spec PodSpec) *runtime.Executor {
		inputs := podSteadyInputs()
		inputs["spec"] = podPlanInput(t, spec)
		return podExecutor(t, server, store, "steady", inputs)
	}
	baseline := makeExecutor(fullPodInput(t).Spec)
	plan, err := baseline.Plan(t.Context())
	require.NoError(t, err)
	_, err = baseline.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST pods"}, server.takeMutations())
	prior, err := store.Current()
	require.NoError(t, err)

	changed := fullPodInput(t).Spec
	changed.DNSConfig.Searches = &[]string{"new.example"}
	changed.OS.Name = "windows"
	for _, containers := range []*[]PodContainer{changed.Container, changed.InitContainer} {
		container := &(*containers)[0]
		(*container.Env)[0].ValueFrom.ResourceFieldRef.Divisor = new("2")
		(*container.VolumeMount)[0].MountPath = "/different"
		(*container.VolumeDevice)[0].DevicePath = "/dev/different"
		container.LivenessProbe.HTTPGet.Path = new("/other")
		container.Lifecycle.PreStop.Exec.Command = &[]string{"other"}
	}
	desired := makeExecutor(changed)
	plan, err = desired.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, podPlanStep(t, plan).Decision)
	_, err = desired.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Empty(t, server.takeMutations())
	after, err := store.Current()
	require.NoError(t, err)
	require.Equal(t, prior.Find("resource.subject").Outputs, after.Find("resource.subject").Outputs)
}

func TestPodKnownMapComprehensionRejectsBeforeAPI(t *testing.T) {
	calls := make(chan string, 4)
	cfg := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls <- req.Method + " " + req.URL.Path
		namespaceStatus(t, w, 500)
	})
	store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
	require.NoError(t, err)
	exec := podExecutor(t, &podTestServer{config: cfg}, store, "known-map-comprehension",
		map[string]any{"keys": []any{"bad key"}})
	_, err = exec.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.Empty(t, podHTTPCalls(calls))
}

func TestPodDeferredConfigNodeSelection(t *testing.T) {
	for _, test := range []struct {
		name, prior, live, desired string
		replaces                   bool
		knownReasons               []string
	}{
		{name: "adopt", live: "worker", desired: "worker"},
		{name: "different", live: "worker", desired: "other-worker", replaces: true},
		{
			name: "managed-drift", prior: "worker", live: "external", desired: "worker",
			replaces: true,
		},
		{name: "leave-unmanaged", live: "worker"},
		{
			name: "known-desired-change", prior: "worker", live: "worker", desired: "other-worker",
			replaces: true, knownReasons: []string{"spec"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newPodTestServer(t)
			store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
			require.NoError(t, err)
			initialInputs := podSteadyInputs()
			if test.prior != "" {
				initialInputs["spec"].(map[string]any)["node-name"] = test.prior
			}
			initial := podExecutor(t, server, store, "steady", initialInputs)
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"POST pods"}, server.takeMutations())
			snapshot, err := store.Current()
			require.NoError(t, err)
			entry := snapshot.Find("resource.subject")
			require.Equal(t, "uid-subject", entry.Outputs["uid"])
			savedNode := entry.Outputs["spec"].(map[string]any)["node-name"]
			if test.prior == "" {
				require.Nil(t, savedNode)
			} else {
				require.Equal(t, test.prior, savedNode)
			}

			server.mu.Lock()
			server.seedValue = *server.config.Server
			server.pods["subject"].Spec.NodeName = test.live
			server.mu.Unlock()

			inputs := podSteadyInputs()
			if test.desired != "" {
				inputs["spec"].(map[string]any)["node-name"] = test.desired
			}
			next := podExecutor(t, server, store, "deferred-config", inputs)
			plan, err = next.Plan(t.Context())
			require.NoError(t, err)
			step := podPlanStep(t, plan)
			require.Equal(t, "library-config.core", step.DeferredConfig)
			require.Empty(t, step.UnresolvedInputs)
			require.Nil(t, step.ObservedOutputs)
			require.Empty(t, server.takeMutations())

			_, err = next.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			mutations := server.takeMutations()
			require.NoError(t, err, "mutations during saved apply: %v", mutations)
			require.Equal(t, test.knownReasons, step.ReplacementReasons)
			require.Equal(t, []string{"current-context", "kubeconfig", "server", "spec"},
				step.PendingReplacementReasons)

			wantMutations := []string{"POST pods"}
			wantUID, wantNode := "uid-subject", test.live
			if test.replaces {
				wantMutations = append(wantMutations, "DELETE subject", "POST pods")
				wantUID, wantNode = "uid-subject-2", test.desired
			}
			require.Equal(t, wantMutations, mutations)
			snapshot, err = store.Current()
			require.NoError(t, err)
			entry = snapshot.Find("resource.subject")
			require.Equal(t, wantUID, entry.Outputs["uid"])
			require.Equal(t, wantNode, entry.Outputs["spec"].(map[string]any)["node-name"])
			appliedNode := entry.Inputs["spec"].(map[string]any)["node-name"]
			if test.desired == "" {
				require.Nil(t, appliedNode)
			} else {
				require.Equal(t, test.desired, appliedNode)
			}
		})
	}
}

func TestPodSavedPlanMetadataUpdates(t *testing.T) {
	hidden := map[string]string{"hidden": "keep"}
	exposed := map[string]string{"hidden": "keep", "exposed": "remove"}
	managed := map[string]any{"example.com/team": "app"}
	ignoreHidden := []any{"^hidden$"}
	ignoreBoth := []any{"^hidden$", "^exposed$"}

	type metadataChange struct {
		live, wantLive          map[string]string
		prior, desired          map[string]any
		wantOutput              map[string]any
		priorIgnore, nextIgnore []any
		operations              []configMapPatchOperation
	}
	removeFilter := metadataChange{
		live: exposed, desired: map[string]any{}, wantOutput: map[string]any{},
		priorIgnore: ignoreBoth, nextIgnore: ignoreHidden, wantLive: hidden,
		operations: []configMapPatchOperation{{Op: "remove", Path: "/exposed"}},
	}
	unchanged := metadataChange{
		live:       map[string]string{"external": "keep"},
		wantLive:   map[string]string{"external": "keep"},
		wantOutput: map[string]any{"external": "keep"},
	}
	for _, tc := range []struct {
		name        string
		change      metadataChange
		annotations *metadataChange
	}{
		{
			name: "first managed entries preserve filtered maps",
			change: metadataChange{
				live: hidden, desired: managed, wantOutput: managed,
				priorIgnore: ignoreHidden, nextIgnore: ignoreHidden,
				wantLive: map[string]string{"hidden": "keep", "example.com/team": "app"},
				operations: []configMapPatchOperation{
					{Op: "add", Path: "/example.com~1team", Value: "app"},
				},
			},
		},
		{
			name: "first managed entries initialize absent maps",
			change: metadataChange{
				desired: managed, wantOutput: managed,
				wantLive:   map[string]string{"example.com/team": "app"},
				operations: []configMapPatchOperation{{Op: "add", Value: managed}},
			},
		},
		{
			name: "remove filter and clear newly visible entries", change: removeFilter,
		},
		{
			name: "remove label filter without patching annotations", change: removeFilter,
			annotations: &unchanged,
		},
		{
			name: "add filter and retain now ignored entries",
			change: metadataChange{
				live: exposed, wantOutput: map[string]any{}, wantLive: exposed,
				priorIgnore: ignoreHidden, nextIgnore: ignoreBoth,
			},
		},
		{
			name: "repair managed drift and preserve unrelated entries",
			change: metadataChange{
				live:  map[string]string{"example.com/team": "drift", "external": "keep"},
				prior: managed, desired: managed,
				priorIgnore: []any{"^example[.]com/team$"}, nextIgnore: []any{"^example[.]com/team$"},
				wantLive:   map[string]string{"example.com/team": "app", "external": "keep"},
				wantOutput: map[string]any{"example.com/team": "app", "external": "keep"},
				operations: []configMapPatchOperation{
					{Op: "replace", Path: "/example.com~1team", Value: "app"},
				},
			},
			annotations: &unchanged,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels, annotations := tc.change, tc.change
			if tc.annotations != nil {
				annotations = *tc.annotations
			}
			inputs := func(prior bool) map[string]any {
				result := podSteadyInputs()
				metadata := result["metadata"].(map[string]any)
				for field, change := range map[string]metadataChange{
					"annotations": annotations, "labels": labels,
				} {
					values, ignore := change.desired, change.nextIgnore
					if prior {
						values, ignore = change.prior, change.priorIgnore
					}
					if values != nil {
						metadata[field] = values
					}
					result["ignore-"+field] = ignore
				}
				return result
			}

			server := newPodTestServer(t)
			store, err := local.NewStore(t.TempDir(), "pod", "test", encrypters.Noop{})
			require.NoError(t, err)
			initial := podExecutor(t, server, store, "metadata-update", inputs(true))
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionCreate, podPlanStep(t, plan).Decision)
			_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"POST pods"}, server.takeMutations())
			snapshot, err := store.Current()
			require.NoError(t, err)
			uid := snapshot.Find("resource.subject").Outputs["uid"]
			require.Equal(t, "uid-subject", uid)

			server.mu.Lock()
			pod := server.pods["subject"]
			pod.Annotations = maps.Clone(annotations.live)
			pod.Labels = maps.Clone(labels.live)
			server.mu.Unlock()

			next := podExecutor(t, server, store, "metadata-update", inputs(false))
			plan, err = next.Plan(t.Context())
			require.NoError(t, err)
			step := podPlanStep(t, plan)
			require.Equal(t, runtime.DecisionUpdate, step.Decision)
			require.Equal(t, uid, step.ObservedOutputs["uid"])
			require.Empty(t, server.takeMutations())
			_, err = next.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)

			wantPatch := []configMapPatchOperation{
				{Op: "test", Path: "/metadata/uid", Value: uid},
			}
			for _, op := range annotations.operations {
				op.Path = "/metadata/annotations" + op.Path
				wantPatch = append(wantPatch, op)
			}
			for _, op := range labels.operations {
				op.Path = "/metadata/labels" + op.Path
				wantPatch = append(wantPatch, op)
			}
			mutations := server.takeMutations()
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(wantPatch) == 1 {
				require.Empty(t, mutations)
				require.Empty(t, server.patches)
			} else {
				require.Equal(t, []string{"PATCH subject"}, mutations)
				require.Len(t, server.patches, 1)
				expected, err := json.Marshal(wantPatch)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(server.patches[0]))
			}
			pod = server.pods["subject"]
			require.Equal(t, uid, string(pod.UID))
			require.Equal(t, annotations.wantLive, pod.Annotations)
			require.Equal(t, labels.wantLive, pod.Labels)

			snapshot, err = store.Current()
			require.NoError(t, err)
			out := snapshot.Find("resource.subject").Outputs
			require.Equal(t, uid, out["uid"])
			metadata := out["metadata"].(map[string]any)
			require.Equal(t, annotations.wantOutput, metadata["annotations"])
			require.Equal(t, labels.wantOutput, metadata["labels"])
		})
	}
}
