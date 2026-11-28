package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type replicationControllerTestServer struct {
	request     func(http.ResponseWriter, *http.Request) bool
	t           *testing.T
	mu          sync.Mutex
	controllers map[string]*corev1.ReplicationController
	creates     map[string]int
	mutations   []string
	patches     [][]byte
	calls       []string
	get         func(http.ResponseWriter, *http.Request) bool
	seedValue   string
	config      *config.Configuration
}

func newReplicationControllerTestServer(t *testing.T) *replicationControllerTestServer {
	t.Helper()
	fake := &replicationControllerTestServer{t: t,
		controllers: map[string]*corev1.ReplicationController{}, creates: map[string]int{}}
	fake.config = configMapAPI(t, fake.serve)
	return fake
}

func (s *replicationControllerTestServer) serve(w http.ResponseWriter, req *http.Request) {
	t := s.t
	if replicationControllerTestDiscovery(t, w, req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	require.True(t, strings.HasPrefix(req.URL.Path,
		"/api/v1/namespaces/default/replicationcontrollers"), req.URL.Path)
	name := path.Base(req.URL.Path)
	s.calls = append(s.calls, req.Method+" "+name)
	if req.Method == http.MethodGet && s.get != nil && s.get(w, req) {
		return
	}
	if req.Method != http.MethodGet {
		s.mutations = append(s.mutations, req.Method+" "+name)
	}
	if s.request != nil && s.request(w, req) {
		return
	}
	switch req.Method {
	case http.MethodPost:
		pod := &corev1.ReplicationController{}
		require.NoError(t, json.NewDecoder(req.Body).Decode(pod))
		s.creates[pod.Name]++
		uid := "uid-" + pod.Name
		if s.creates[pod.Name] > 1 {
			uid += "-" + strconv.Itoa(s.creates[pod.Name])
		}
		pod.UID = types.UID(uid)
		if pod.Name == "" {
			pod.Name = pod.GenerateName + "abcde"
		}
		pod.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
		pod.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyAlways
		pod.Spec.Template.Spec.SchedulerName = "default-scheduler"
		pod.Status.FullyLabeledReplicas = *pod.Spec.Replicas
		if pod.Name == "seed" {
			pod.Annotations = map[string]string{"value": s.seedValue}
		}
		s.controllers[pod.Name] = pod
		respond(t, w, pod)
	case http.MethodGet:
		if pod := s.controllers[name]; pod != nil {
			respond(t, w, pod)
		} else {
			namespaceStatus(t, w, 404)
		}
	case http.MethodPatch:
		pod := s.controllers[name]
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
		var updated corev1.ReplicationController
		require.NoError(t, json.Unmarshal(after, &updated))
		updated.Status.FullyLabeledReplicas = *updated.Spec.Replicas
		s.controllers[name] = &updated
		respond(t, w, &updated)
	case http.MethodDelete:
		pod := s.controllers[name]
		if pod == nil {
			namespaceStatus(t, w, 404)
			return
		}
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, metav1.DeleteOptions{
			TypeMeta:          metav1.TypeMeta{APIVersion: "v1", Kind: "DeleteOptions"},
			Preconditions:     &metav1.Preconditions{UID: &pod.UID},
			PropagationPolicy: new(metav1.DeletePropagationForeground),
		}, options)
		delete(s.controllers, name)
		respond(t, w, metav1.Status{Status: "Success"})
	default:
		t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		namespaceStatus(t, w, 400)
	}
}

func (s *replicationControllerTestServer) takeMutations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutations := s.mutations
	s.mutations = nil
	return mutations
}

func replicationControllerTestDiscovery(t *testing.T, w http.ResponseWriter,
	req *http.Request) bool {
	t.Helper()
	switch req.URL.Path {
	case "/api":
		respond(t, w, metav1.APIVersions{Versions: []string{"v1"}})
	case "/apis":
		respond(t, w, metav1.APIGroupList{})
	case "/api/v1":
		respond(t, w, metav1.APIResourceList{
			GroupVersion: "v1", APIResources: []metav1.APIResource{{
				Name: "replicationcontrollers", Kind: "ReplicationController", Namespaced: true,
				Verbs: []string{"get", "delete", "create", "patch"},
			}},
		})
	default:
		return false
	}
	return true
}

