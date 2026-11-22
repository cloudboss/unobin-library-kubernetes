package v1

import (
	"encoding/json"
	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func daemonSet() *DaemonSetResource {
	return &DaemonSetResource{
		Metadata: Metadata{Name: new("example")},
		Spec:     DaemonSetSpec{Template: DaemonSetPodTemplate{Metadata: Metadata{}}},
	}
}

func TestDaemonSetDefaults(t *testing.T) {
	obj, err := daemonSet().expand(nil)
	require.NoError(t, err)
	require.Equal(t, "default", obj.Namespace)
	require.Equal(t, int32(0), obj.Spec.MinReadySeconds)
	require.Equal(t, new(int32(10)), obj.Spec.RevisionHistoryLimit)
	require.Empty(t, obj.Spec.UpdateStrategy.Type)
	require.Nil(t, obj.Spec.Selector)
	require.Empty(t, obj.Spec.Template.Spec.RestartPolicy)
	require.Nil(t, obj.Spec.Template.Spec.AutomountServiceAccountToken)
}

func daemonSetWithPod() *DaemonSetResource {
	r := daemonSet()
	r.Spec.Selector = &Selector{MatchLabels: new(map[string]string{"app": "example"})}
	r.Spec.Template.Spec = &PodSpec{
		Container: new([]Container{{Name: "app", Image: new("nginx:stable")}}),
	}
	return r
}

func fullDaemonSetInput(t *testing.T) *DaemonSetResource {
	t.Helper()
	data, err := os.ReadFile("testdata/ub/daemon-set-full/input.ub")
	require.NoError(t, err)
	parsed, err := lang.ParseSource("input.ub", data)
	require.NoError(t, err)
	value, err := runtime.Eval(parsed.Body.Fields[0].Value, &runtime.EvalContext{})
	require.NoError(t, err)
	factory := value.(map[string]any)["factory"].(map[string]any)
	inputs := factory["inputs"].(map[string]any)
	encoded, err := ub.Marshal(inputs["workload"])
	require.NoError(t, err)
	var input DaemonSetResource
	require.NoError(t, ub.Unmarshal(encoded, &input))
	return &input
}

func readyDaemonSet(t *testing.T) *appsv1.DaemonSet {
	t.Helper()
	obj, err := daemonSetWithPod().expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	obj.Generation = 2
	obj.Spec.UpdateStrategy.Type = "RollingUpdate"
	obj.Status = appsv1.DaemonSetStatus{
		ObservedGeneration: 2, DesiredNumberScheduled: 1, NumberReady: 1,
		UpdatedNumberScheduled: 1, NumberAvailable: 1,
	}
	return obj
}

func TestDaemonSetStrategyDefaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy *DaemonSetStrategy
		want     appsv1.DaemonSetUpdateStrategy
	}{
		{"absent", nil, appsv1.DaemonSetUpdateStrategy{}},
		{"present", &DaemonSetStrategy{}, appsv1.DaemonSetUpdateStrategy{Type: "RollingUpdate"}},
		{"rolling", &DaemonSetStrategy{RollingUpdate: &DaemonSetRollingUpdate{}},
			appsv1.DaemonSetUpdateStrategy{
				Type: "RollingUpdate", RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxSurge: new(intstr.FromInt32(0)), MaxUnavailable: new(intstr.FromInt32(1)),
				}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := daemonSet()
			r.Spec.Strategy = tc.strategy
			obj, err := r.expand(nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, obj.Spec.UpdateStrategy)
		})
	}
	r := daemonSet()
	r.Spec.MinReadySeconds, r.Spec.RevisionHistoryLimit = new(int64(-1)), new(int64(0))
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, int32(-1), obj.Spec.MinReadySeconds)
	require.Equal(t, new(int32(0)), obj.Spec.RevisionHistoryLimit)
	r.Spec.MinReadySeconds, r.Spec.RevisionHistoryLimit = new(int64(4294967296)), new(int64(-1))
	obj, err = r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, int32(0), obj.Spec.MinReadySeconds)
	require.Equal(t, new(int32(-1)), obj.Spec.RevisionHistoryLimit)
}

