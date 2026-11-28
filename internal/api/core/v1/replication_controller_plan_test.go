package v1

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
)

func TestReplicationControllerSavedPlans(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	store, err := local.NewStore(t.TempDir(), "replication-controller", "test", encrypters.Noop{})
	require.NoError(t, err)
	input := replicationControllerInput()
	inputs := podPlanInput(t, input)
	exec := replicationControllerExecutor(t, server, store, "steady", inputs)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionCreate, podPlanStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, podPlanStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Empty(t, server.takeMutations())
	input.Spec.Replicas = new(int64(2))
	for k, v := range podPlanInput(t, input) {
		inputs[k] = v
	}
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, podPlanStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH subject"}, server.takeMutations())
	(*input.Spec.Template.Spec.Container)[0].Image = new("image2")
	for k, v := range podPlanInput(t, input) {
		inputs[k] = v
	}
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, podPlanStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH subject", "DELETE subject", "POST replicationcontrollers"},
		server.takeMutations())
}

func TestReplicationControllerEmptySizeLimitSavedNoOp(t *testing.T) {
	for _, explicitFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicitFirst), func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			store, err := local.NewStore(t.TempDir(), "rc-empty-limit", "test", encrypters.Noop{})
			require.NoError(t, err)
			r := replicationControllerInput()
			limit := &PodEmptyDirVolume{}
			if explicitFirst {
				limit.SizeLimit = new("")
			}
			r.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"), EmptyDir: limit}})
			before, err := r.prepare()
			require.NoError(t, err)
			exec := replicationControllerExecutor(t, server, store, "steady", podPlanInput(t, r))
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
			if explicitFirst {
				limit.SizeLimit = nil
			} else {
				limit.SizeLimit = new("")
			}
			after, err := r.prepare()
			require.NoError(t, err)
			require.Equal(t, before, after)
			exec = replicationControllerExecutor(t, server, store, "steady", podPlanInput(t, r))
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionNoOp, podPlanStep(t, plan).Decision)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Empty(t, server.takeMutations())
			require.Equal(t, new(int32(1)), server.controllers["subject"].Spec.Replicas)
			require.Equal(t, "uid-subject", string(server.controllers["subject"].UID))
		})
	}
}

func TestReplicationControllerRuntimeClassSavedPlans(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		initial, desired, live *string
		adopt                  bool
	}{
		{name: "omitted"},
		{name: "empty", initial: new(""), desired: new("")},
		{name: "omitted to empty", desired: new("")},
		{name: "empty to omitted", initial: new("")},
		{name: "configured", initial: new("sandbox"), desired: new("sandbox"), live: new("sandbox")},
		{name: "computed omitted", live: new("admitted")},
		{name: "computed empty", initial: new(""), desired: new(""), live: new("admitted")},
		{name: "computed to empty", desired: new(""), live: new("admitted")},
		{name: "adopt matching", desired: new("admitted"), live: new("admitted"), adopt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			var sent *string
			capture := true
			server.get = func(_ http.ResponseWriter, _ *http.Request) bool {
				pod := &server.controllers["subject"].Spec.Template.Spec
				if capture {
					sent = pod.RuntimeClassName
					capture = false
					pod.RuntimeClassName = tc.live
				}
				return false
			}
			store, err := local.NewStore(t.TempDir(), "rc-runtime", "test", encrypters.Noop{})
			require.NoError(t, err)
			r := replicationControllerInput()
			r.Spec.Template.Spec.RuntimeClassName = tc.initial
			exec := replicationControllerExecutor(t, server, store, "steady", podPlanInput(t, r))
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			wantCreate := tc.initial
			if wantCreate != nil && *wantCreate == "" {
				wantCreate = nil
			}
			require.Equal(t, wantCreate, sent)
			require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
			r.Spec.Template.Spec.RuntimeClassName = tc.desired
			exec = replicationControllerExecutor(t, server, store, "steady", podPlanInput(t, r))
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			want := runtime.DecisionNoOp
			if tc.adopt {
				want = runtime.DecisionUpdate
			}
			require.Equal(t, want, podPlanStep(t, plan).Decision)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Empty(t, server.takeMutations())
			r.Spec.Replicas = new(int64(2))
			expectedSpec := server.controllers["subject"].Spec.DeepCopy()
			expectedSpec.Replicas = new(int32(2))
			exec = replicationControllerExecutor(t, server, store, "steady", podPlanInput(t, r))
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionUpdate, podPlanStep(t, plan).Decision)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"PATCH subject"}, server.takeMutations())
			expectedPatch, err := json.Marshal([]configMapPatchOperation{
				{Op: "test", Path: "/metadata/uid", Value: "uid-subject"},
				{Op: "replace", Path: "/spec", Value: expectedSpec},
			})
			require.NoError(t, err)
			require.JSONEq(t, string(expectedPatch), string(server.patches[0]))
			live := server.controllers["subject"]
			require.Equal(t, tc.live, live.Spec.Template.Spec.RuntimeClassName)
			require.Equal(t, new(int32(2)), live.Spec.Replicas)
			require.Equal(t, "uid-subject", string(live.UID))
		})
	}
}

