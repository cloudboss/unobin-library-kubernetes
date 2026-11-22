package v1

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
)

func fullInput(t *testing.T) *JobResource {
	t.Helper()
	data, err := os.ReadFile("testdata/ub/full/input.ub")
	require.NoError(t, err)
	var r JobResource
	data = bytes.TrimPrefix(data, []byte("input:"))
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
	template := expected["template"].(map[string]any)
	podSpec := template["spec"].(map[string]any)
	volume := podSpec["volumes"].([]any)[0].(map[string]any)
	claim := volume["ephemeral"].(map[string]any)["volumeClaimTemplate"].(map[string]any)
	claim["metadata"].(map[string]any)["creationTimestamp"] = nil
	actual, err := nativeMap(obj.Spec)
	require.NoError(t, err)
	assertNativeMembers(t, expected, actual, "spec")
	obj.UID = "uid"
	observed, err := r.output(obj, "server")
	require.NoError(t, err)

	require.Equal(t, int64(2), *observed.Spec.Completions)
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
	configMap := (*projection.ConfigMap)[0]
	configMap.Optional = nil
	require.Equal(t, []ProjectionGroup{
		{Secret: projection.Secret},
		{ConfigMap: new([]ConfigMapProjection{configMap})},
		{DownwardAPI: projection.DownwardAPI},
		{ServiceAccountToken: projection.ServiceAccountToken},
	}, (*pod.Volume)[0].Projected.Sources)
	require.Nil(t, (*(*pod.Volume)[0].Projected.Sources[1].ConfigMap)[0].Optional)
	require.Equal(t, "100m", (*(*pod.Container)[0].Resources.Requests)["example.com/value"])
}

func assertNativeMembers(t *testing.T, want, got any, location string) {
	t.Helper()
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		require.True(t, ok, location)
		for key, value := range expected {
			if value == "" {
				continue
			}
			require.Contains(t, actual, key, location)
			assertNativeMembers(t, value, actual[key], location+"."+key)
		}
	case []any:
		actual, ok := got.([]any)
		require.True(t, ok, location)
		require.Equal(t, expected, actual, location)
	default:
		require.Equal(t, want, got, location)
	}
}

func TestMetadataNameValidation(t *testing.T) {
	for _, field := range []string{"name", "generate-name"} {
		r := job()
		r.Metadata.Name = nil
		if field == "name" {
			r.Metadata.Name = new("")
		} else {
			r.Metadata.GenerateName = new("")
		}
		require.NotEmpty(t, checkNativeJobInputs(t, r), field)
	}
	r := job()
	r.Metadata.Name = nil
	r.Metadata.GenerateName = new("example--")
	_, err := r.expand(nil)
	require.NoError(t, err)
}

func TestOptionalBlockAndPointerValues(t *testing.T) {
	r := job()
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
	r := job()
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
		{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "s1"},
			Optional: new(false)}},
		{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "s2"},
			Optional: new(false)}},
		{ConfigMap: &corev1.ConfigMapProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: "c1"}, Optional: new(false)}},
		{ConfigMap: &corev1.ConfigMapProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: "c2"}, Optional: new(false)}},
		{DownwardAPI: &corev1.DownwardAPIProjection{}},
		{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
			Path: "token1", ExpirationSeconds: new(int64(3600))}},
		{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
			Path: "token2", ExpirationSeconds: new(int64(3600))}},
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
			Audience: new(""), Path: "token1", ExpirationSeconds: new(int64(3600))}},
		{ServiceAccountToken: &ServiceAccountTokenProjection{
			Audience: new(""), Path: "token2", ExpirationSeconds: new(int64(3600))}},
	}, groups)
}
