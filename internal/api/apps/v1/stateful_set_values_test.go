package v1

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func statefulSetClaim(name string) StatefulSetClaimTemplate {
	return StatefulSetClaimTemplate{Metadata: Metadata{Name: &name}, Spec: ClaimSpec{
		AccessModes: []string{"ReadWriteOnce"}, Resources: ClaimResources{Requests: new(
			map[string]string{"storage": "1Gi"})}}}
}

func TestStatefulSetExpansionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*StatefulSetResource)
		check  func(*testing.T, *appsv1.StatefulSet)
	}{
		{"optional pod spec", func(r *StatefulSetResource) { r.Spec.Template.Spec = nil },
			func(t *testing.T, o *appsv1.StatefulSet) { require.Empty(t, o.Spec.Template.Spec.Containers) }},
		{"namespace locations", func(r *StatefulSetResource) {
			r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{statefulSetClaim("data"),
				statefulSetClaim("logs")})
		}, func(t *testing.T, o *appsv1.StatefulSet) {
			require.Equal(t, "default", o.Namespace)
			require.Empty(t, o.Spec.Template.Namespace)
			require.Equal(t, []string{"data", "logs"}, []string{o.Spec.VolumeClaimTemplates[0].Name,
				o.Spec.VolumeClaimTemplates[1].Name})
			require.Equal(t, "default", o.Spec.VolumeClaimTemplates[0].Namespace)
		}},
		{"empty namespace", func(r *StatefulSetResource) {
			r.Metadata.Namespace = new("")
			claim := statefulSetClaim("data")
			claim.Metadata.Namespace = new("")
			r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{claim})
		}, func(t *testing.T, o *appsv1.StatefulSet) {
			require.Empty(t, o.Namespace)
			require.Empty(t, o.Spec.VolumeClaimTemplates[0].Namespace)
		}},
		{"claims min ready", func(r *StatefulSetResource) {
			r.Spec.MinReadySeconds = new(int64(10))
			r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{statefulSetClaim("data")})
		}, func(t *testing.T, o *appsv1.StatefulSet) {
			require.Equal(t, int32(10),
				o.Spec.MinReadySeconds)
		}},
		{"present strategy", func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{}}
		}, func(t *testing.T, o *appsv1.StatefulSet) {
			require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, o.Spec.UpdateStrategy.Type)
			require.Zero(t, *o.Spec.UpdateStrategy.RollingUpdate.Partition)
		}},
		{"negative partition OnDelete", func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new("OnDelete"),
				RollingUpdate: &StatefulSetRollingUpdate{Partition: new(int64(-1))}}
		}, func(t *testing.T, o *appsv1.StatefulSet) {
			require.Equal(t, int32(-1), *o.Spec.UpdateStrategy.RollingUpdate.Partition)
		}},
		{"present retention", func(r *StatefulSetResource) {
			r.Spec.PersistentVolumeClaimRetentionPolicy = &StatefulSetClaimRetentionPolicy{}
		}, func(t *testing.T, o *appsv1.StatefulSet) {
			require.Equal(t, appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
				o.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted)
			require.Equal(t, appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
				o.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled)
		}},
		{"claim defaults", func(r *StatefulSetResource) {
			claim := statefulSetClaim("data")
			claim.Spec.StorageClassName = new("")
			claim.Spec.AccessModes = []string{"ReadWriteOnce", "ReadOnlyMany", "ReadWriteOnce"}
			r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{claim})
		}, func(t *testing.T, o *appsv1.StatefulSet) {
			require.Nil(t, o.Spec.VolumeClaimTemplates[0].Spec.StorageClassName)
			require.Equal(t, []corev1.PersistentVolumeAccessMode{"ReadOnlyMany", "ReadWriteOnce"},
				o.Spec.VolumeClaimTemplates[0].Spec.AccessModes)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := statefulSet()
			tc.change(r)
			o, err := r.expand(nil)
			require.NoError(t, err)
			tc.check(t, o)
		})
	}
	for _, policy := range []string{"Always", "OnFailure", "Never"} {
		r := statefulSet()
		r.Spec.Template.Spec.RestartPolicy = &policy
		o, err := r.expand(nil)
		require.NoError(t, err)
		require.Equal(t, corev1.RestartPolicy(policy), o.Spec.Template.Spec.RestartPolicy)
	}
	for _, text := range []string{"", "0", "+01", "-1", "2147483647", "-2147483648"} {
		r := statefulSet()
		r.Spec.Replicas = &text
		o, err := r.expand(nil)
		require.NoError(t, err)
		if text == "" {
			require.Nil(t, o.Spec.Replicas)
		}
	}
	for _, text := range []string{"2147483648", "-2147483649", "9223372036854775807",
		"-9223372036854775808"} {
		r := statefulSet()
		r.Spec.Replicas = &text
		_, err := r.statefulSetSpecMap(nil)
		require.NoError(t, err)
		_, err = r.expand(nil)
		require.Error(t, err)
	}
	for _, text := range []string{"bad", "9223372036854775808"} {
		r := statefulSet()
		r.Spec.Replicas = &text
		_, err := r.statefulSetSpecMap(nil)
		require.Error(t, err)
	}
}