func TestReplicationControllerNativeBoundaries(t *testing.T) {
	body, err := os.ReadFile("testdata/ub/replication-controller/inputs.json")
	require.NoError(t, err)
	var cases []struct {
		Name  string
		Input map[string]any
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&cases))
	for i := range cases {
		cases[i].Input = replicationControllerNumbers(cases[i].Input).(map[string]any)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			store, err := local.NewStore(t.TempDir(), "rc-native", "test", encrypters.Noop{})
			require.NoError(t, err)
			original := podPlanInput(t, replicationControllerInput())
			exec := replicationControllerExecutor(t, server, store, "steady", original)
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
			valid := strings.HasPrefix(tc.Name, "valid-")
			// Evaluated input rules run before otherwise unchanged planning.
			desired := maps.Clone(tc.Input)
			exec = replicationControllerExecutor(t, server, store, "steady", desired)
			_, err = exec.Plan(t.Context())
			if valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
			}
			require.Empty(t, server.takeMutations())
			// A serialized NoOp step must check its resolved desired inputs again.
			exec = replicationControllerExecutor(t, server, store, "steady", original)
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionNoOp, podPlanStep(t, plan).Decision)
			file := podSavedPlan(t, plan)
			if !valid {
				for k, v := range tc.Input {
					file.Inputs[k] = v
				}
				for _, step := range file.Steps {
					if step.Address == "resource.subject" {
						for k, v := range tc.Input {
							step.Inputs[k] = v
						}
					}
				}
				invalid := replicationControllerExecutor(t, server, store, "steady", maps.Clone(tc.Input))
				_, err = invalid.ApplyPlan(t.Context(), file)
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
				require.Empty(t, server.takeMutations())
			}
			// Pending complete values remain valid until the seed resolves.
			choices := maps.Clone(tc.Input)
			for _, field := range []string{"create-timeout", "update-timeout", "delete-timeout"} {
				if _, ok := choices[field]; !ok {
					choices[field] = "10m"
				}
			}
			for _, field := range []string{"ignore-labels", "ignore-annotations"} {
				if _, ok := choices[field]; !ok {
					choices[field] = []any{}
				}
			}
			server.seedValue = "selected"
			deferred := replicationControllerExecutor(t, server, store, "deferred", map[string]any{
				"choices": map[string]any{"selected": choices},
			})
			plan, err = deferred.Plan(t.Context())
			require.NoError(t, err)
			require.Contains(t, podPlanStep(t, plan).UnresolvedInputs, "spec")
			_, err = deferred.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			if valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
				require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
				snapshot, err := store.Current()
				require.NoError(t, err)
				require.Equal(t, "uid-subject", snapshot.Find("resource.subject").Outputs["uid"])
			}
		})
	}
}

func replicationControllerNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			panic(err)
		}
		return n
	case map[string]any:
		for k, item := range v {
			v[k] = replicationControllerNumbers(item)
		}
	case []any:
		for k, item := range v {
			v[k] = replicationControllerNumbers(item)
		}
	}
	return value
}

