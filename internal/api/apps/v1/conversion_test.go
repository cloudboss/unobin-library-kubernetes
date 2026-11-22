package v1

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func fullInput(t *testing.T) *DeploymentResource {
	t.Helper()
	data, err := os.ReadFile("testdata/ub/full/input.ub")
	require.NoError(t, err)
	var r DeploymentResource
	require.NoError(t, ub.Unmarshal(data, &r))
	return &r
}

func TestCompleteModelConversion(t *testing.T) {
	r := fullInput(t)
	obj, err := r.expand(nil)
	require.NoError(t, err)
	want, err := expandValue(reflect.ValueOf(r.Spec), reflect.Value{}, "spec")
	require.NoError(t, err)
	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	var expected map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&expected))
	actual, err := nativeMap(obj.Spec)
	require.NoError(t, err)
	assertNativeMembers(t, expected, actual, "spec")
	obj.UID = "uid"
	observed, err := r.output(obj, "server")
	require.NoError(t, err)
	require.Nil(t, observed.Spec.Paused)
	require.Equal(t, "2", *observed.Spec.Replicas)
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
		{Secret: projection.Secret},
		{ConfigMap: &configMaps},
		{DownwardAPI: projection.DownwardAPI},
		{ServiceAccountToken: projection.ServiceAccountToken},
	}, (*pod.Volume)[0].Projected.Sources)
	require.Equal(t, "100m", (*(*pod.Container)[0].Resources.Requests)["example.com/value"])
}

func assertNativeMembers(t *testing.T, want, got any, location string) {
	t.Helper()
	require.Equal(t, nativeInputMembers(t, want, want, location),
		nativeInputMembers(t, want, got, location), location)
}

func nativeInputMembers(t *testing.T, want, got any, location string) any {
	t.Helper()
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		require.True(t, ok, location)
		members := make(map[string]any)
		for key, value := range expected {
			if value == "" {
				continue
			}
			require.Contains(t, actual, key, location)
			members[key] = nativeInputMembers(t, value, actual[key], location+"."+key)
		}
		return members
	case []any:
		actual, ok := got.([]any)
		require.True(t, ok, location)
		members := make([]any, len(actual))
		for i, value := range actual {
			if i < len(expected) {
				members[i] = nativeInputMembers(t, expected[i], value, location)
			} else {
				members[i] = value
			}
		}
		return members
	default:
		return got
	}
}

func deployment() *DeploymentResource {
	return &DeploymentResource{
		Metadata: Metadata{Name: new("example")},
		Spec: DeploymentSpec{
			Selector: &Selector{MatchLabels: new(map[string]string{"app": "example"})},
			Template: PodTemplate{
				Metadata: Metadata{Labels: new(map[string]string{"app": "example"})},
				Spec:     PodSpec{Container: new([]Container{{Name: "app", Image: new("nginx:stable")}})},
			},
		},
	}
}

func TestExpansionDefaults(t *testing.T) {
	r := deployment()
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, "default", obj.Namespace)
	require.Empty(t, obj.Spec.Template.Namespace)
	require.Nil(t, obj.Spec.Replicas)
	require.Empty(t, obj.Spec.Strategy.Type)
	require.Equal(t, int32(600), *obj.Spec.ProgressDeadlineSeconds)
	require.Equal(t, int32(10), *obj.Spec.RevisionHistoryLimit)
	pod := obj.Spec.Template.Spec
	require.Equal(t, corev1.DNSClusterFirst, pod.DNSPolicy)
	require.Equal(t, corev1.RestartPolicyAlways, pod.RestartPolicy)
	require.Equal(t, int64(30), *pod.TerminationGracePeriodSeconds)
	require.True(t, *pod.AutomountServiceAccountToken)
	require.True(t, *pod.EnableServiceLinks)
	require.False(t, *pod.ShareProcessNamespace)
	require.Equal(t, "/dev/termination-log", pod.Containers[0].TerminationMessagePath)
	require.Empty(t, pod.Containers[0].ImagePullPolicy)
	r.Spec.Template.Spec.AutomountServiceAccountToken = new(false)
	r.Spec.Template.Spec.TerminationGracePeriodSeconds = new(int64(0))
	obj, err = r.expand(nil)
	require.NoError(t, err)
	require.False(t, *obj.Spec.Template.Spec.AutomountServiceAccountToken)
	require.Zero(t, *obj.Spec.Template.Spec.TerminationGracePeriodSeconds)
}

