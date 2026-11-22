package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
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

func TestDaemonSetStateBackedPlanApply(t *testing.T) {
	var stored *appsv1.DaemonSet
	var credentials []string
	mutations := 0
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		if req.Method != "GET" {
			mutations++
		}
		switch req.Method {
		case "POST":
			stored = &appsv1.DaemonSet{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.UID = "uid"
			if stored.Name == "" {
				stored.Name = "example"
			}
			stored.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{Type: "RollingUpdate"}
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
	source, err := os.ReadFile("testdata/ub/daemon-set-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	lib := &runtime.Library{
		Name: "kubernetes-apps-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"daemon-set": runtime.MakeResource[
				DaemonSetResource, *DaemonSetResourceOutput, *config.Configuration,
			]((&DaemonSetResource{}).ResourceDefinition()),
		},
	}
	libs := map[string]*runtime.Library{"apps": lib}
	addNativeDaemonSetConstraints(t, lib)
	store, err := local.NewStore(t.TempDir(), "daemon-set", "test", encrypters.Noop{})
	require.NoError(t, err)
	input := daemonSetWithPod()
	inputs := func(r *DaemonSetResource) map[string]any {
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
				Name: "daemon-set", Version: "v0", ContentRevision: "c0",
			},
		}
	}
	plan := func(r *DaemonSetResource, want runtime.Decision, change func(map[string]any)) (
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
	exec, p = plan(input, runtime.DecisionNoOp, nil)
	apply(exec, p)
	exec, p = plan(input, runtime.DecisionNoOp, nil)
	t.Run("invalid evaluated unchanged input", func(t *testing.T) {
		before := mutations
		in := inputs(input)
		in["name"] = ""
		_, err := executor(in).Plan(t.Context())
		require.ErrorContains(t, err, "resource.workload")
		require.ErrorContains(t, err, "constraint")
		require.Equal(t, before, mutations)
	})
	for _, value := range []string{"", "malformed", "1Gi"} {
		t.Run("evaluated quantity="+value, func(t *testing.T) {
			before := mutations
			r := daemonSetWithPod()
			r.Spec.Template.Spec.Volume = new([]Volume{{
				Name: new("scratch"), EmptyDir: &EmptyDirVolume{SizeLimit: &value},
			}})
			exec := executor(inputs(r))
			p, err := exec.Plan(t.Context())
			require.NoError(t, err)
			if value == "1Gi" {
				return
			} else {
				encoded, err := runtime.EncodePlan(p)
				require.NoError(t, err)
				file, err := runtime.DecodePlan(encoded)
				require.NoError(t, err)
				_, err = exec.ApplyPlan(t.Context(), file)
				require.ErrorContains(t, err, "sizeLimit")
			}
			require.Equal(t, before, mutations)
		})
	}
	t.Run("invalid saved NoOp", func(t *testing.T) {
		before := mutations
		encoded, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		file.Inputs["name"] = ""
		exec.Inputs["name"] = ""
		t.Cleanup(func() { exec.Inputs["name"] = "example" })
		for i := range file.Steps {
			step := &file.Steps[i]
			if step.Address == "resource.workload" {
				require.Equal(t, runtime.DecisionNoOp, step.Decision)
				step.Inputs["metadata"].(map[string]any)["name"] = ""
			}
		}
		_, err = exec.ApplyPlan(t.Context(), file)
		require.ErrorContains(t, err, "resource.workload")
		require.ErrorContains(t, err, "constraint")
		require.Equal(t, before, mutations)
	})
	t.Run("invalid saved NoOp nested input", func(t *testing.T) {
		before := mutations
		exec := executor(inputs(input))
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		encoded, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		setPolicy := func(spec map[string]any) {
			pod := spec["template"].(map[string]any)["spec"].(map[string]any)
			pod["restart-policy"] = "invalid"
		}
		setPolicy(file.Inputs["spec"].(map[string]any))
		setPolicy(exec.Inputs["spec"].(map[string]any))
		for i := range file.Steps {
			if step := &file.Steps[i]; step.Address == "resource.workload" {
				require.Equal(t, runtime.DecisionNoOp, step.Decision)
				setPolicy(step.Inputs["spec"].(map[string]any))
			}
		}
		_, err = exec.ApplyPlan(t.Context(), file)
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		require.Equal(t, before, mutations)
	})
	for _, value := range []string{"", "invalid"} {
		if !t.Run("invalid saved replacement quantity="+value, func(t *testing.T) {
			before := mutations
			replacement, replacementPlan := plan(input, runtime.DecisionReplace, func(in map[string]any) {
				in["name"] = "replacement"
			})
			encoded, err := runtime.EncodePlan(replacementPlan)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(encoded)
			require.NoError(t, err)
			setQuantity := func(spec map[string]any) {
				pod := spec["template"].(map[string]any)["spec"].(map[string]any)
				pod["volume"] = []any{map[string]any{
					"name": "scratch", "empty-dir": map[string]any{"size-limit": value},
				}}
			}
			setQuantity(file.Inputs["spec"].(map[string]any))
			setQuantity(replacement.Inputs["spec"].(map[string]any))
			for i := range file.Steps {
				if step := &file.Steps[i]; step.Address == "resource.workload" {
					setQuantity(step.Inputs["spec"].(map[string]any))
				}
			}
			_, err = replacement.ApplyPlan(t.Context(), file)
			require.ErrorContains(t, err, "quantities must match")
			require.Equal(t, before, mutations)
		}) {
			return
		}
	}
	stored.Generation = 99
	stored.ResourceVersion = "changed"
	stored.Status = appsv1.DaemonSetStatus{ObservedGeneration: 98}
	plan(input, runtime.DecisionNoOp, nil)
	input.Spec.Template.Spec.Container = new([]Container{{Name: "app", Image: new("nginx:changed")}})
	exec, p = plan(input, runtime.DecisionUpdate, nil)
	apply(exec, p)
	require.Equal(t, "default-scheduler", stored.Spec.Template.Spec.SchedulerName)
	plan(input, runtime.DecisionNoOp, nil)
	plan(input, runtime.DecisionReplace, func(in map[string]any) { in["name"] = "different" })
	plan(input, runtime.DecisionReplace, func(in map[string]any) {
		in["server"] = "http://unused.invalid"
	})
	plan(input, runtime.DecisionNoOp, func(in map[string]any) { in["token"] = "current" })
	require.Contains(t, credentials, "Bearer current")
	input = fullDaemonSetInput(t)
	exec, p = plan(input, runtime.DecisionReplace, nil)
	apply(exec, p)
	plan(input, runtime.DecisionNoOp, nil)
	for _, tc := range []struct {
		name     string
		change   func(*DaemonSetSpec)
		decision runtime.Decision
	}{
		{"selector labels", func(s *DaemonSetSpec) {
			s.Selector.MatchLabels = new(map[string]string{"new": "label"})
		}, runtime.DecisionReplace},
		{"selector expression key", func(s *DaemonSetSpec) {
			(*s.Selector.MatchExpressions)[0].Key = new("new")
		}, runtime.DecisionReplace},
		{"selector expression operator", func(s *DaemonSetSpec) {
			(*s.Selector.MatchExpressions)[0].Operator = new("other")
		}, runtime.DecisionReplace},
		{"selector expression values", func(s *DaemonSetSpec) {
			(*s.Selector.MatchExpressions)[0].Values = new([]string{"other"})
		}, runtime.DecisionReplace},
		{"template name", func(s *DaemonSetSpec) {
			s.Template.Metadata.Name = new("other")
		}, runtime.DecisionReplace},
		{"template generate name", func(s *DaemonSetSpec) {
			s.Template.Metadata.Name = nil
			s.Template.Metadata.GenerateName = new("other-")
		}, runtime.DecisionReplace},
		{"template namespace", func(s *DaemonSetSpec) {
			s.Template.Metadata.Namespace = new("other")
		}, runtime.DecisionReplace},
		{"required match field key", func(s *DaemonSetSpec) {
			(*daemonSetRequiredNodeTerm(s).MatchFields)[0].Key = "other"
		}, runtime.DecisionReplace},
		{"required match field operator", func(s *DaemonSetSpec) {
			(*daemonSetRequiredNodeTerm(s).MatchFields)[0].Operator = "other"
		}, runtime.DecisionReplace},
		{"required match field values", func(s *DaemonSetSpec) {
			(*daemonSetRequiredNodeTerm(s).MatchFields)[0].Values = new([]string{"other"})
		}, runtime.DecisionReplace},
		{"preferred match field", func(s *DaemonSetSpec) {
			terms := s.Template.Spec.Affinity.NodeAffinity.
				PreferredDuringSchedulingIgnoredDuringExecution
			(*(*terms)[0].Preference.MatchFields)[0].Key = "other"
		}, runtime.DecisionReplace},
		{"azure file namespace", func(s *DaemonSetSpec) {
			(*s.Template.Spec.Volume)[0].AzureFile.SecretNamespace = new("other")
		}, runtime.DecisionReplace},
		{"claim access", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).AccessModes = []string{"ReadOnlyMany"}
		}, runtime.DecisionReplace},
		{"claim limits", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).Resources.Limits = new(map[string]string{"storage": "2Gi"})
		}, runtime.DecisionReplace},
		{"claim selector", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).Selector.MatchLabels = new(map[string]string{"other": "value"})
		}, runtime.DecisionReplace},
		{"claim volume name", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).VolumeName = new("other")
		}, runtime.DecisionReplace},
		{"claim storage class", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).StorageClassName = new("other")
		}, runtime.DecisionReplace},
		{"claim volume mode", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).VolumeMode = new("Filesystem")
		}, runtime.DecisionReplace},
		{"image", func(s *DaemonSetSpec) {
			(*s.Template.Spec.Container)[0].Image = new("other")
		}, runtime.DecisionUpdate},
		{"env", func(s *DaemonSetSpec) {
			(*s.Template.Spec.Container)[0].Env = nil
		}, runtime.DecisionUpdate},
		{"node expression", func(s *DaemonSetSpec) {
			(*daemonSetRequiredNodeTerm(s).MatchExpressions)[0].Key = new("other")
		}, runtime.DecisionUpdate},
		{"claim requests", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).Resources.Requests = new(map[string]string{"storage": "2Gi"})
		}, runtime.DecisionUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := fullDaemonSetInput(t)
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
		set  func(*DaemonSetSpec, bool)
	}{
		{"CPU numeric equality", func(s *DaemonSetSpec, second bool) {
			value := "0.1"
			if second {
				value = "100m"
			}
			(*s.Template.Spec.Container)[0].Resources.Requests =
				new(map[string]string{"cpu": value})
		}},
		{"storage numeric equality", func(s *DaemonSetSpec, second bool) {
			value := "1Gi"
			if second {
				value = "1073741824"
			}
			(*s.Template.Spec.Container)[0].Resources.Requests =
				new(map[string]string{"memory": value})
		}},
		{"claim limits numeric equality", func(s *DaemonSetSpec, second bool) {
			value := "1Gi"
			if second {
				value = "1073741824"
			}
			daemonSetClaimSpec(s).Resources.Limits = new(map[string]string{"storage": value})
		}},
		{"selector empty values", func(s *DaemonSetSpec, second bool) {
			s.Selector.MatchExpressions = new([]SelectorRequirement{{Operator: new("Exists")}})
			if second {
				(*s.Selector.MatchExpressions)[0].Values = new([]string{})
			}
		}},
		{"selector empty map", func(s *DaemonSetSpec, second bool) {
			s.Selector.MatchLabels = nil
			if second {
				s.Selector.MatchLabels = new(map[string]string{})
			}
		}},
		{"selector empty list", func(s *DaemonSetSpec, second bool) {
			s.Selector.MatchExpressions = nil
			if second {
				s.Selector.MatchExpressions = new([]SelectorRequirement{})
			}
		}},
		{"replacement empty collection", func(s *DaemonSetSpec, second bool) {
			daemonSetRequiredNodeTerm(s).MatchFields = nil
			if second {
				daemonSetRequiredNodeTerm(s).MatchFields = new([]NodeField{})
			}
		}},
		{"claim empty limits", func(s *DaemonSetSpec, second bool) {
			daemonSetClaimSpec(s).Resources.Limits = nil
			if second {
				daemonSetClaimSpec(s).Resources.Limits = new(map[string]string{})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored = nil
			before, after := fullDaemonSetInput(t), fullDaemonSetInput(t)
			tc.set(&before.Spec, false)
			tc.set(&after.Spec, true)
			exec, p := plan(before, runtime.DecisionCreate, nil)
			apply(exec, p)
			plan(after, runtime.DecisionNoOp, nil)
		})
	}
	t.Run("Azure Disk computed kind", func(t *testing.T) {
		stored = nil
		r := daemonSetWithPod()
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
		r := daemonSetWithPod()
		r.Spec.Template.Spec.Container = new([]Container{{
			Name: "app", Resources: &Resources{Requests: new(map[string]string{"cpu": value})},
		}})
		if value == "0.1" {
			input = r
		} else {
			require.True(t, equivalentDaemonSetSpec(input.Spec, r.Spec))
		}
	}

	for _, tc := range []struct {
		name   string
		remove func(*DaemonSetSpec)
	}{
		{"selector", func(s *DaemonSetSpec) { s.Selector = nil }},
		{"template namespace", func(s *DaemonSetSpec) { s.Template.Metadata.Namespace = nil }},
		{"node match fields", func(s *DaemonSetSpec) {
			daemonSetRequiredNodeTerm(s).MatchFields = nil
		}},
		{"azure file namespace", func(s *DaemonSetSpec) {
			(*s.Template.Spec.Volume)[0].AzureFile.SecretNamespace = nil
		}},
		{"claim limits", func(s *DaemonSetSpec) {
			daemonSetClaimSpec(s).Resources.Limits = nil
		}},
		{"claim selector", func(s *DaemonSetSpec) { daemonSetClaimSpec(s).Selector = nil }},
	} {
		for _, adding := range []bool{false, true} {
			t.Run(fmt.Sprintf("immutable %s addition=%v", tc.name, adding), func(t *testing.T) {
				stored = nil
				before, after := fullDaemonSetInput(t), fullDaemonSetInput(t)
				if adding {
					tc.remove(&before.Spec)
				} else {
					tc.remove(&after.Spec)
				}
				exec, p := plan(before, runtime.DecisionCreate, nil)
				apply(exec, p)
				plan(after, runtime.DecisionReplace, nil)
			})
		}
	}
	for _, tc := range []struct {
		name     string
		prepare  func(*DaemonSetSpec)
		reverse  func(*DaemonSetSpec)
		decision runtime.Decision
	}{
		{"selector values set", func(s *DaemonSetSpec) {
			(*s.Selector.MatchExpressions)[0].Values = new([]string{"a", "b", "a"})
		}, func(s *DaemonSetSpec) {
			(*s.Selector.MatchExpressions)[0].Values = new([]string{"b", "a"})
		}, runtime.DecisionNoOp},
		{"containers list", func(s *DaemonSetSpec) {
			*s.Template.Spec.Container = append(*s.Template.Spec.Container, Container{Name: "second"})
		}, func(s *DaemonSetSpec) {
			slices.Reverse(*s.Template.Spec.Container)
		}, runtime.DecisionUpdate},
		{"volumes list", func(s *DaemonSetSpec) {
			*s.Template.Spec.Volume = append(*s.Template.Spec.Volume, Volume{Name: new("second")})
		}, func(s *DaemonSetSpec) {
			slices.Reverse(*s.Template.Spec.Volume)
		}, runtime.DecisionReplace},
		{"env list", func(s *DaemonSetSpec) {
			(*s.Template.Spec.Container)[0].Env = new([]Env{{Name: "A"}, {Name: "B"}})
		}, func(s *DaemonSetSpec) {
			slices.Reverse(*(*s.Template.Spec.Container)[0].Env)
		}, runtime.DecisionUpdate},
		{"selector expressions list", func(s *DaemonSetSpec) {
			*s.Selector.MatchExpressions = append(*s.Selector.MatchExpressions,
				SelectorRequirement{Key: new("second")})
		}, func(s *DaemonSetSpec) {
			slices.Reverse(*s.Selector.MatchExpressions)
		}, runtime.DecisionReplace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored = nil
			before, after := fullDaemonSetInput(t), fullDaemonSetInput(t)
			tc.prepare(&before.Spec)
			tc.prepare(&after.Spec)
			tc.reverse(&after.Spec)
			exec, p := plan(before, runtime.DecisionCreate, nil)
			apply(exec, p)
			plan(after, tc.decision, nil)
		})
	}
	t.Run("computed immutable omission", func(t *testing.T) {
		stored = nil
		before, after := fullDaemonSetInput(t), fullDaemonSetInput(t)
		after.Spec.Template.Metadata.Name = nil
		daemonSetClaimSpec(&after.Spec).VolumeName = nil
		daemonSetClaimSpec(&after.Spec).StorageClassName = nil
		daemonSetClaimSpec(&after.Spec).VolumeMode = nil
		exec, p := plan(before, runtime.DecisionCreate, nil)
		apply(exec, p)
		plan(after, runtime.DecisionNoOp, nil)
	})
	t.Run("destroy historical inputs without identity", func(t *testing.T) {
		stored = nil
		lib.Constraints = nil
		exec, p := plan(daemonSetWithPod(), runtime.DecisionCreate, func(in map[string]any) {
			in["name"] = ""
		})
		apply(exec, p)
		addNativeDaemonSetConstraints(t, lib)
		_, err := exec.Plan(t.Context())
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		before := mutations
		exec.Destroy = true
		p, err = exec.Plan(t.Context())
		require.NoError(t, err)
		apply(exec, p)
		require.Nil(t, stored)
		require.Equal(t, before+1, mutations)
	})
}