func TestReplicationControllerReplacementMatrix(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(*ReplicationControllerResource)
		replace bool
	}{
		{"name", func(r *ReplicationControllerResource) {
			r.Metadata.Name = new("other")
		}, true},
		{"prefix", func(r *ReplicationControllerResource) {
			r.Metadata.Name = nil
			r.Metadata.GenerateName = new("other-")
		}, true},
		{"namespace", func(r *ReplicationControllerResource) {
			r.Metadata.Namespace = new("other")
		}, true},
		{"template identity", func(r *ReplicationControllerResource) {
			r.Spec.Template.Metadata.Name = new("named")
		}, true},
		{"template namespace", func(r *ReplicationControllerResource) {
			r.Spec.Template.Metadata.Namespace = new("other")
		}, true},
		{"labels", func(r *ReplicationControllerResource) {
			r.Metadata.Labels = new(map[string]string{"key": "v"})
		}, false},
		{"template labels", func(r *ReplicationControllerResource) {
			r.Spec.Template.Metadata.Labels = new(map[string]string{"key": "v"})
		}, false},
		{"replicas", func(r *ReplicationControllerResource) {
			r.Spec.Replicas = new(int64(2))
		}, false},
		{"minimum ready", func(r *ReplicationControllerResource) {
			r.Spec.MinReadySeconds = new(int64(2))
		}, false},
		{"selector", func(r *ReplicationControllerResource) {
			r.Spec.Selector = map[string]string{"key": "v"}
		}, false},
		{"deadline", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.ActiveDeadlineSeconds = new(int64(2))
		}, false},
		{"os", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.OS = &PodOS{Name: "linux"}
		}, false},
		{"image", func(r *ReplicationControllerResource) {
			(*r.Spec.Template.Spec.Container)[0].Image = new("other")
		}, true},
		{"init", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.InitContainer = new([]PodContainer{{Name: "init", Image: new("image")}})
		}, true},
		{"affinity", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.Affinity = &PodSchedulingAffinity{}
		}, true},
		{"dns config", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.DNSConfig = &PodDNSConfig{Searches: new([]string{"example.test"})}
		}, true},
		{"readiness gate", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.ReadinessGate = new([]PodReadinessGate{{ConditionType: "Ready"}})
		}, true},
		{"pull secret", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.ImagePullSecrets = new([]PodNamedReference{{Name: "auth"}})
		}, true},
		{"toleration", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.Toleration = new([]PodToleration{{Key: new("key")}})
		}, true},
		{"windows", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.SecurityContext = &PodSecurityContext{
				WindowsOptions: &PodWindowsOptions{RunAsUsername: new("user")}}
		}, false},
		{"security", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.SecurityContext = &PodSecurityContext{RunAsUser: new("1")}
		}, true},
		{"probe", func(r *ReplicationControllerResource) {
			(*r.Spec.Template.Spec.Container)[0].LivenessProbe = &PodProbe{Exec: &PodExec{
				Command: new([]string{"true"})}}
		}, true},
		{"resources", func(r *ReplicationControllerResource) {
			(*r.Spec.Template.Spec.Container)[0].Resources = &PodResources{
				Requests: new(map[string]string{"cpu": "1"}),
			}
		}, true},
		{"container security", func(r *ReplicationControllerResource) {
			(*r.Spec.Template.Spec.Container)[0].SecurityContext = &PodContainerSecurityContext{
				RunAsUser: new("1")}
		}, true},
		{"lifecycle", func(r *ReplicationControllerResource) {
			(*r.Spec.Template.Spec.Container)[0].Lifecycle = &PodLifecycle{PreStop: &PodHandler{
				Exec: &PodExec{Command: new([]string{"true"})}}}
		}, true},
		{"mount", func(r *ReplicationControllerResource) {
			(*r.Spec.Template.Spec.Container)[0].VolumeMount = new([]PodMount{{Name: "data",
				MountPath: "/data"}})
		}, true},
		{"port", func(r *ReplicationControllerResource) {
			(*r.Spec.Template.Spec.Container)[0].Port = new([]PodPort{{ContainerPort: 8080}})
		}, true},
		{"topology mutable", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.TopologySpreadConstraint = new([]PodTopologySpreadConstraint{{
				MaxSkew: new(int64(2))}})
		}, false},
		{"topology immutable", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.TopologySpreadConstraint = new([]PodTopologySpreadConstraint{{
				MinDomains: new(int64(2))}})
		}, true},
		{"volume mutable", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"),
				ConfigMap: &PodConfigMapVolume{Name: new("settings")}}})
		}, false},
		{"volume immutable", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"),
				EmptyDir: &PodEmptyDirVolume{Medium: new("Memory")}}})
		}, true},
		{"azure namespace", func(r *ReplicationControllerResource) {
			r.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"),
				AzureFile: &PodAzureFileVolume{SecretName: "auth", ShareName: "data",
					SecretNamespace: new("other")}}})
		}, true},
	}
	for _, field := range []string{"AutomountServiceAccountToken", "DNSPolicy", "EnableServiceLinks",
		"HostIPC", "HostNetwork", "HostPID", "Hostname", "NodeName", "NodeSelector", "RuntimeClassName",
		"PriorityClassName", "RestartPolicy", "SchedulerName", "ServiceAccountName",
		"ShareProcessNamespace",
		"Subdomain", "TerminationGracePeriodSeconds", "HostAliases"} {
		tests = append(tests, struct {
			name    string
			edit    func(*ReplicationControllerResource)
			replace bool
		}{
			name: field, replace: true, edit: func(r *ReplicationControllerResource) {
				value := reflect.ValueOf(&r.Spec.Template.Spec).Elem().FieldByName(field)
				switch field {
				case "AutomountServiceAccountToken", "EnableServiceLinks":
					value.Set(reflect.ValueOf(new(false)))
				case "DNSPolicy":
					value.Set(reflect.ValueOf(new("Default")))
				case "RestartPolicy":
					value.Set(reflect.ValueOf(new("Never")))
				case "HostAliases":
					value.Set(reflect.ValueOf(new([]PodHostAlias{{IP: "127.0.0.1", Hostnames: []string{"host"}}})))
				default:
					switch value.Type().Elem().Kind() {
					case reflect.Bool:
						value.Set(reflect.ValueOf(new(true)))
					case reflect.String:
						value.Set(reflect.ValueOf(new("other")))
					case reflect.Int64:
						value.Set(reflect.ValueOf(new(int64(3))))
					case reflect.Map:
						value.Set(reflect.ValueOf(new(map[string]string{"key": "v"})))
					}
				}
			},
		})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			store, err := local.NewStore(t.TempDir(), "rc-matrix", "test", encrypters.Noop{})
			require.NoError(t, err)
			r := replicationControllerInput()
			initial := replicationControllerExecutor(t, server, store, "steady", podPlanInput(t, r))
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			tc.edit(&r)
			exec := replicationControllerExecutor(t, server, store, "steady", podPlanInput(t, r))
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			want := runtime.DecisionUpdate
			if tc.replace {
				want = runtime.DecisionReplace
			}
			require.Equal(t, want, podPlanStep(t, plan).Decision)
		})
	}
}

