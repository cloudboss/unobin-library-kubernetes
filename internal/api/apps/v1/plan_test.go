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
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestStateBackedPlanApply(t *testing.T) {
	var stored *appsv1.Deployment
	var credentials []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		switch req.Method {
		case "POST":
			stored = &appsv1.Deployment{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.UID = "uid"
			stored.Spec.Replicas = new(int32(1))
			stored.Spec.Strategy = appsv1.DeploymentStrategy{Type: "RollingUpdate"}
			stored.Spec.Template.Spec.ServiceAccountName = "default"
			stored.Spec.Template.Spec.SchedulerName = "default-scheduler"
			stored.Spec.Template.Spec.Containers[0].ImagePullPolicy = "IfNotPresent"
			for i := range stored.Spec.Template.Spec.Volumes {
				if disk := stored.Spec.Template.Spec.Volumes[i].AzureDisk; disk != nil && disk.Kind == nil {
					disk.Kind = new(corev1.AzureManagedDisk)
				}
			}
		case "PATCH":
			var patch []patchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&patch))
			encoded, err := json.Marshal(patch)
			require.NoError(t, err)
			operations, err := jsonpatch.DecodePatch(encoded)
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			after, err := operations.Apply(before)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(after, stored))
		case "GET":
			if stored == nil {
				apiError(t, w, 404)
				return
			}
		case "DELETE":
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	source, err := os.ReadFile("testdata/ub/state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	lib := &runtime.Library{
		Name: "kubernetes-apps-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"deployment": runtime.MakeResource[
				DeploymentResource, *DeploymentResourceOutput, *config.Configuration,
			]((&DeploymentResource{}).ResourceDefinition()),
		},
	}
	libs := map[string]*runtime.Library{"apps": lib}
	store, err := local.NewStore(t.TempDir(), "deployment", "test", encrypters.Noop{})
	require.NoError(t, err)
	input := deployment()
	inputs := func(r *DeploymentResource) map[string]any {
		encoded, err := ub.Marshal(r.Spec)
		require.NoError(t, err)
		var spec map[string]any
		require.NoError(t, ub.Unmarshal(encoded, &spec))
		return map[string]any{
			"server": *c.Server, "token": "first", "name": "example",
			"labels": map[string]any{"app": "example"}, "spec": spec,
		}
	}
	executor := func(in map[string]any) *runtime.Executor {
		return &runtime.Executor{
			DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: in,
			Store: store, Factory: state.FactoryInfo{
				Name: "deployment", Version: "v0", ContentRevision: "c0",
			},
		}
	}
	plan := func(r *DeploymentResource, want runtime.Decision, change func(map[string]any)) (
		*runtime.Executor, *runtime.Plan,
	) {
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
	exec, p := plan(input, runtime.DecisionCreate, nil)
	apply(exec, p)
	plan(input, runtime.DecisionNoOp, nil)
	stored.Generation = 99
	stored.ResourceVersion = "changed"
	stored.Status = appsv1.DeploymentStatus{ObservedGeneration: 98}
	plan(input, runtime.DecisionNoOp, nil)
	input.Spec.Template.Spec.Container = new([]Container{{Name: "app", Image: new("nginx:changed")}})
	exec, p = plan(input, runtime.DecisionUpdate, nil)
	apply(exec, p)
	require.Equal(t, int32(1), *stored.Spec.Replicas)
	require.Equal(t, "default-scheduler", stored.Spec.Template.Spec.SchedulerName)
	plan(input, runtime.DecisionNoOp, nil)
	plan(input, runtime.DecisionReplace, func(in map[string]any) { in["name"] = "different" })
	plan(input, runtime.DecisionReplace, func(in map[string]any) {
		in["server"] = "http://unused.invalid"
	})
	plan(input, runtime.DecisionNoOp, func(in map[string]any) { in["token"] = "current" })
	require.Contains(t, credentials, "Bearer current")
	input = fullInput(t)
	exec, p = plan(input, runtime.DecisionReplace, nil)
	apply(exec, p)
	plan(input, runtime.DecisionNoOp, nil)
	for _, tc := range []struct {
		name     string
		change   func(*DeploymentSpec)
		decision runtime.Decision
	}{
		{"selector labels", func(s *DeploymentSpec) {
			s.Selector.MatchLabels = new(map[string]string{"new": "label"})
		}, runtime.DecisionReplace},
		{"selector expression key", func(s *DeploymentSpec) {
			(*s.Selector.MatchExpressions)[0].Key = new("new")
		}, runtime.DecisionReplace},
		{"selector expression operator", func(s *DeploymentSpec) {
			(*s.Selector.MatchExpressions)[0].Operator = new("other")
		}, runtime.DecisionReplace},
		{"selector expression values", func(s *DeploymentSpec) {
			(*s.Selector.MatchExpressions)[0].Values = new([]string{"other"})
		}, runtime.DecisionReplace},
		{"template name", func(s *DeploymentSpec) {
			s.Template.Metadata.Name = new("other")
		}, runtime.DecisionReplace},
		{"template generate name", func(s *DeploymentSpec) {
			s.Template.Metadata.Name = nil
			s.Template.Metadata.GenerateName = new("other-")
		}, runtime.DecisionReplace},
		{"template namespace", func(s *DeploymentSpec) {
			s.Template.Metadata.Namespace = new("other")
		}, runtime.DecisionReplace},
		{"required match field key", func(s *DeploymentSpec) {
			(*requiredNodeTerm(s).MatchFields)[0].Key = "other"
		}, runtime.DecisionReplace},
		{"required match field operator", func(s *DeploymentSpec) {
			(*requiredNodeTerm(s).MatchFields)[0].Operator = "other"
		}, runtime.DecisionReplace},
		{"required match field values", func(s *DeploymentSpec) {
			(*requiredNodeTerm(s).MatchFields)[0].Values = new([]string{"other"})
		}, runtime.DecisionReplace},
		{"preferred match field", func(s *DeploymentSpec) {
			terms := s.Template.Spec.Affinity.NodeAffinity.
				PreferredDuringSchedulingIgnoredDuringExecution
			(*(*terms)[0].Preference.MatchFields)[0].Key = "other"
		}, runtime.DecisionReplace},
		{"azure file namespace", func(s *DeploymentSpec) {
			(*s.Template.Spec.Volume)[0].AzureFile.SecretNamespace = new("other")
		}, runtime.DecisionReplace},
		{"claim access", func(s *DeploymentSpec) {
			claimSpec(s).AccessModes = []string{"ReadOnlyMany"}
		}, runtime.DecisionReplace},
		{"claim limits", func(s *DeploymentSpec) {
			claimSpec(s).Resources.Limits = new(map[string]string{"storage": "2Gi"})
		}, runtime.DecisionReplace},
		{"claim selector", func(s *DeploymentSpec) {
			claimSpec(s).Selector.MatchLabels = new(map[string]string{"other": "value"})
		}, runtime.DecisionReplace},
		{"claim volume name", func(s *DeploymentSpec) {
			claimSpec(s).VolumeName = new("other")
		}, runtime.DecisionReplace},
		{"claim storage class", func(s *DeploymentSpec) {
			claimSpec(s).StorageClassName = new("other")
		}, runtime.DecisionReplace},
		{"claim volume mode", func(s *DeploymentSpec) {
			claimSpec(s).VolumeMode = new("Filesystem")
		}, runtime.DecisionReplace},
		{"image", func(s *DeploymentSpec) {
			(*s.Template.Spec.Container)[0].Image = new("other")
		}, runtime.DecisionUpdate},
		{"env", func(s *DeploymentSpec) { (*s.Template.Spec.Container)[0].Env = nil }, runtime.DecisionUpdate},
		{"replicas", func(s *DeploymentSpec) { s.Replicas = new("4") }, runtime.DecisionUpdate},
		{"node expression", func(s *DeploymentSpec) {
			(*requiredNodeTerm(s).MatchExpressions)[0].Key = new("other")
		}, runtime.DecisionUpdate},
		{"claim requests", func(s *DeploymentSpec) {
			claimSpec(s).Resources.Requests = new(map[string]string{"storage": "2Gi"})
		}, runtime.DecisionUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := fullInput(t)
			tc.change(&changed.Spec)
			plan(changed, tc.decision, nil)
		})
	}
	stored.UID = "replacement"
	_, err = executor(inputs(input)).Plan(t.Context())
	require.ErrorContains(t, err, "UID changed")
	stored = nil
	plan(input, runtime.DecisionCreate, nil)
	for _, tc := range []struct {
		name string
		set  func(*DeploymentSpec, bool)
	}{
		{"CPU numeric equality", func(s *DeploymentSpec, second bool) {
			value := "0.1"
			if second {
				value = "100m"
			}
			(*s.Template.Spec.Container)[0].Resources.Requests =
				new(map[string]string{"cpu": value})
		}},
		{"storage numeric equality", func(s *DeploymentSpec, second bool) {
			value := "1Gi"
			if second {
				value = "1073741824"
			}
			(*s.Template.Spec.Container)[0].Resources.Requests =
				new(map[string]string{"memory": value})
		}},
		{"claim limits numeric equality", func(s *DeploymentSpec, second bool) {
			value := "1Gi"
			if second {
				value = "1073741824"
			}
			claimSpec(s).Resources.Limits = new(map[string]string{"storage": value})
		}},
		{"selector empty values", func(s *DeploymentSpec, second bool) {
			s.Selector.MatchExpressions = new([]SelectorRequirement{{Operator: new("Exists")}})
			if second {
				(*s.Selector.MatchExpressions)[0].Values = new([]string{})
			}
		}},
		{"selector empty map", func(s *DeploymentSpec, second bool) {
			s.Selector.MatchLabels = nil
			if second {
				s.Selector.MatchLabels = new(map[string]string{})
			}
		}},
		{"selector empty list", func(s *DeploymentSpec, second bool) {
			s.Selector.MatchExpressions = nil
			if second {
				s.Selector.MatchExpressions = new([]SelectorRequirement{})
			}
		}},
		{"replacement empty collection", func(s *DeploymentSpec, second bool) {
			requiredNodeTerm(s).MatchFields = nil
			if second {
				requiredNodeTerm(s).MatchFields = new([]NodeField{})
			}
		}},
		{"claim empty limits", func(s *DeploymentSpec, second bool) {
			claimSpec(s).Resources.Limits = nil
			if second {
				claimSpec(s).Resources.Limits = new(map[string]string{})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored = nil
			before, after := fullInput(t), fullInput(t)
			tc.set(&before.Spec, false)
			tc.set(&after.Spec, true)
			exec, p := plan(before, runtime.DecisionCreate, nil)
			apply(exec, p)
			plan(after, runtime.DecisionNoOp, nil)
		})
	}
	t.Run("Azure Disk computed kind", func(t *testing.T) {
		stored = nil
		r := deployment()
		r.Spec.Template.Spec.Volume = new([]Volume{{Name: new("disk"), AzureDisk: &AzureDiskVolume{
			CachingMode: "None", DataDiskURI: "disk-uri", DiskName: "disk-name",
		}}})
		exec, p := plan(r, runtime.DecisionCreate, nil)
		apply(exec, p)
		r.Spec.MinReadySeconds = new(int64(2))
		exec, p = plan(r, runtime.DecisionUpdate, nil)
		apply(exec, p)
		require.NotNil(t, stored.Spec.Template.Spec.Volumes[0].AzureDisk.Kind)
		require.Equal(t, corev1.AzureManagedDisk, *stored.Spec.Template.Spec.Volumes[0].AzureDisk.Kind)
		plan(r, runtime.DecisionNoOp, nil)
	})
	for _, value := range []string{"0.1", "100m"} {
		r := deployment()
		r.Spec.Template.Spec.Container = new([]Container{{
			Name: "app", Resources: &Resources{Requests: new(map[string]string{"cpu": value})},
		}})
		if value == "0.1" {
			input = r
		} else {
			require.True(t, equivalentSpec(input.Spec, r.Spec))
		}
	}
}

func requiredNodeTerm(s *DeploymentSpec) *NodeTerm {
	selector := s.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	return &(*selector.NodeSelectorTerm)[0]
}

func claimSpec(s *DeploymentSpec) *ClaimSpec {
	return &(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec
}
