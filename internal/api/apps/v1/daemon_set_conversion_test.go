package v1

import (
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"testing"
)

func TestDaemonSetOptionalBlockAndPointerValues(t *testing.T) {
	r := daemonSetWithPod()
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

func TestDaemonSetProjectedSourceOrderAndNormalization(t *testing.T) {
	r := daemonSetWithPod()
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