func daemonSetRequiredNodeTerm(s *DaemonSetSpec) *NodeTerm {
	selector := s.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	return &(*selector.NodeSelectorTerm)[0]
}

func daemonSetClaimSpec(s *DaemonSetSpec) *ClaimSpec {
	return &(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec
}

func TestDaemonSetDeferredNativeConstraints(t *testing.T) {
	for _, test := range []struct {
		fixture string
		name    string
		valid   bool
	}{
		{"deferred", "valid", true}, {"deferred", "BAD", false},
		{"deferred-threshold", "1%", true}, {"deferred-threshold", "0%", false},
		{"deferred-container-restart", "Always", true},
		{"deferred-container-restart", "Never", false},
		{"deferred-quantity", "1Gi", true},
		{"deferred-quantity", "", false},
		{"deferred-quantity", "malformed", false},
		{"deferred-replacement-quantity", "malformed", false},
		{"literal-quantity-empty", "", false},
		{"literal-quantity-invalid", "malformed", false},
		{"literal-quantity-valid", "1Gi", true},
	} {
		t.Run(test.fixture+"/"+test.name, func(t *testing.T) {
			name := test.name
			mutations := 0
			initializing := test.fixture == "deferred-replacement-quantity"
			c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					mutations++
				}
				obj := readyDaemonSet(t)
				obj.Name = name
				if strings.Contains(test.fixture, "quantity") {
					obj.Name = "seed"
					obj.Annotations = map[string]string{"quantity": name}
				}
				if strings.Contains(test.fixture, "threshold") ||
					strings.Contains(test.fixture, "container-restart") {
					obj.Name = "seed"
					obj.Annotations = map[string]string{"threshold": name, "policy": name}
				}
				if initializing || strings.HasSuffix(req.URL.Path, "/example") {
					obj.Name = "example"
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			source, err := os.ReadFile("testdata/ub/daemon-set-" + test.fixture + "/factory.ub")
			require.NoError(t, err)
			parsed, err := syntax.ParseSource("factory.ub", source)
			require.NoError(t, err)
			lib := &runtime.Library{
				Name: "kubernetes-apps-v1", Configuration: config.LibraryConfiguration(),
				Resources: map[string]runtime.ResourceRegistration{
					"daemon-set": runtime.MakeResource[
						DaemonSetResource, *DaemonSetResourceOutput, *config.Configuration,
					]((&DaemonSetResource{}).ResourceDefinition()),
				},
			}
			addNativeDaemonSetConstraints(t, lib)
			libs := map[string]*runtime.Library{"apps": lib}
			store, err := local.NewStore(t.TempDir(), "daemon-set", "test", encrypters.Noop{})
			require.NoError(t, err)
			if test.fixture == "deferred-replacement-quantity" {
				source, err := os.ReadFile("testdata/ub/daemon-set-state/factory.ub")
				require.NoError(t, err)
				baseline, err := syntax.ParseSource("factory.ub", source)
				require.NoError(t, err)
				spec, err := ub.Marshal(daemonSetWithPod().Spec)
				require.NoError(t, err)
				var values map[string]any
				require.NoError(t, ub.Unmarshal(spec, &values))
				initial := &runtime.Executor{
					DAG:          runtime.BuildSyntaxDAG(baseline.Factory.Body, libs),
					SyntaxSource: &baseline.Factory.Body, Libraries: libs,
					Inputs: map[string]any{
						"server": *c.Server, "spec": values, "name": "example", "token": "first",
						"labels": map[string]any{"app": "example"},
					}, Store: store,
					Factory: state.FactoryInfo{Name: "deferred", Version: "v0", ContentRevision: "c0"},
				}
				p, err := initial.Plan(t.Context())
				require.NoError(t, err)
				encoded, err := runtime.EncodePlan(p)
				require.NoError(t, err)
				file, err := runtime.DecodePlan(encoded)
				require.NoError(t, err)
				_, err = initial.ApplyPlan(t.Context(), file)
				require.NoError(t, err)
				mutations = 0
				initializing = false
			}
			exec := &runtime.Executor{
				DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
				SyntaxSource: &parsed.Factory.Body, Libraries: libs,
				Inputs: map[string]any{"server": *c.Server}, Store: store,
				Factory: state.FactoryInfo{Name: "deferred", Version: "v0", ContentRevision: "c0"},
			}
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			if test.fixture == "deferred-replacement-quantity" {
				for _, step := range plan.Steps {
					if step.Address == "resource.workload" {
						require.Equal(t, runtime.DecisionReplace, step.Decision)
					}
				}
			}
			require.Zero(t, mutations)
			encoded, err := runtime.EncodePlan(plan)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(encoded)
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), file)
			if !test.valid {
				require.ErrorContains(t, err, "resource.workload")
				if strings.Contains(test.fixture, "quantity") {
					require.ErrorContains(t, err, "sizeLimit")
				} else {
					require.ErrorContains(t, err, "constraint")
				}
				if strings.HasPrefix(test.fixture, "literal-") {
					require.Zero(t, mutations)
				} else {
					require.Equal(t, 1, mutations)
				}
			} else {
				require.NoError(t, err)
				if strings.HasPrefix(test.fixture, "literal-") {
					require.Equal(t, 1, mutations)
				} else {
					require.Equal(t, 2, mutations)
				}
			}
		})
	}
}