func TestStatefulSetValidationBeforeCasts(t *testing.T) {
	for _, change := range []func(*StatefulSetResource){
		func(r *StatefulSetResource) { r.Spec.RevisionHistoryLimit = new(int64(0)) },
		func(r *StatefulSetResource) { r.Spec.RevisionHistoryLimit = new(int64(-4294967295)) },
		func(r *StatefulSetResource) { r.Spec.MinReadySeconds = new(int64(-4294967295)) },
		func(r *StatefulSetResource) {
			r.Spec.Template.Spec.TerminationGracePeriodSeconds = new(int64(-4294967295))
		},
		func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Port = new([]Port{{ContainerPort: 4294967297}})
		},
		func(r *StatefulSetResource) { r.Spec.Template.Spec.RestartPolicy = new("Sometimes") },
		func(r *StatefulSetResource) { r.Spec.PodManagementPolicy = new("Other") },
		func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new(
				"Other")}
		},
		func(r *StatefulSetResource) {
			r.Spec.PersistentVolumeClaimRetentionPolicy = &StatefulSetClaimRetentionPolicy{
				WhenDeleted: new("Other")}
		},
		func(r *StatefulSetResource) { r.IgnoreLabels = new([]string{"["}) },
	} {
		r := statefulSet()
		change(r)
		require.NotEmpty(t, checkNativeStatefulSetInputs(t, r))
	}
	for _, field := range []string{"CreateTimeout", "ReadTimeout", "UpdateTimeout", "DeleteTimeout"} {
		for _, value := range []string{"0", "-1s", "bad"} {
			r := statefulSet()
			reflect.ValueOf(r).Elem().FieldByName(field).Set(reflect.ValueOf(&value))
			require.NotEmpty(t, checkNativeStatefulSetInputs(t, r))
		}
	}
	for _, location := range []string{"outer", "template", "claim"} {
		for _, metadata := range []Metadata{{Name: new("INVALID")}, {GenerateName: new(
			"bad.prefix-")}, {Name: new("a"), GenerateName: new("b-")}, {Labels: new(map[string]string{
			"bad key": "x"})}, {Labels: new(map[string]string{"key": "bad value"})}} {
			r := statefulSet()
			switch location {
			case "outer":
				r.Metadata = metadata
			case "template":
				r.Spec.Template.Metadata = metadata
			case "claim":
				claim := statefulSetClaim("data")
				claim.Metadata = metadata
				r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{claim})
			}
			require.NotEmpty(t, checkNativeStatefulSetInputs(t, r), location)
		}
	}
	r := statefulSet()
	r.Metadata.Annotations = new(map[string]string{"example.com/UPPER": ""})
	_, err := r.expand(nil)
	require.NoError(t, err)
	r.Spec.RevisionHistoryLimit = new(int64(4294967297))
	o, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), *o.Spec.RevisionHistoryLimit)
}

func TestStatefulSetOuterMetadata(t *testing.T) {
	values := map[string]string{
		"app.kubernetes.io/deprecated.daemonset.template.generation":          "exception",
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation": "exception",
		"notapp.kubernetes.io/hidden":                                         "hidden",
		"kubernetes.io/internal":                                              "hidden",
		"prefixkubernetes.io/key":                                             "hidden",
		"hostless":                                                            "kept",
		"bad%zz/key":                                                          "kept",
		"custom/deprecated.daemonset.template.generation":                     "hidden",
		"app.kubernetes.io/regex":                                             "regex",
	}
	for _, tracked := range []bool{false, true} {
		for _, patterns := range [][]string{nil, {"regex"}, {"deprecated"}} {
			got := maps.Clone(values)
			var baseline *map[string]string
			if tracked {
				baseline = &values
			}
			filterStatefulSetMetadata(got, baseline, &patterns)
			if tracked {
				require.Equal(t, values, got)
				continue
			}
			want := map[string]string{"hostless": "kept", "bad%zz/key": "kept"}
			if len(patterns) == 0 || patterns[0] != "deprecated" {
				want["app.kubernetes.io/deprecated.daemonset.template.generation"] = "exception"
				want["service.beta.kubernetes.io/deprecated.daemonset.template.generation"] = "exception"
			}
			if len(patterns) == 0 || patterns[0] != "regex" {
				want["app.kubernetes.io/regex"] = "regex"
			}
			require.Equal(t, want, got)
		}
	}
	r := statefulSet()
	r.IgnoreAnnotations = new([]string{"ignore"})
	r.IgnoreLabels = r.IgnoreAnnotations
	o, err := r.expand(nil)
	require.NoError(t, err)
	o.UID = "uid"
	o.Labels = map[string]string{"kubernetes.io/key": "current", "ignore": "hidden"}
	o.Annotations = maps.Clone(o.Labels)
	tracked := Metadata{Labels: new(map[string]string{"kubernetes.io/key": "old",
		"missing": "old"}), Annotations: new(map[string]string{"kubernetes.io/key": "old"})}
	o.Spec.Template.Labels = maps.Clone(o.Labels)
	o.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{
		Labels: maps.Clone(o.Labels)}}}
	out, err := r.statefulSetOutput(o, "server", &tracked)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"kubernetes.io/key": "current"}, *out.Metadata.Labels)
	require.Equal(t, *out.Metadata.Labels, *out.Metadata.Annotations)
	require.Equal(t, o.Spec.Template.Labels, *out.Spec.Template.Metadata.Labels)
	require.Equal(t, o.Spec.VolumeClaimTemplates[0].Labels,
		*(*out.Spec.VolumeClaimTemplate)[0].Metadata.Labels)
}