func TestReplicationControllerDriftAndHistoricalDestroy(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	store, err := local.NewStore(t.TempDir(), "rc-drift", "test", encrypters.Noop{})
	require.NoError(t, err)
	input := podPlanInput(t, replicationControllerInput())
	exec := replicationControllerExecutor(t, server, store, "steady", input)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	live := server.controllers["subject"]
	live.Generation = 100
	live.ResourceVersion = "100"
	live.Status.ReadyReplicas = 99
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, podPlanStep(t, plan).Decision)
	live.Spec.Template.Spec.Containers[0].Image = "external"
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, podPlanStep(t, plan).Decision)
	live.Spec.Template.Spec.Containers[0].Image = "image"
	live.Spec.Replicas = new(int32(2))
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, podPlanStep(t, plan).Decision)
	live.Spec.Replicas = new(int32(1))
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.subject")
	entry.Inputs["metadata"] = map[string]any{}
	entry.Inputs["ignore-labels"] = []any{"["}
	entry.Inputs["delete-timeout"] = "invalid"
	revision, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(revision))
	input["metadata"] = map[string]any{}
	exec.Destroy = true
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionDestroy, podPlanStep(t, plan).Decision)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
}

func TestReplicationControllerDeferredQuantityPreflight(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	store, err := local.NewStore(t.TempDir(), "rc-preflight", "test", encrypters.Noop{})
	require.NoError(t, err)
	initial := replicationControllerExecutor(t, server, store, "steady",
		podPlanInput(t, replicationControllerInput()))
	plan, err := initial.Plan(t.Context())
	require.NoError(t, err)
	_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	server.takeMutations()
	r := replicationControllerInput()
	(*r.Spec.Template.Spec.Container)[0].Image = new("image2")
	(*r.Spec.Template.Spec.Container)[0].Resources = &PodResources{
		Requests: new(map[string]string{"cpu": "invalid quantity"})}
	choices := podPlanInput(t, r)
	for _, field := range []string{"create-timeout", "update-timeout", "delete-timeout"} {
		choices[field] = "10m"
	}
	for _, field := range []string{"ignore-labels", "ignore-annotations"} {
		choices[field] = []any{}
	}
	server.seedValue = "selected"
	exec := replicationControllerExecutor(t, server, store, "deferred", map[string]any{
		"choices": map[string]any{"selected": choices},
	})
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.ErrorContains(t, err, "quantity")
	require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
	snapshot, err := store.Current()
	require.NoError(t, err)
	require.Equal(t, "uid-subject", snapshot.Find("resource.subject").Outputs["uid"])
}

