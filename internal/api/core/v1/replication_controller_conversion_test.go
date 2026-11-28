package v1

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReplicationControllerFullRequestAndOutput(t *testing.T) {
	pod := fullPodInput(t)
	body, err := ub.Marshal(pod.Spec)
	require.NoError(t, err)
	r := replicationControllerInput()
	require.NoError(t, ub.Unmarshal(body, &r.Spec.Template.Spec))
	got, err := r.prepare()
	require.NoError(t, err)
	expected, err := os.ReadFile("testdata/pod-full.json")
	require.NoError(t, err)
	var want corev1.Pod
	require.NoError(t, json.Unmarshal(expected, &want))
	require.Equal(t, &corev1.ReplicationController{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ReplicationController"},
		ObjectMeta: metav1.ObjectMeta{Name: "subject", Namespace: "default",
			Labels: map[string]string{}, Annotations: map[string]string{}},
		Spec: corev1.ReplicationControllerSpec{Replicas: new(int32(1)), Selector: map[string]string{},
			Template: &corev1.PodTemplateSpec{Spec: want.Spec}},
	}, got)
	got.UID = "uid"
	out, err := r.output(got, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	expected, err = os.ReadFile("testdata/ub/pod-matrix/output.ub")
	require.NoError(t, err)
	var expectedOutput PodResourceOutput
	require.NoError(t, podFixtureValue(expected, &expectedOutput))
	require.Equal(t, expectedOutput.Spec, out.Spec.Template.Spec)
}

func TestReplicationControllerComputedAndZeroConversion(t *testing.T) {
	r := replicationControllerInput()
	r.Spec.Replicas = new(int64(0))
	r.Spec.MinReadySeconds = new(int64(math.MaxInt64))
	r.Spec.Template.Spec.AutomountServiceAccountToken = new(false)
	r.Spec.Template.Spec.EnableServiceLinks = new(false)
	r.Spec.Template.Spec.SecurityContext = &PodSecurityContext{FSGroup: new("-9223372036854775808")}
	got, err := r.prepare()
	require.NoError(t, err)
	require.Equal(t, new(int32(0)), got.Spec.Replicas)
	require.Equal(t, int32(-1), got.Spec.MinReadySeconds)
	require.Equal(t, new(false), got.Spec.Template.Spec.AutomountServiceAccountToken)
	require.Equal(t, new(false), got.Spec.Template.Spec.EnableServiceLinks)
	require.Equal(t, new(int64(math.MinInt64)), got.Spec.Template.Spec.SecurityContext.FSGroup)
	require.Nil(t, got.Spec.Template.Spec.SecurityContext.WindowsOptions)
	require.Equal(t, new(int64(0)), got.Spec.Template.Spec.TerminationGracePeriodSeconds)
	require.Empty(t, got.Spec.Template.Spec.RestartPolicy)
	require.Empty(t, got.Spec.Template.Spec.DNSPolicy)
	require.Empty(t, got.Spec.Template.Namespace)
}

func TestReplicationControllerProjectionFiltering(t *testing.T) {
	r := replicationControllerInput()
	r.IgnoreLabels = new([]string{"^ignored"})
	live, err := r.prepare()
	require.NoError(t, err)
	live.Labels = map[string]string{"visible": "root", "ignored": "root"}
	live.Spec.Template.Labels = map[string]string{"visible": "template", "ignored": "template",
		"kubernetes.io/internal": "value"}
	spec := &live.Spec.Template.Spec
	spec.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready"}, {Key: "keep"}}
	spec.Volumes = []corev1.Volume{{Name: "prefix-default-token-abcde-suffix"},
		{Name: "kube-api-access-one"}, {Name: "keep"}}
	spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: "default-token-abcde", MountPath: "/first"},
		{Name: "kube-api-access-other", MountPath: "/second"},
	}
	spec.InitContainers = append(spec.InitContainers, spec.Containers[0])
	out, err := r.output(live, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"visible": "root"}, out.Metadata.Labels)
	require.Equal(t, map[string]string{"visible": "template"}, out.Spec.Template.Metadata.Labels)
	require.Equal(t, new([]PodToleration{
		{Key: new("node.kubernetes.io/not-ready"), Effect: new(""), Operator: new(""), Value: new("")},
		{Key: new("keep"), Effect: new(""), Operator: new(""), Value: new("")},
	}), out.Spec.Template.Spec.Toleration)
	require.Equal(t, new([]PodVolumeOutput{{Name: new("kube-api-access-one")}, {Name: new("keep")}}),
		out.Spec.Template.Spec.Volume)
	for _, containers := range []*[]PodContainer{out.Spec.Template.Spec.Container,
		out.Spec.Template.Spec.InitContainer} {
		require.Equal(t, new([]PodMount{{Name: "kube-api-access-other", MountPath: "/second",
			ReadOnly: new(false), SubPath: new(""), SubPathExpr: new(""), MountPropagation: new("None")}}),
			(*containers)[0].VolumeMount)
	}
	r.Metadata.Labels = new(map[string]string{"ignored": "configured", "kubernetes.io/internal": "v"})
	out, err = r.output(live, "server", podTrackedMetadata(r.Metadata, ServiceMetadataOutput{}))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"visible": "template", "ignored": "template",
		"kubernetes.io/internal": "value"}, out.Spec.Template.Metadata.Labels)
}