func TestDaemonSetRolloutPredicates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*appsv1.DaemonSet)
		ready   bool
		failure bool
	}{
		{"ready", func(*appsv1.DaemonSet) {}, true, false},
		{"generation pending", func(o *appsv1.DaemonSet) { o.Generation++ }, false, false},
		{"impossible generation", func(o *appsv1.DaemonSet) { o.Status.ObservedGeneration++ },
			false, true},
		{"not ready", func(o *appsv1.DaemonSet) { o.Status.NumberReady = 0 }, false, false},
		{"too many ready", func(o *appsv1.DaemonSet) { o.Status.NumberReady++ }, false, false},
		{"old pod still ready", func(o *appsv1.DaemonSet) {
			o.Status.UpdatedNumberScheduled = 0
		}, false, false},
		{"updated pod not yet available", func(o *appsv1.DaemonSet) {
			o.Status.NumberAvailable = 0
		}, false, false},
		{"on delete leaves the old pod ready", func(o *appsv1.DaemonSet) {
			o.Spec.UpdateStrategy.Type = appsv1.OnDeleteDaemonSetStrategyType
			o.Status.UpdatedNumberScheduled = 0
		}, true, false},
		{"zero", func(o *appsv1.DaemonSet) {
			o.Status.NumberReady, o.Status.DesiredNumberScheduled = 0, 0
			o.Status.NumberAvailable, o.Status.UpdatedNumberScheduled = 0, 0
		}, true, false},
		{"irrelevant status", func(o *appsv1.DaemonSet) {
			o.Status.NumberUnavailable, o.Status.NumberMisscheduled = 99, 99
			o.Status.Conditions = []appsv1.DaemonSetCondition{{Type: "Failure", Status: "True"}}
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := readyDaemonSet(t)
			tc.change(obj)
			ready, err := daemonSetRolloutReady(obj)
			if tc.failure {
				require.ErrorContains(t, err, "exceeds generation")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.ready, ready)
		})
	}
}

func TestDaemonSetCompleteModelConversion(t *testing.T) {
	r := fullDaemonSetInput(t)
	require.Empty(t, checkNativeDaemonSetInputs(t, r))
	obj, err := r.expand(nil)
	require.NoError(t, err)
	expected, err := os.ReadFile("testdata/daemon-set-full.json")
	require.NoError(t, err)
	actual, err := json.Marshal(obj)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
	obj.UID = "uid"
	observed, err := r.output(obj, "server")
	require.NoError(t, err)
	pod := observed.Spec.Template.Spec
	require.Equal(t, int64(2), *pod.ActiveDeadlineSeconds)
	require.Equal(t, "2", *pod.SecurityContext.RunAsUser)
	require.Equal(t, []int64{2}, *pod.SecurityContext.SupplementalGroups)
	require.Equal(t, "example", *pod.SecurityContext.WindowsOptions.RunAsUsername)
	require.Equal(t, "0644", *(*pod.Volume)[0].ConfigMap.DefaultMode)
	require.Nil(t, (*pod.Volume)[0].Local)
	require.Nil(t, (*pod.Volume)[0].CephFS)
	require.Nil(t, (*pod.Volume)[0].AzureFile.SecretNamespace)
	require.Nil(t, (*pod.Volume)[0].RBD.SecretRef.Namespace)
	projection := (*r.Spec.Template.Spec.Volume)[0].Projected.Sources[0]
	configMaps := append([]ConfigMapProjection(nil), (*projection.ConfigMap)...)
	configMaps[0].Optional = nil
	require.Equal(t, []ProjectionGroup{
		{Secret: projection.Secret}, {ConfigMap: &configMaps},
		{DownwardAPI: projection.DownwardAPI}, {ServiceAccountToken: projection.ServiceAccountToken},
	}, (*pod.Volume)[0].Projected.Sources)
	require.Equal(t, "100m", (*(*pod.Container)[0].Resources.Requests)["example.com/value"])
	require.Equal(t, corev1.RestartPolicyAlways, obj.Spec.Template.Spec.RestartPolicy)
}

func TestDaemonSetThresholds(t *testing.T) {
	for _, value := range []string{"0", "3", "1%", "100%", "00", "0%", "101%", "-1", "2.5", ""} {
		t.Run(value, func(t *testing.T) {
			r := daemonSet()
			r.Spec.Strategy = &DaemonSetStrategy{RollingUpdate: &DaemonSetRollingUpdate{
				MaxSurge: &value, MaxUnavailable: &value,
			}}
			valid := value == "0" || value == "3" || value == "1%" || value == "100%"
			diagnostics := checkNativeDaemonSetInputs(t, r)
			require.Equal(t, valid, len(diagnostics) == 0, "%v", diagnostics)
			if valid {
				obj, err := r.expand(nil)
				require.NoError(t, err)
				require.Equal(t, intstr.Parse(value), *obj.Spec.UpdateStrategy.RollingUpdate.MaxSurge)
				require.Equal(t, intstr.Parse(value), *obj.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable)
			}
		})
	}
}