func TestReplicationControllerPartiallyKnownInputs(t *testing.T) {
	for _, tc := range []struct {
		fixture, value            string
		extra                     map[string]any
		planFailure, applyFailure bool
	}{
		{fixture: "known-object", value: "image2",
			extra: map[string]any{"deadline": int64(0)}, planFailure: true},
		{fixture: "known-map", value: "valid", planFailure: true},
		{fixture: "known-comprehension", value: "audience", extra: map[string]any{
			"members": []any{"one"}, "expiration": int64(599)}, planFailure: true},
		{fixture: "known-comprehension", value: "audience", extra: map[string]any{
			"members": []any{"one"}, "expiration": int64(600)}},
		{fixture: "unknown-map-key", value: "bad key", applyFailure: true},
		{fixture: "unknown-map-key", value: "valid"},
	} {
		t.Run(tc.fixture+"/"+tc.value, func(t *testing.T) {
			server := newReplicationControllerTestServer(t)
			server.seedValue = tc.value
			store, err := local.NewStore(t.TempDir(), "rc-partial", "test", encrypters.Noop{})
			require.NoError(t, err)
			initial := replicationControllerExecutor(t, server, store, "steady",
				podPlanInput(t, replicationControllerInput()))
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			require.NoError(t, err)
			server.takeMutations()
			inputs := map[string]any{}
			maps.Copy(inputs, tc.extra)
			exec := replicationControllerExecutor(t, server, store, tc.fixture, inputs)
			plan, err = exec.Plan(t.Context())
			if tc.planFailure {
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
				require.Empty(t, server.takeMutations())
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, podPlanStep(t, plan).UnresolvedInputs)
			_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
			if tc.applyFailure {
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
				require.Equal(t, []string{"POST replicationcontrollers"}, server.takeMutations())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestReplicationControllerSavedIgnorePatternUpdates(t *testing.T) {
	server := newReplicationControllerTestServer(t)
	store, err := local.NewStore(t.TempDir(), "rc-ignore", "test", encrypters.Noop{})
	require.NoError(t, err)
	r := replicationControllerInput()
	r.Metadata.Labels = new(map[string]string{"keep": "configured"})
	inputs := podPlanInput(t, r)
	exec := replicationControllerExecutor(t, server, store, "steady", inputs)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	server.takeMutations()
	server.controllers["subject"].Labels["ignored"] = "external"
	inputs["ignore-labels"] = []any{"^ignored"}
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	snapshot, err := store.Current()
	require.NoError(t, err)
	metadata := snapshot.Find("resource.subject").Outputs["metadata"].(map[string]any)
	require.Equal(t, map[string]any{"keep": "configured"}, metadata["labels"])
	require.Equal(t, map[string]string{"keep": "configured", "ignored": "external"},
		server.controllers["subject"].Labels)
	inputs["ignore-labels"] = []any{}
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), podSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"keep": "configured"}, server.controllers["subject"].Labels)
	require.Equal(t, []string{"PATCH subject"}, server.takeMutations())
}
