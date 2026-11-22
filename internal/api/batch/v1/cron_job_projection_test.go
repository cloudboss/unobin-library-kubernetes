package v1

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCronJobMetadataProjection(t *testing.T) {
	cases := []struct {
		key      string
		internal bool
	}{
		{"kubernetes.io", true}, {"notkubernetes.io/key", true}, {
			"app.kubernetes.io/deprecated.daemonset.template.generation", false},
		{"service.beta.kubernetes.io/deprecated.daemonset.template.generation", false},
		{"app.kubernetes.io", false}, {"service.beta.kubernetes.io:80/key", false}, {
			"kubernetes.io:80/key", true},
		{"example.com/deprecated.daemonset.template.generation", true}, {"kubernetes.io:bad/key",
			false}, {"%/deprecated.daemonset.template.generation", false},
		{"example.com/key", false}, {"ignored.example/key", false},
	}
	for _, which := range []string{"labels", "annotations"} {
		for _, tc := range cases {
			for _, tracked := range []bool{false, true} {
				t.Run(which+tc.key+strconv.FormatBool(tracked), func(t *testing.T) {
					r := cronJob()
					obj := readyCronJob(t)
					values := map[string]string{tc.key: "remote"}
					tracking := CronJobMetadata{}
					if which == "labels" {
						obj.Labels = values
						r.IgnoreLabels = new([]string{"^ignored"})
						if tracked {
							tracking.Labels = new(map[string]string{tc.key: "old"})
						}
						tracking.Annotations = new(map[string]string{tc.key: "wrong-map"})
					} else {
						obj.Annotations = values
						r.IgnoreAnnotations = new([]string{"^ignored"})
						if tracked {
							tracking.Annotations = new(map[string]string{tc.key: "old"})
						}
						tracking.Labels = new(map[string]string{tc.key: "wrong-map"})
					}
					out, err := r.output(obj, "server", tracking)
					require.NoError(t, err)
					got := out.Metadata.Labels
					if which == "annotations" {
						got = out.Metadata.Annotations
					}
					if tracked || (!tc.internal && tc.key != "ignored.example/key") {
						require.Equal(t, map[string]string{tc.key: "remote"}, *got)
					} else {
						require.True(t, got == nil || len(*got) == 0)
					}
					require.Equal(t, "remote", values[tc.key])
				})
			}
		}
	}
	r := cronJob()
	r.IgnoreLabels = new([]string{"["})
	require.NotEmpty(t, checkNativeJobInputs(t, r))
	r = cronJob()
	r.IgnoreLabels = new([]string{"^app[.]kubernetes"})
	obj := readyCronJob(t)
	obj.Labels = map[string]string{"app.kubernetes.io/name": "x"}
	out, err := r.output(obj, "server", r.Metadata)
	require.NoError(t, err)
	require.True(t, out.Metadata.Labels == nil || len(*out.Metadata.Labels) == 0)
}

func TestCronJobTokenNormalizationAndOrder(t *testing.T) {
	r := cronJob()
	obj := readyCronJob(t)
	pod := &obj.Spec.JobTemplate.Spec.Template.Spec
	names := []string{"near-default-token-ABCD1", "extra-default-token-abcde-suffix",
		"kube-api-access-two", "keep"}
	for _, name := range names {
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: name})
		pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name: name, MountPath: "/" + name})
	}
	pod.InitContainers = []corev1.Container{pod.Containers[0]}
	pod.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready"}}
	before := obj.DeepCopy()
	out, err := r.output(obj, "server", r.Metadata)
	require.NoError(t, err)
	require.Equal(t, before, obj)
	want := []string{names[0], names[2], names[3]}
	var got []string
	for _, vol := range *out.Spec.JobTemplate.Spec.Template.Spec.Volume {
		got = append(got, *vol.Name)
	}
	require.Equal(t, want, got)
	for _, containers := range []*[]Container{
		out.Spec.JobTemplate.Spec.Template.Spec.Container,
		out.Spec.JobTemplate.Spec.Template.Spec.InitContainer} {
		got = nil
		for _, mount := range *(*containers)[0].VolumeMount {
			got = append(got, mount.Name)
		}
		require.Equal(t, want, got)
	}
	require.Equal(t, "node.kubernetes.io/not-ready",
		*(*out.Spec.JobTemplate.Spec.Template.Spec.Toleration)[0].Key)
	a, b := cronJob(), cronJob()
	a.Spec.JobTemplate.Spec.Selector =
		&Selector{MatchExpressions: new([]SelectorRequirement{{Values: new([]string{
			"b", "a", "a"})}})}
	b.Spec.JobTemplate.Spec.Selector =
		&Selector{MatchExpressions: new([]SelectorRequirement{{Values: new([]string{
			"a", "b"})}})}
	require.True(t, equivalentSpec(a.Spec.JobTemplate.Spec, b.Spec.JobTemplate.Spec))
	a.Spec.JobTemplate.Spec.Template.Spec.Affinity = &Affinity{NodeAffinity: &NodeAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: new([]PreferredSchedulingTerm{{Weight: 0}, {
			Weight: 101}})}}
	_, err = a.prepare(nil)
	require.NoError(t, err)
	b.Spec.JobTemplate.Spec.Template.Spec.Affinity = &Affinity{NodeAffinity: &NodeAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: new([]PreferredSchedulingTerm{{Weight: 101}, {
			Weight: 0}})}}
	require.True(t, immutableChanged(a.Spec.JobTemplate.Spec, b.Spec.JobTemplate.Spec))
	require.False(t, reflect.DeepEqual(a.Spec, b.Spec))
}

func TestCronJobGeneratedLabelLocations(t *testing.T) {
	for _, manual := range []*bool{nil, new(false), new(true)} {
		r := cronJob()
		obj := readyCronJob(t)
		values := map[string]string{"keep-job-name": "keep"}
		for _, key := range generatedLabels {
			values[key] = "generated"
		}
		obj.Labels = values
		obj.Spec.JobTemplate.Labels = values
		obj.Spec.JobTemplate.Spec.ManualSelector = manual
		obj.Spec.JobTemplate.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: values, MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "job-name", Values: []string{"job-name"}},
			},
		}
		obj.Spec.JobTemplate.Spec.Template.Labels = values
		out, err := r.output(obj, "server", r.Metadata)
		require.NoError(t, err)
		want := map[string]string{"keep-job-name": "keep"}
		require.Equal(t, want, *out.Metadata.Labels)
		require.Equal(t, values, *out.Spec.JobTemplate.Metadata.Labels)
		require.Equal(t, want, *out.Spec.JobTemplate.Spec.Selector.MatchLabels)
		require.Equal(t, want, *out.Spec.JobTemplate.Spec.Template.Metadata.Labels)
		require.Equal(t, []string{"job-name"},
			*(*out.Spec.JobTemplate.Spec.Selector.MatchExpressions)[0].Values)
		require.Equal(t, values, obj.Labels)
		require.Equal(t, values, obj.Spec.JobTemplate.Spec.Selector.MatchLabels)
	}
}