func replicationControllerExecutor(
	t *testing.T, server *replicationControllerTestServer, store state.Backend, fixture string,
	inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/ub/replication-controller", fixture,
		"factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	base := runtime.MakeResource[
		ReplicationControllerResource, *ReplicationControllerResourceOutput, *config.Configuration,
	]((&ReplicationControllerResource{}).ResourceDefinition())
	extracted, err := nativeCoreSchema()
	require.NoError(t, err)
	pod := *extracted.Resources["replication-controller"]
	pod.EvaluatedInputs = pod.Inputs
	library := &runtime.Library{
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"replication-controller": ReplicationControllerRegistration[ReplicationControllerResource,
				*ReplicationControllerResourceOutput](
				base, base.Read, base.KnownReplacementReasons),
		},
		Schema: &runtime.LibrarySchema{Resources: map[string]*runtime.TypeSchema{
			"replication-controller": &pod}},
		Defaults:    map[string][]lang.DefaultSpec{"resource.replication-controller": pod.Defaults},
		Constraints: map[string][]lang.ConstraintSpec{"resource.replication-controller": pod.Constraints},
	}
	libraries := make(map[string]*runtime.Library, len(parsed.Factory.Body.Imports))
	for _, imp := range parsed.Factory.Body.Imports {
		libraries[imp.Alias.Name] = library
	}
	for _, field := range []string{"create-timeout", "update-timeout", "delete-timeout"} {
		if inputs[field] == nil {
			inputs[field] = "10m"
		}
	}
	for _, field := range []string{"ignore-labels", "ignore-annotations"} {
		if inputs[field] == nil {
			inputs[field] = []any{}
		}
	}
	inputs["server"] = *server.config.Server
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libraries), SyntaxSource: &parsed.Factory.Body,
		Libraries: libraries, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "replication-controller", Version: "v0", ContentRevision: "v0"},
	}
}

func TestReplicationControllerLifecycle(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	r := replicationControllerInput()
	created, err := r.Create(t.Context(), server.config)
	require.NoError(t, err)
	require.Equal(t, "uid-subject", created.UID)
	require.Equal(t, new(int64(1)), created.Spec.Replicas)
	prior := replicationControllerPrior{Inputs: r, Outputs: created}
	r.Metadata.Labels = new(map[string]string{"example.com/key": "value"})
	r.Spec.Replicas = new(int64(2))
	updated, err := r.Update(t.Context(), server.config, prior)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, new(int64(2)), updated.Spec.Replicas)
	prior = replicationControllerPrior{Inputs: r, Outputs: updated}
	require.NoError(t, r.Delete(t.Context(), server.config, prior))
	require.Equal(t, []string{"POST replicationcontrollers", "PATCH subject", "PATCH subject",
		"DELETE subject"}, server.takeMutations())
	require.Equal(t, []string{
		"POST replicationcontrollers", "GET subject", "GET subject",
		"GET subject", "PATCH subject", "GET subject", "GET subject",
		"GET subject", "PATCH subject", "GET subject", "DELETE subject", "GET subject",
	}, server.calls)
}