func TestReplicationControllerSemanticEqualityAndParsing(t *testing.T) {
	a, b := replicationControllerInput(), replicationControllerInput()
	a.Spec.Template.Spec.SecurityContext = &PodSecurityContext{FSGroup: new("+0001"),
		SupplementalGroups: new([]int64{2, 1, 2})}
	b.Spec.Template.Spec.SecurityContext = &PodSecurityContext{FSGroup: new("1"),
		SupplementalGroups: new([]int64{1, 2})}
	a.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"), EmptyDir: &PodEmptyDirVolume{
		SizeLimit: new("1Gi")}}})
	b.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"), EmptyDir: &PodEmptyDirVolume{
		SizeLimit: new("1024Mi")}}})
	require.False(t, replicationControllerSpecChanged(a.Spec, b.Spec, false))
	a.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"), ConfigMap: &PodConfigMapVolume{
		DefaultMode: new("000644")}}})
	b.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"), ConfigMap: &PodConfigMapVolume{
		DefaultMode: new("0644")}}})
	require.False(t, replicationControllerSpecChanged(a.Spec, b.Spec, false))
	a.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"), EmptyDir: &PodEmptyDirVolume{
		SizeLimit: new("")}}})
	_, err := a.prepare()
	require.NoError(t, err)
	a.Spec.Template.Spec.Volume = new([]PodVolume{{Name: new("data"), EmptyDir: &PodEmptyDirVolume{
		SizeLimit: new("bad quantity")}}})
	_, err = a.prepare()
	require.ErrorContains(t, err, "sizeLimit")
}

func TestReplicationControllerComputedAdoption(t *testing.T) {
	r := replicationControllerInput()
	live, err := r.prepare()
	require.NoError(t, err)
	live.Spec.Template.Spec.HostIPC = true
	live.Spec.Template.Spec.NodeName = "worker"
	live.Spec.Template.Spec.TerminationGracePeriodSeconds = new(int64(30))
	out, err := r.output(live, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	replicationControllerComputed(reflect.ValueOf(&r.Spec), reflect.ValueOf(out.Spec))
	got, err := r.prepare()
	require.NoError(t, err)
	require.Equal(t, live.Spec, got.Spec)
}

func TestReplicationControllerRuntimeClassRequest(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input, want *string
	}{
		{name: "omitted"},
		{name: "empty", input: new("")},
		{name: "configured", input: new("sandbox"), want: new("sandbox")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := replicationControllerInput()
			r.Spec.Template.Spec.RuntimeClassName = tc.input
			got, err := r.prepare()
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Spec.Template.Spec.RuntimeClassName)
		})
	}
}