func fullStatefulSet(t *testing.T) *StatefulSetResource {
	t.Helper()
	data, err := os.ReadFile("testdata/ub/stateful-set/full/input.ub")
	require.NoError(t, err)
	var r StatefulSetResource
	require.NoError(t, ub.Unmarshal(data, &r))
	return &r
}

func TestStatefulSetFullConversion(t *testing.T) {
	r := fullStatefulSet(t)
	o, err := r.expand(nil)
	require.NoError(t, err)
	o.UID = "uid"
	out, err := r.statefulSetOutput(o, "server", nil)
	require.NoError(t, err)
	require.Equal(t, "2", *out.Spec.Replicas)
	require.Equal(t, "0644", *(*out.Spec.Template.Spec.Volume)[0].ConfigMap.DefaultMode)
	require.Nil(t, (*out.Spec.Template.Spec.Volume)[0].Local)
	require.Nil(t, (*out.Spec.Template.Spec.Volume)[0].CephFS)
	require.Nil(t, (*out.Spec.Template.Spec.Volume)[0].RBD.SecretRef.Namespace)
	require.Nil(t, (*out.Spec.Template.Spec.Volume)[0].AzureFile.SecretNamespace)
	source := (*r.Spec.Template.Spec.Volume)[0].Projected.Sources[0]
	configMap := (*source.ConfigMap)[0]
	require.Equal(t, []ProjectionGroup{
		{Secret: source.Secret},
		{ConfigMap: new([]ConfigMapProjection{{Name: configMap.Name, Items: configMap.Items}})},
		{DownwardAPI: source.DownwardAPI},
		{ServiceAccountToken: source.ServiceAccountToken},
	}, (*out.Spec.Template.Spec.Volume)[0].Projected.Sources)
	want, err := r.statefulSetSpecMap(nil)
	require.NoError(t, err)
	actual, err := nativeMap(o.Spec)
	require.NoError(t, err)
	canonical, err := nativeMap(want)
	require.NoError(t, err)
	assertNativeMembers(t, canonical, actual, "spec")
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "resourceVersion")
	require.NotContains(t, string(encoded), "observedGeneration")
}

func TestStatefulSetTokenNormalization(t *testing.T) {
	r := statefulSet()
	obj, err := r.expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	pod := &obj.Spec.Template.Spec
	pod.ServiceAccountName = "a.b"
	names := []string{"ordinary", "prefix-aXb-token-abc12-suffix", "a.b-token-def34",
		"kube-api-access-more"}
	mounts := []corev1.VolumeMount{}
	for _, name := range names {
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: name})
		mounts = append(mounts, corev1.VolumeMount{Name: name})
	}
	pod.Containers[0].VolumeMounts = mounts
	pod.InitContainers = []corev1.Container{{Name: "init", VolumeMounts: mounts}}
	pod.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready", Operator: "Exists"}}
	out, err := r.statefulSetOutput(obj, "server", nil)
	require.NoError(t, err)
	want := []string{"ordinary", "a.b-token-def34", "kube-api-access-more"}
	var volumes []string
	for _, volume := range *out.Spec.Template.Spec.Volume {
		volumes = append(volumes, *volume.Name)
	}
	require.Equal(t, want, volumes)
	for _, containers := range []*[]Container{out.Spec.Template.Spec.Container,
		out.Spec.Template.Spec.InitContainer} {
		var got []string
		for _, mount := range *(*containers)[0].VolumeMount {
			got = append(got, mount.Name)
		}
		require.Equal(t, want, got)
	}
	require.Equal(t, "node.kubernetes.io/not-ready", *(*out.Spec.Template.Spec.Toleration)[0].Key)
}