func TestReplicationControllerMetadataOnlyAndComputedValues(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	r := replicationControllerInput()
	created, err := r.Create(t.Context(), server.config)
	require.NoError(t, err)
	prior := replicationControllerPrior{Inputs: r, Outputs: created}
	live := server.controllers["subject"]
	live.Spec.Template.Spec.TerminationGracePeriodSeconds = new(int64(30))
	live.Spec.Template.Spec.NodeName = "worker"
	r.Metadata.Annotations = new(map[string]string{"example.com/a~b": "value"})
	out, err := r.Update(t.Context(), server.config, prior)
	require.NoError(t, err)
	require.Equal(t, new(int64(30)), out.Spec.Template.Spec.TerminationGracePeriodSeconds)
	require.Equal(t, new("worker"), out.Spec.Template.Spec.NodeName)
	require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-subject"},
 {"op":"add","path":"/metadata/annotations","value":{"example.com/a~b":"value"}}]`,
		string(server.patches[0]))
	r.Spec.Replicas = new(int64(3))
	expectedSpec := server.controllers["subject"].Spec.DeepCopy()
	expectedSpec.Replicas = new(int32(3))
	prior = replicationControllerPrior{Inputs: prior.Inputs, Outputs: out}
	_, err = r.Update(t.Context(), server.config, prior)
	require.NoError(t, err)
	live = server.controllers["subject"]
	require.Equal(t, new(int64(30)), live.Spec.Template.Spec.TerminationGracePeriodSeconds)
	require.Equal(t, "worker", live.Spec.Template.Spec.NodeName)
	expectedPatch, err := json.Marshal([]configMapPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: "uid-subject"},
		{Op: "replace", Path: "/spec", Value: expectedSpec},
	})
	require.NoError(t, err)
	require.JSONEq(t, string(expectedPatch), string(server.patches[1]))
}

func TestReplicationControllerWaitUsesLiveCount(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	reads := 0
	server.get = func(_ http.ResponseWriter, _ *http.Request) bool {
		reads++
		live := server.controllers["subject"]
		live.Spec.Replicas = new(int32(2))
		if reads == 1 {
			live.Status.FullyLabeledReplicas = 1
		} else {
			live.Status.FullyLabeledReplicas = 2
		}
		return false
	}
	r := replicationControllerInput()
	out, err := r.Create(t.Context(), server.config)
	require.NoError(t, err)
	require.Equal(t, new(int64(2)), out.Spec.Replicas)
	require.Equal(t, 3, reads)
	require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
}

func TestReplicationControllerWaitErrors(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			server.get = func(w http.ResponseWriter, _ *http.Request) bool {
				namespaceStatus(t, w, status)
				return true
			}
			r := replicationControllerInput()
			_, err := r.Create(t.Context(), server.config)
			require.Error(t, err)
			require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
		})
	}
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint("cancelled=", cancelled), func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			r := replicationControllerInput()
			r.CreateTimeout = new("15ms")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server.get = func(_ http.ResponseWriter, _ *http.Request) bool {
				server.controllers["subject"].Status.FullyLabeledReplicas = 0
				if cancelled {
					cancel()
				}
				return false
			}
			_, err := r.Create(ctx, server.config)
			require.Error(t, err)
			require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
		})
	}
}

func TestReplicationControllerReadIdentityAndErrors(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	r := replicationControllerInput()
	out, err := r.Create(t.Context(), server.config)
	require.NoError(t, err)
	prior := replicationControllerPrior{Inputs: r, Outputs: out}
	moved := *server.config
	moved.Server = new("https://unreachable.invalid")
	moved.Token = new("current-credentials")
	server.request = func(_ http.ResponseWriter, req *http.Request) bool {
		require.Equal(t, "Bearer current-credentials", req.Header.Get("Authorization"))
		return false
	}
	_, err = r.Read(t.Context(), &moved, prior)
	require.NoError(t, err)
	server.request = nil
	server.controllers["subject"].UID = "new-uid"
	_, err = r.Read(t.Context(), server.config, prior)
	require.ErrorContains(t, err, "UID changed")
	require.ErrorContains(t, r.Delete(t.Context(), server.config, prior), "UID changed")
	require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
	delete(server.controllers, "subject")
	_, err = r.Read(t.Context(), server.config, prior)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	require.NoError(t, r.Delete(t.Context(), server.config, prior))
	for _, status := range []int{403, 500} {
		server.get = func(w http.ResponseWriter, _ *http.Request) bool {
			namespaceStatus(t, w, status)
			return true
		}
		_, err = r.Read(t.Context(), server.config, prior)
		require.Error(t, err)
		require.NotErrorIs(t, err, runtime.ErrNotFound)
	}
}

func TestReplicationControllerDeleteFailuresAndReuse(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		status       int
		success      bool
	}{
		{"drain patch missing", "PATCH", 404, false},
		{"drain get missing", "GET", 404, false},
		{"delete missing", "DELETE", 404, true},
		{"delete forbidden", "DELETE", 403, false},
		{"final get missing", "FINAL", 404, true},
		{"final get forbidden", "FINAL", 403, false},
		{"name reused", "REUSED", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			r := replicationControllerInput()
			out, err := r.Create(t.Context(), server.config)
			require.NoError(t, err)
			server.takeMutations()
			phase := "initial"
			server.request = func(w http.ResponseWriter, req *http.Request) bool {
				if req.Method == "PATCH" {
					phase = "drain"
				}
				if req.Method == "DELETE" {
					phase = "delete"
				}
				matches := req.Method == tc.method && (req.Method != "GET" || phase == "drain") ||
					tc.method == "FINAL" && req.Method == "GET" && phase == "delete"
				if matches {
					namespaceStatus(t, w, tc.status)
					return true
				}
				if tc.method == "REUSED" && phase == "delete" && req.Method == "GET" {
					live := &corev1.ReplicationController{
						TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ReplicationController"},
						ObjectMeta: metav1.ObjectMeta{UID: "other"}}
					respond(t, w, live)
					return true
				}
				return false
			}
			err = r.Delete(t.Context(), server.config, replicationControllerPrior{Inputs: r, Outputs: out})
			if tc.success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			mutations := server.takeMutations()
			if tc.method == "PATCH" || tc.method == "GET" {
				require.Equal(t, []string{"PATCH subject"}, mutations)
			} else {
				require.Equal(t, []string{"PATCH subject", "DELETE subject"}, mutations)
			}
		})
	}
}

func TestReplicationControllerMetadataPatchContents(t *testing.T) {
	r := replicationControllerInput()
	server := newReplicationControllerTestServer(t)
	r.Metadata.Labels = new(map[string]string{"example.com/key": "before", "remove": "gone"})
	r.Metadata.Annotations = new(map[string]string{"example.com/a~b": "before"})
	out, err := r.Create(t.Context(), server.config)
	require.NoError(t, err)
	prior := replicationControllerPrior{Inputs: r, Outputs: out}
	r.Metadata.Labels = new(map[string]string{"example.com/key": "after", "new": "added"})
	r.Metadata.Annotations = new(map[string]string{"example.com/a~b": "after"})
	_, err = r.Update(t.Context(), server.config, prior)
	require.NoError(t, err)
	require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-subject"},
 {"op":"replace","path":"/metadata/annotations/example.com~1a~0b","value":"after"},
 {"op":"replace","path":"/metadata/labels/example.com~1key","value":"after"},
 {"op":"add","path":"/metadata/labels/new","value":"added"},
 {"op":"remove","path":"/metadata/labels/remove"}]`, string(server.patches[0]))
	require.False(t, strings.Contains(string(server.patches[0]), `"path":"/spec"`))
}

