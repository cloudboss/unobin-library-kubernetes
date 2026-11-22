package v1

import (
	"encoding/json"
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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestStatefulSetStateBackedPlanApply(t *testing.T) {
	var stored *appsv1.StatefulSet
	var credentials []string
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		switch req.Method {
		case "POST":
			stored = &appsv1.StatefulSet{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			defaultStatefulSet(stored)
		case "PATCH":
			var operations []patchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			stored = applyStatefulSetPatch(t, stored, operations)
		case "DELETE":
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		case "GET":
			if stored == nil {
				apiError(t, w, 404)
				return
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	data, err := os.ReadFile("testdata/ub/stateful-set/state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", data)
	require.NoError(t, err)
	lib := &runtime.Library{
		Name: "kubernetes-apps-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"stateful-set": statefulSetResourceRegistration(),
		},
	}
	addNativeStatefulSetConstraints(t, lib)
	libs := map[string]*runtime.Library{"apps": lib}
	store, err := local.NewStore(t.TempDir(), "stateful-set", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := func(r *StatefulSetResource) map[string]any {
		encoded, err := ub.Marshal(r)
		require.NoError(t, err)
		var input map[string]any
		require.NoError(t, ub.Unmarshal(encoded, &input))
		return map[string]any{"server": *c.Server, "token": "first", "metadata": input["metadata"],
			"spec": input["spec"]}
	}
	executor := func(in map[string]any) *runtime.Executor {
		return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: in, Store: store,
			Factory: state.FactoryInfo{Name: "stateful-set", Version: "v0", ContentRevision: "c0"}}
	}
	plan := func(r *StatefulSetResource, want runtime.Decision, change func(map[string]any)) (
		*runtime.Executor, *runtime.Plan) {
		t.Helper()
		in := inputs(r)
		if change != nil {
			change(in)
		}
		exec := executor(in)
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.workload" {
				require.Equal(t, want, step.Decision)
				found = true
			}
		}
		require.True(t, found)
		return exec, p
	}
	apply := func(exec *runtime.Executor, p *runtime.Plan) {
		t.Helper()
		encoded, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
	}
	input := statefulSet()
	exec, p := plan(input, runtime.DecisionCreate, nil)
	apply(exec, p)
	plan(input, runtime.DecisionNoOp, nil)
	stored.Generation = 99
	stored.ResourceVersion = "changed"
	stored.Status = appsv1.StatefulSetStatus{ObservedGeneration: 98}
	plan(input, runtime.DecisionNoOp, nil)
	input.Spec.Replicas = new("0")
	(*input.Spec.Template.Spec.Container)[0].Image = new("changed")
	exec, p = plan(input, runtime.DecisionUpdate, nil)
	apply(exec, p)
	require.Zero(t, *stored.Spec.Replicas)
	require.Equal(t, "default-scheduler", stored.Spec.Template.Spec.SchedulerName)
	plan(input, runtime.DecisionNoOp, nil)
	plan(input, runtime.DecisionNoOp, func(in map[string]any) { in["token"] = "rotated" })
	require.Contains(t, credentials, "Bearer rotated")
	plan(input, runtime.DecisionReplace, func(in map[string]any) {
		in["server"] =
			"http://unused.invalid"
	})
	input = fullStatefulSet(t)
	input.WaitForRollout = new(false)
	exec, p = plan(input, runtime.DecisionReplace, nil)
	apply(exec, p)
	plan(input, runtime.DecisionNoOp, nil)
	for _, tc := range []struct {
		name   string
		change func(*StatefulSetResource)
		want   runtime.Decision
	}{
		{"service", func(r *StatefulSetResource) { r.Spec.ServiceName = "other" },
			runtime.DecisionReplace},
		{"selector", func(r *StatefulSetResource) {
			r.Spec.Selector.MatchLabels = new(
				map[string]string{"other": "x"})
		}, runtime.DecisionReplace},
		{"policy removal", func(r *StatefulSetResource) { r.Spec.PodManagementPolicy = nil },
			runtime.DecisionNoOp},
		{"revision removal", func(r *StatefulSetResource) { r.Spec.RevisionHistoryLimit = nil },
			runtime.DecisionNoOp},
		{"template namespace removal", func(r *StatefulSetResource) {
			r.Spec.Template.Metadata.Namespace = nil
		}, runtime.DecisionReplace},
		{"claim removal", func(r *StatefulSetResource) { r.Spec.VolumeClaimTemplate = nil },
			runtime.DecisionReplace},
		{"claim resize", func(r *StatefulSetResource) {
			(*r.Spec.VolumeClaimTemplate)[0].Spec.Resources.Requests = new(map[string]string{
				"storage": "2Gi"})
		}, runtime.DecisionReplace},
		{"claim quantity equality", func(r *StatefulSetResource) {
			(*r.Spec.VolumeClaimTemplate)[0].Spec.Resources.Requests = new(map[string]string{
				"storage": "1073741824"})
		}, runtime.DecisionNoOp},
		{"CPU quantity equality", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{
				"example.com/value": "0.1"})
		}, runtime.DecisionNoOp},
		{"azure file namespace", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Volume)[0].AzureFile.SecretNamespace = new("other")
		}, runtime.DecisionReplace},
		{"ephemeral limits", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Limits =
				new(map[string]string{"storage": "2Gi"})
		}, runtime.DecisionReplace},
		{"ephemeral request", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Requests =
				new(map[string]string{"storage": "2Gi"})
		}, runtime.DecisionUpdate},
		{"explicit false", func(r *StatefulSetResource) {
			r.Spec.Template.Spec.HostNetwork =
				new(false)
		}, runtime.DecisionUpdate},
		{"list removal", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Command =
				new([]string{})
		}, runtime.DecisionUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := fullStatefulSet(t)
			tc.change(changed)
			plan(
				changed, tc.want, nil)
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*StatefulSetSpec)
	}{
		{"selector expressions", func(s *StatefulSetSpec) {
			(*s.Selector.MatchExpressions)[0].Key =
				new("other")
		}},
		{"selector expression order", func(s *StatefulSetSpec) {
			s.Selector.MatchExpressions = new([]SelectorRequirement{{Key: new("new")}, {Key: new(
				"example")}})
		}},
		{"template name", func(s *StatefulSetSpec) { s.Template.Metadata.Name = new("other") }},
		{"template generate name", func(s *StatefulSetSpec) {
			s.Template.Metadata.Name = nil
			s.Template.Metadata.GenerateName = new("other-")
		}},
		{"required node fields", func(s *StatefulSetSpec) {
			node := s.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			(*(*node.NodeSelectorTerm)[0].MatchFields)[0].Key = "other"
		}},
		{"preferred node fields", func(s *StatefulSetSpec) {
			terms := s.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			(*(*terms)[0].Preference.MatchFields)[0].Key = "other"
		}},
		{"ephemeral access", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.AccessModes =
				[]string{"ReadOnlyMany"}
		}},
		{"ephemeral selector", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Selector = nil
		}},
		{"ephemeral volume", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.VolumeName = new("other")
		}},
		{"ephemeral class", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.StorageClassName = new("other")
		}},
		{"ephemeral mode", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.VolumeMode = new("Filesystem")
		}},
		{"claim add", func(s *StatefulSetSpec) {
			*s.VolumeClaimTemplate = append(*s.VolumeClaimTemplate, statefulSetClaim("extra"))
		}},
		{"claim reorder", func(s *StatefulSetSpec) {
			a := *s.VolumeClaimTemplate
			a[0], a[1] = a[1],
				a[0]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := fullStatefulSet(t)
			tc.change(&changed.Spec)
			plan(changed, runtime.DecisionReplace, nil)
		})
	}

	stored.Spec.Template.Spec.Containers[0].Image = "drift"
	exec, p = plan(input, runtime.DecisionUpdate, nil)
	apply(exec, p)
	require.Equal(t, "example", stored.Spec.Template.Spec.Containers[0].Image)
	for _, tc := range []struct {
		name          string
		before, after func(*StatefulSetResource)
		want          runtime.Decision
	}{
		{"retained OnDelete strategy defaults", func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new("OnDelete")}
		}, func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{}
		}, runtime.DecisionUpdate},
		{"retained RollingUpdate strategy defaults", func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new("RollingUpdate")}
		}, func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{}
		}, runtime.DecisionNoOp},
		{"revision integer alias", func(r *StatefulSetResource) {
			r.Spec.RevisionHistoryLimit = new(int64(1))
		}, func(r *StatefulSetResource) {
			r.Spec.RevisionHistoryLimit = new(int64(4294967297))
		}, runtime.DecisionReplace},
		{"positive partition alias", func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{}}
		}, func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy.RollingUpdate.Partition = new(int64(4294967296))
		}, runtime.DecisionUpdate},
		{"negative partition alias", func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{}}
		}, func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy.RollingUpdate.Partition = new(int64(-4294967296))
		}, runtime.DecisionUpdate},
		{"set reorder", func(r *StatefulSetResource) {
			(*r.Spec.Selector.MatchExpressions)[0].Values =
				new([]string{"a", "b"})
		}, func(r *StatefulSetResource) {
			(*r.Spec.Selector.MatchExpressions)[0].Values = new([]string{"b", "a", "a"})
		}, runtime.DecisionNoOp},
		{"list reorder", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Command =
				new([]string{"a", "b"})
		}, func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Command = new([]string{"b", "a"})
		},
			runtime.DecisionUpdate},
		{"computed claim removal", func(r *StatefulSetResource) {
			(*r.Spec.VolumeClaimTemplate)[0].Spec.VolumeName = new("bound")
			(*r.Spec.VolumeClaimTemplate)[0].Spec.StorageClassName = new("remote")
			(*r.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = new("Filesystem")
		}, func(r *StatefulSetResource) {
			(*r.Spec.VolumeClaimTemplate)[0].Spec.VolumeName = nil
			(*r.Spec.VolumeClaimTemplate)[0].Spec.StorageClassName = nil
			(*r.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = nil
		}, runtime.DecisionNoOp},
		{"computed empty list", func(r *StatefulSetResource) {
			r.Spec.Template.Spec.ImagePullSecrets = new([]NamedReference{{Name: "secret"}})
		}, func(r *StatefulSetResource) {
			r.Spec.Template.Spec.ImagePullSecrets = new(
				[]NamedReference{})
		}, runtime.DecisionUpdate},
		{"computed empty map", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{"cpu": "100m"})
		}, func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{})
		}, runtime.DecisionUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored = nil
			before := fullStatefulSet(t)
			tc.before(before)
			exec, p := plan(before, runtime.DecisionCreate, nil)
			apply(exec, p)
			after := fullStatefulSet(t)
			tc.before(after)
			tc.after(after)
			plan(after, tc.want, nil)
		})
	}

	stored = nil
	input = statefulSet()
	exec, p = plan(input, runtime.DecisionCreate, nil)
	apply(exec, p)
	uid := stored.UID
	input.Metadata.Labels = new(map[string]string{
		"kubernetes.io/new": "internal", "example.com/ignored": "ignored"})
	input.Metadata.Annotations = new(map[string]string{
		"kubernetes.io/new": "internal", "example.com/ignored": "ignored"})
	exec, p = plan(input, runtime.DecisionUpdate, nil)
	apply(exec, p)
	plan(input, runtime.DecisionNoOp, nil)
	require.Equal(t, *input.Metadata.Labels, stored.Labels)
	require.Equal(t, *input.Metadata.Annotations, stored.Annotations)
	input.Metadata.Labels, input.Metadata.Annotations = nil, nil
	exec, p = plan(input, runtime.DecisionUpdate, nil)
	apply(exec, p)
	plan(input, runtime.DecisionNoOp, nil)
	require.Empty(t, stored.Labels)
	require.Empty(t, stored.Annotations)
	require.Equal(t, uid, stored.UID)

	for _, field := range []string{"initial policy", "policy", "revision", "claim", "ephemeral"} {
		t.Run("computed reintroduction "+field, func(t *testing.T) {
			stored = nil
			before := fullStatefulSet(t)
			if field == "claim" {
				(*before.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = new("Filesystem")
			}
			if field == "initial policy" {
				before.Spec.PodManagementPolicy = nil
			}
			exec, p := plan(before, runtime.DecisionCreate, nil)
			apply(exec, p)
			uid := stored.UID
			if field != "initial policy" {
				switch field {
				case "policy":
					before.Spec.PodManagementPolicy = nil
				case "revision":
					before.Spec.RevisionHistoryLimit = nil
				case "name":
					before.Metadata.Name = nil
				case "claim":
					(*before.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = nil
				case "ephemeral":
					(*before.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.VolumeName = nil
				}
				before.Metadata.Labels = new(map[string]string{"step": "omitted"})
				exec, p = plan(before, runtime.DecisionUpdate, nil)
				apply(exec, p)
			}
			after := fullStatefulSet(t)
			if field == "claim" {
				(*after.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = new("Filesystem")
			}
			if field == "initial policy" {
				after.Spec.PodManagementPolicy = new("OrderedReady")
			} else {
				after.Metadata.Labels = before.Metadata.Labels
			}
			exec, p = plan(after, runtime.DecisionNoOp, nil)
			apply(exec, p)
			require.Equal(t, uid, stored.UID)
			input = after
		})
	}

	stored.UID = "replacement"
	_, err = executor(inputs(input)).Plan(t.Context())
	require.ErrorContains(t, err, "UID changed")
	stored = nil
	plan(input, runtime.DecisionCreate, nil)
	t.Run("destroy historical inputs without identity", func(t *testing.T) {
		lib.Constraints = nil
		historical := statefulSet()
		historical.Metadata.Name = nil
		exec, p := plan(historical, runtime.DecisionCreate, nil)
		apply(exec, p)
		addNativeStatefulSetConstraints(t, lib)
		_, err := exec.Plan(t.Context())
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		exec.Destroy = true
		p, err = exec.Plan(t.Context())
		require.NoError(t, err)
		apply(exec, p)
		require.Nil(t, stored)
	})
}

func TestStatefulSetFailedCreateDoesNotAdopt(t *testing.T) {
	writes := 0
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" {
			writes++
			obj, err := statefulSet().expand(nil)
			require.NoError(t, err)
			defaultStatefulSet(obj)
			require.NoError(t, json.NewEncoder(w).Encode(obj))
			return
		}
		require.Equal(t, "GET", req.Method)
		apiError(t, w, 403)
	})
	data, err := os.ReadFile("testdata/ub/stateful-set/state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", data)
	require.NoError(t, err)
	lib := &runtime.Library{
		Name: "kubernetes-apps-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"stateful-set": statefulSetResourceRegistration()},
	}
	addNativeStatefulSetConstraints(t, lib)
	libs := map[string]*runtime.Library{"apps": lib}
	store, err := local.NewStore(t.TempDir(), "failure", "test", encrypters.Noop{})
	require.NoError(t, err)
	r := statefulSet()
	encoded, err := ub.Marshal(r)
	require.NoError(t, err)
	var in map[string]any
	require.NoError(t, ub.Unmarshal(encoded, &in))
	inputs := map[string]any{"server": *c.Server, "token": "first", "metadata": in["metadata"],
		"spec": in["spec"]}
	executor := func() *runtime.Executor {
		return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs, Store: store,
			Factory: state.FactoryInfo{Name: "failure", Version: "v0", ContentRevision: "c0"}}
	}
	exec := executor()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	planData, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(planData)
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), file)
	require.Error(t, err)
	require.Equal(t, 1, writes)
	plan, err = executor().Plan(t.Context())
	require.NoError(t, err)
	found := false
	for _, step := range plan.Steps {
		if step.Address == "resource.workload" {
			require.Equal(t, runtime.DecisionCreate, step.Decision)
			found = true
		}
	}
	require.True(t, found)
}