func TestReplicaAndStrategyConversion(t *testing.T) {
	for _, value := range []string{"", "0", "2", "-1", "2147483647", "-2147483648"} {
		t.Run(value, func(t *testing.T) {
			r := deployment()
			r.Spec.Replicas = &value
			obj, err := r.expand(nil)
			require.NoError(t, err)
			if value == "" {
				require.Nil(t, obj.Spec.Replicas)
			} else {
				require.NotNil(t, obj.Spec.Replicas)
			}
		})
	}
	for _, value := range []string{"no", "2147483648", "-2147483649", "1.0"} {
		r := deployment()
		r.Spec.Replicas = &value
		_, err := r.expand(nil)
		require.Error(t, err)
	}
	for _, strategy := range []*Strategy{
		{}, {RollingUpdate: &RollingUpdate{}},
		{RollingUpdate: &RollingUpdate{MaxSurge: new("2"), MaxUnavailable: new("0%")}},
		{Type: new("Recreate")},
	} {
		r := deployment()
		r.Spec.Strategy = strategy
		obj, err := r.expand(nil)
		require.NoError(t, err)
		if strategy.Type != nil {
			require.Equal(t, appsv1.RecreateDeploymentStrategyType, obj.Spec.Strategy.Type)
			require.Nil(t, obj.Spec.Strategy.RollingUpdate)
		} else {
			require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, obj.Spec.Strategy.Type)
		}
	}
}

func TestDeploymentNumericCasts(t *testing.T) {
	r := deployment()
	r.Spec.MinReadySeconds = new(int64(2147483648))
	r.Spec.ProgressDeadlineSeconds = new(int64(-2147483649))
	r.Spec.RevisionHistoryLimit = new(int64(4294967296))
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, int32(-2147483648), obj.Spec.MinReadySeconds)
	require.Equal(t, int32(2147483647), *obj.Spec.ProgressDeadlineSeconds)
	require.Zero(t, *obj.Spec.RevisionHistoryLimit)
}

func TestMetadataNameValidation(t *testing.T) {
	for _, field := range []string{"name", "generate-name"} {
		r := deployment()
		r.Metadata.Name = nil
		if field == "name" {
			r.Metadata.Name = new("")
		} else {
			r.Metadata.GenerateName = new("")
		}
		require.NotEmpty(t, checkNativeDeploymentInputs(t, r), field)
	}
	r := deployment()
	r.Metadata.Name = nil
	r.Metadata.GenerateName = new("example--")
	_, err := r.expand(nil)
	require.NoError(t, err)
}

func TestInputIdentitySemantics(t *testing.T) {
	reg := runtime.MakeResource[
		DeploymentResource, *DeploymentResourceOutput, *config.Configuration,
	]((&DeploymentResource{}).ResourceDefinition())
	r := deployment()
	data, err := ub.Marshal(r)
	require.NoError(t, err)
	var before map[string]any
	require.NoError(t, ub.Unmarshal(data, &before))
	r.Metadata.Namespace = new("default")
	r.Spec.Template.Metadata.Namespace = new("")
	equal, err := reg.InputsEqual(r, before)
	require.NoError(t, err)
	require.True(t, equal)
}