func TestReplicationControllerDeletePhaseTimeouts(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	r := replicationControllerInput()
	r.DeleteTimeout = new("400ms")
	out, err := r.Create(t.Context(), server.config)
	require.NoError(t, err)
	final := server.controllers["subject"].DeepCopy()
	phase := ""
	reads := 0
	server.request = func(w http.ResponseWriter, req *http.Request) bool {
		switch req.Method {
		case "PATCH":
			phase = "drain"
			reads = 0
		case "DELETE":
			phase = "final"
			reads = 0
		case "GET":
			reads++
			if phase == "drain" {
				count := int32(0)
				if reads == 1 {
					count = 1
				}
				server.controllers["subject"].Status.FullyLabeledReplicas = count
			}
			if phase == "final" && reads == 1 {
				respond(t, w, final)
				return true
			}
		}
		return false
	}
	err = r.Delete(t.Context(), server.config, replicationControllerPrior{Inputs: r, Outputs: out})
	require.NoError(t, err)
}

func TestReplicationControllerPostConflictDoesNotAdopt(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	server.request = func(w http.ResponseWriter, req *http.Request) bool {
		require.Equal(t, "POST", req.Method)
		namespaceStatus(t, w, 409)
		return true
	}
	r := replicationControllerInput()
	_, err := r.Create(t.Context(), server.config)
	require.Error(t, err)
	require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
}