func TestOptionalBlockAndPointerValues(t *testing.T) {
	r := deployment()
	r.Spec.Template.Spec.SecurityContext = &PodSecurityContext{WindowsOptions: &WindowsOptions{}}
	r.Spec.Template.Spec.DNSConfig = &DNSConfig{Option: new([]DNSOption{{Name: "ndots"}})}
	r.Spec.Template.Spec.Volume = new([]Volume{
		{Name: new("absent")}, {Name: new("empty"), EmptyDir: &EmptyDirVolume{}},
		{Name: new("host"), HostPath: &HostPathVolume{}},
		{Name: new("csi"), CSI: &CSIVolume{Driver: "example"}},
		{Name: new("fc"), FC: &FCVolume{LUN: 0, TargetWWNs: []string{"wwn"}}},
		{Name: new("azure"), AzureDisk: &AzureDiskVolume{
			CachingMode: "None", DataDiskURI: "uri", DiskName: "disk",
		}},
	})
	r.Spec.Template.Spec.Container = new([]Container{{
		Name: "app", SecurityContext: &ContainerSecurityContext{},
		LivenessProbe: &Probe{GRPC: &GRPC{Port: 8080}},
		EnvFrom:       new([]EnvFrom{{SecretRef: &EnvFromRef{Name: "secret"}}}),
	}})
	obj, err := r.expand(nil)
	require.NoError(t, err)
	pod := obj.Spec.Template.Spec
	require.False(t, *pod.SecurityContext.RunAsNonRoot)
	require.Equal(t, "", *pod.SecurityContext.WindowsOptions.GMSACredentialSpec)
	require.Equal(t, "", *pod.SecurityContext.WindowsOptions.GMSACredentialSpecName)
	require.False(t, *pod.SecurityContext.WindowsOptions.HostProcess)
	require.Equal(t, "", *pod.SecurityContext.WindowsOptions.RunAsUserName)
	require.Equal(t, "", *pod.DNSConfig.Options[0].Value)
	require.Nil(t, pod.Volumes[0].EmptyDir)
	require.Equal(t, &corev1.EmptyDirVolumeSource{}, pod.Volumes[1].EmptyDir)
	require.Equal(t, corev1.HostPathType(""), *pod.Volumes[2].HostPath.Type)
	require.Equal(t, "", *pod.Volumes[3].CSI.FSType)
	require.False(t, *pod.Volumes[3].CSI.ReadOnly)
	require.Equal(t, int32(0), *pod.Volumes[4].FC.Lun)
	require.Nil(t, pod.Volumes[5].AzureDisk.Kind)
	require.Equal(t, "", *pod.Volumes[5].AzureDisk.FSType)
	require.False(t, *pod.Volumes[5].AzureDisk.ReadOnly)
	container := pod.Containers[0]
	require.True(t, *container.SecurityContext.AllowPrivilegeEscalation)
	require.False(t, *container.SecurityContext.Privileged)
	require.False(t, *container.SecurityContext.ReadOnlyRootFilesystem)
	require.False(t, *container.SecurityContext.RunAsNonRoot)
	require.False(t, *container.EnvFrom[0].SecretRef.Optional)
	require.Equal(t, "", *container.LivenessProbe.GRPC.Service)
	require.Equal(t, int32(3), container.LivenessProbe.FailureThreshold)
	require.Equal(t, int32(10), container.LivenessProbe.PeriodSeconds)
	require.Equal(t, int32(1), container.LivenessProbe.SuccessThreshold)
	require.Equal(t, int32(1), container.LivenessProbe.TimeoutSeconds)
}

func TestProjectedSourceOrderAndNormalization(t *testing.T) {
	r := deployment()
	r.Spec.Template.Spec.Volume = new([]Volume{{Name: new("projection"), Projected: &ProjectedVolume{
		Sources: []ProjectionGroup{
			{Secret: new([]SecretProjection{{Name: new("s1")}, {Name: new("s2")}}),
				ConfigMap:           new([]ConfigMapProjection{{Name: new("c1")}, {Name: new("c2")}}),
				DownwardAPI:         &DownwardAPIProjection{},
				ServiceAccountToken: &ServiceAccountTokenProjection{Path: "token1"}},
			{ServiceAccountToken: &ServiceAccountTokenProjection{Path: "token2"}},
		},
	}}})
	obj, err := r.expand(nil)
	require.NoError(t, err)
	sources := obj.Spec.Template.Spec.Volumes[0].Projected.Sources
	require.Equal(t, []corev1.VolumeProjection{
		{Secret: &corev1.SecretProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: "s1"}, Optional: new(false),
		}},
		{Secret: &corev1.SecretProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: "s2"}, Optional: new(false),
		}},
		{ConfigMap: &corev1.ConfigMapProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: "c1"}, Optional: new(false),
		}},
		{ConfigMap: &corev1.ConfigMapProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: "c2"}, Optional: new(false),
		}},
		{DownwardAPI: &corev1.DownwardAPIProjection{}},
		{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
			Path: "token1", ExpirationSeconds: new(int64(3600)),
		}},
		{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
			Path: "token2", ExpirationSeconds: new(int64(3600)),
		}},
	}, sources)
	obj.UID = "uid"
	out, err := r.output(obj, "server")
	require.NoError(t, err)
	groups := (*out.Spec.Template.Spec.Volume)[0].Projected.Sources
	require.Equal(t, []ProjectionGroup{
		{Secret: new([]SecretProjection{{Name: new("s1"), Optional: new(false)}})},
		{Secret: new([]SecretProjection{{Name: new("s2"), Optional: new(false)}})},
		{ConfigMap: new([]ConfigMapProjection{{Name: new("c1")}})},
		{ConfigMap: new([]ConfigMapProjection{{Name: new("c2")}})},
		{DownwardAPI: &DownwardAPIProjection{}},
		{ServiceAccountToken: &ServiceAccountTokenProjection{
			Audience: new(""), Path: "token1", ExpirationSeconds: new(int64(3600)),
		}},
		{ServiceAccountToken: &ServiceAccountTokenProjection{
			Audience: new(""), Path: "token2", ExpirationSeconds: new(int64(3600)),
		}},
	}, groups)
}
