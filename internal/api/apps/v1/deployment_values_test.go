package v1

import (
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestQuantityAndCollectionEquality(t *testing.T) {
	quantity := comparisonValue("1Gi", reflect.TypeFor[string](), "quantity", false)
	require.Equal(t, quantity,
		comparisonValue("1073741824", reflect.TypeFor[string](), "quantity", false))
	for _, tc := range []struct {
		name string
		set  func(*DeploymentSpec, bool)
	}{
		{"container quantity", func(s *DeploymentSpec, second bool) {
			value := "1Gi"
			if second {
				value = "1073741824"
			}
			(*s.Template.Spec.Container)[0].Resources = &Resources{
				Limits: new(map[string]string{"memory": value}),
			}
		}},
		{"claim quantity", func(s *DeploymentSpec, second bool) {
			value := "1Gi"
			if second {
				value = "1073741824"
			}
			claimSpec(s).Resources.Limits = new(map[string]string{"storage": value})
		}},
		{"selector set", func(s *DeploymentSpec, second bool) {
			s.Selector.MatchExpressions = new([]SelectorRequirement{{Operator: new("Exists")}})
			if second {
				(*s.Selector.MatchExpressions)[0].Values = new([]string{})
			}
		}},
		{"selector map", func(s *DeploymentSpec, second bool) {
			s.Selector.MatchLabels = nil
			if second {
				s.Selector.MatchLabels = new(map[string]string{})
			}
		}},
		{"selector list", func(s *DeploymentSpec, second bool) {
			s.Selector.MatchExpressions = nil
			if second {
				s.Selector.MatchExpressions = new([]SelectorRequirement{})
			}
		}},
		{"node fields", func(s *DeploymentSpec, second bool) {
			requiredNodeTerm(s).MatchFields = nil
			if second {
				requiredNodeTerm(s).MatchFields = new([]NodeField{})
			}
		}},
		{"claim limits", func(s *DeploymentSpec, second bool) {
			claimSpec(s).Resources.Limits = nil
			if second {
				claimSpec(s).Resources.Limits = new(map[string]string{})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := fullInput(t), fullInput(t)
			tc.set(&a.Spec, false)
			tc.set(&b.Spec, true)
			require.True(t, equivalentSpec(a.Spec, b.Spec))
			require.Equal(t, replacementValues(reflect.ValueOf(a.Spec), ""),
				replacementValues(reflect.ValueOf(b.Spec), ""))
		})
	}
	t.Run("optional object presence", func(t *testing.T) {
		a, b := deployment(), deployment()
		a.Spec.Selector = nil
		b.Spec.Selector = &Selector{}
		require.False(t, equivalentSpec(a.Spec, b.Spec))
		require.NotEqual(t, replacementValues(reflect.ValueOf(a.Spec), ""),
			replacementValues(reflect.ValueOf(b.Spec), ""))
	})
	t.Run("computed collection clear", func(t *testing.T) {
		a, b := deployment(), deployment()
		a.Spec.Template.Spec.ImagePullSecrets = nil
		b.Spec.Template.Spec.ImagePullSecrets = new([]NamedReference{})
		require.False(t, equivalentSpec(a.Spec, b.Spec))
		observed := a.Spec
		observed.Template.Spec.ImagePullSecrets = new([]NamedReference{{Name: "retained"}})
		retained, err := a.expand(&observed)
		require.NoError(t, err)
		cleared, err := b.expand(&observed)
		require.NoError(t, err)
		require.Equal(t, []corev1.LocalObjectReference{{Name: "retained"}},
			retained.Spec.Template.Spec.ImagePullSecrets)
		require.Empty(t, cleared.Spec.Template.Spec.ImagePullSecrets)
	})
}

func TestReadExactSignedIntegers(t *testing.T) {
	for _, number := range []int64{9007199254740993, math.MaxInt64, math.MinInt64} {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			obj := readyDeployment(t)
			pod := &obj.Spec.Template.Spec
			pod.Tolerations = []corev1.Toleration{{TolerationSeconds: &number}}
			pod.SecurityContext = &corev1.PodSecurityContext{
				RunAsUser: &number, SupplementalGroups: []int64{number},
			}
			pod.ActiveDeadlineSeconds = &number
			pod.Containers[0].SecurityContext = &corev1.SecurityContext{RunAsGroup: &number}
			out, err := deployment().output(obj, "server")
			require.NoError(t, err)
			got := out.Spec.Template.Spec
			require.Equal(t, strconv.FormatInt(number, 10), *(*got.Toleration)[0].TolerationSeconds)
			require.Equal(t, strconv.FormatInt(number, 10), *got.SecurityContext.RunAsUser)
			require.Equal(t, []int64{number}, *got.SecurityContext.SupplementalGroups)
			require.Equal(t, number, *got.ActiveDeadlineSeconds)
			require.Equal(t, strconv.FormatInt(number, 10),
				*(*got.Container)[0].SecurityContext.RunAsGroup)
		})
	}
}

func TestMetadataUpdateWithEquivalentQuantity(t *testing.T) {
	r := deployment()
	r.WaitForRollout = new(false)
	(*r.Spec.Template.Spec.Container)[0].Resources = &Resources{
		Requests: new(map[string]string{"memory": "1Gi"}),
	}
	stored, err := r.expand(nil)
	require.NoError(t, err)
	stored.UID = "uid"
	stored.Spec.Strategy.Type = "RollingUpdate"
	stored.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] =
		resource.MustParse("1073741824")
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PATCH" {
			var ops []patchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
			for _, op := range ops {
				require.NotEqual(t, "/spec", op.Path)
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	out, err := r.output(stored, *c.Server)
	require.NoError(t, err)
	p := prior{Inputs: *r, Outputs: out, Configuration: c}
	r.Metadata.Annotations = new(map[string]string{"changed": "yes"})
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
}

func TestObservedQuantityEquality(t *testing.T) {
	a, b := deployment(), deployment()
	(*a.Spec.Template.Spec.Container)[0].Resources = &Resources{
		Requests: new(map[string]string{"memory": "1Gi"}),
	}
	(*b.Spec.Template.Spec.Container)[0].Resources = &Resources{
		Requests: new(map[string]string{"memory": "1073741824"}),
	}
	a.Spec.Template.Spec.Toleration = new([]Toleration{{Effect: new("")}})
	b.Spec.Template.Spec.Toleration = new([]Toleration{{Effect: new("")}})
	require.True(t, equivalentObservedSpec(a.Spec, b.Spec))
}

func TestMetadataValidatorsAtEveryLocation(t *testing.T) {
	t.Run("shared label and annotation map", func(t *testing.T) {
		r := deployment()
		values := new(map[string]string{"Example.COM/key": "value"})
		r.Metadata.Labels, r.Metadata.Annotations = values, values
		_, err := r.expand(nil)
		require.Error(t, err)
	})
	for _, location := range []string{"outer", "template", "claim"} {
		for _, tc := range []struct {
			name, key, value  string
			annotation, valid bool
		}{
			{"uppercase annotation", "Example.COM/key", "any value", true, true},
			{"uppercase label", "Example.COM/key", "value", false, false},
			{"bad annotation", "bad key", "value", true, false},
			{"bad label key", "bad key", "value", false, false},
			{"bad label value", "example.com/key", "bad value", false, false},
		} {
			t.Run(location+"/"+tc.name, func(t *testing.T) {
				r := deployment()
				values := new(map[string]string{tc.key: tc.value})
				m := Metadata{}
				if tc.annotation {
					m.Annotations = values
				} else {
					m.Labels = values
				}
				switch location {
				case "outer":
					r.Metadata.Annotations, r.Metadata.Labels = m.Annotations, m.Labels
				case "template":
					r.Spec.Template.Metadata = m
				case "claim":
					r.Spec.Template.Spec.Volume = new([]Volume{{Ephemeral: &EphemeralVolume{
						VolumeClaimTemplate: VolumeClaimTemplate{
							Metadata: &ClaimMetadata{Annotations: m.Annotations, Labels: m.Labels},
							Spec:     ClaimSpec{AccessModes: []string{"ReadWriteOnce"}},
						},
					}}})
				}
				if !tc.valid {
					c := testAPI(t, func(_ http.ResponseWriter, req *http.Request) {
						t.Errorf("invalid metadata reached API: %s", req.Method)
					})
					_, err := r.Create(t.Context(), c)
					require.Error(t, err)
					return
				}
				obj, err := r.expand(nil)
				require.NoError(t, err)
				annotations := obj.Annotations
				switch location {
				case "template":
					annotations = obj.Spec.Template.Annotations
				case "claim":
					annotations = obj.Spec.Template.Spec.Volumes[0].Ephemeral.VolumeClaimTemplate.Annotations
				}
				require.Equal(t, map[string]string{tc.key: tc.value}, annotations)
			})
		}
	}
}

func TestDownwardItemPathValidation(t *testing.T) {
	for _, projected := range []bool{false, true} {
		for _, value := range []string{
			"/absolute", "../parent", "dir/../parent", "..prefix", "dir/file",
		} {
			t.Run(strconv.FormatBool(projected)+"/"+value, func(t *testing.T) {
				r := deployment()
				volume := Volume{DownwardAPI: &DownwardAPIVolume{
					Items: new([]DownwardItem{{Path: value}}),
				}}
				if projected {
					volume = Volume{Projected: &ProjectedVolume{Sources: []ProjectionGroup{{
						DownwardAPI: &DownwardAPIProjection{
							Items: new([]ProjectedDownwardItem{{Path: value}}),
						},
					}}}}
				}
				r.Spec.Template.Spec.Volume = new([]Volume{volume})
				_, err := r.expand(nil)
				if value == "dir/file" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}

func TestNestedInt32Casts(t *testing.T) {
	const large = int64(4294967296)
	for _, value := range []any{
		DeploymentSpec{MinReadySeconds: new(large), ProgressDeadlineSeconds: new(large),
			RevisionHistoryLimit: new(large)},
		Probe{FailureThreshold: new(large), InitialDelaySeconds: new(large),
			PeriodSeconds: new(large), SuccessThreshold: new(large), TimeoutSeconds: new(large)},
		PreferredSchedulingTerm{Weight: large}, WeightedPodAffinityTerm{Weight: large},
		TopologySpreadConstraint{MaxSkew: new(large), MinDomains: new(large)},
		AWSElasticBlockStoreVolume{Partition: new(large)},
		GCEPersistentDiskVolume{Partition: new(large)},
		FCVolume{LUN: large}, ISCSIVolume{LUN: new(large)},
	} {
		t.Run(reflect.TypeOf(value).Name(), func(t *testing.T) {
			got, err := expandValue(reflect.ValueOf(value), reflect.Value{}, "test")
			require.NoError(t, err)
			for key, number := range got.(map[string]any) {
				if _, ok := number.(int64); ok {
					require.Equal(t, int64(0), number, key)
				}
			}
		})
	}
	r := deployment()
	(*r.Spec.Template.Spec.Container)[0].LivenessProbe = &Probe{InitialDelaySeconds: new(large)}
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Zero(t, obj.Spec.Template.Spec.Containers[0].LivenessProbe.InitialDelaySeconds)
	for _, value := range []any{
		Probe{FailureThreshold: new(int64(0))}, Probe{PeriodSeconds: new(int64(-1))},
		TopologySpreadConstraint{MaxSkew: new(int64(0))},
		GRPC{Port: large}, Port{ContainerPort: large}, Port{ContainerPort: 80, HostPort: new(large)},
	} {
		_, err := expandValue(reflect.ValueOf(value), reflect.Value{}, "test")
		require.Error(t, err)
	}
	for _, value := range []any{
		GRPC{Port: 65535}, Port{ContainerPort: 65535, HostPort: new(int64(1))},
	} {
		_, err := expandValue(reflect.ValueOf(value), reflect.Value{}, "test")
		require.NoError(t, err)
	}
}

func TestTokenSubstringAndRegexFiltering(t *testing.T) {
	for _, tc := range []struct{ account, token string }{
		{"", "extra-default-token-abcde-suffix"},
		{"custom.account", "extra-customXaccount-token-abcde-suffix"},
		{"custom", "kube-api-access-anything"},
	} {
		t.Run(tc.token, func(t *testing.T) {
			obj := readyDeployment(t)
			pod := &obj.Spec.Template.Spec
			pod.ServiceAccountName = tc.account
			pod.Volumes = []corev1.Volume{{Name: "before"}, {Name: tc.token},
				{Name: tc.token + "-second"}, {Name: "after"}}
			mounts := []corev1.VolumeMount{{Name: "before"}, {Name: tc.token},
				{Name: tc.token + "-second"}, {Name: "after"}}
			pod.Containers[0].VolumeMounts = mounts
			pod.InitContainers = []corev1.Container{{Name: "init", VolumeMounts: mounts}}
			out, err := deployment().output(obj, "server")
			require.NoError(t, err)
			want := []string{"before", tc.token + "-second", "after"}
			var volumes []string
			for _, volume := range *out.Spec.Template.Spec.Volume {
				volumes = append(volumes, *volume.Name)
			}
			require.Equal(t, want, volumes)
			for _, containers := range []*[]Container{
				out.Spec.Template.Spec.Container, out.Spec.Template.Spec.InitContainer,
			} {
				var names []string
				for _, mount := range *(*containers)[0].VolumeMount {
					names = append(names, mount.Name)
				}
				require.Equal(t, want, names)
			}
		})
	}
}

func TestAzureDiskKindLifecycle(t *testing.T) {
	r := deployment()
	r.WaitForRollout = new(false)
	r.Spec.Template.Spec.Volume = new([]Volume{{Name: new("disk"), AzureDisk: &AzureDiskVolume{
		CachingMode: "None", DataDiskURI: "disk-uri", DiskName: "disk-name",
	}}})
	stored := readyDeployment(t)
	var patches [][]patchOperation
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case "POST":
			stored = &appsv1.Deployment{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			require.Nil(t, stored.Spec.Template.Spec.Volumes[0].AzureDisk.Kind)
			stored.UID = "uid"
			stored.Spec.Strategy.Type = "RollingUpdate"
			stored.Spec.Template.Spec.Volumes[0].AzureDisk.Kind = new(corev1.AzureManagedDisk)
		case "PATCH":
			var ops []patchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
			patches = append(patches, ops)
			encoded, err := json.Marshal(ops)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(encoded)
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			after, err := patch.Apply(before)
			require.NoError(t, err)
			stored = &appsv1.Deployment{}
			require.NoError(t, json.Unmarshal(after, stored))
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	for _, kind := range []*string{nil, new("")} {
		(*r.Spec.Template.Spec.Volume)[0].AzureDisk.Kind = kind
		out, err := r.Create(t.Context(), c)
		require.NoError(t, err)
		require.Equal(t, "Managed", *(*out.Spec.Template.Spec.Volume)[0].AzureDisk.Kind)
	}
	(*r.Spec.Template.Spec.Volume)[0].AzureDisk.Kind = nil
	out, err := r.output(stored, *c.Server)
	require.NoError(t, err)
	p := prior{Inputs: *r, Outputs: out, Configuration: c}
	r.Spec.MinReadySeconds = new(int64(2))
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.NotNil(t, stored.Spec.Template.Spec.Volumes[0].AzureDisk.Kind)
	require.Equal(t, corev1.AzureManagedDisk, *stored.Spec.Template.Spec.Volumes[0].AzureDisk.Kind)
	p = prior{Inputs: *r, Outputs: out, Configuration: c}
	r.Metadata.Annotations = new(map[string]string{"changed": "yes"})
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	for _, operation := range patches[1] {
		require.NotEqual(t, "/spec", operation.Path)
	}
	(*r.Spec.Template.Spec.Volume)[0].AzureDisk.Kind = new("")
	obj, err := r.expand(&out.Spec)
	require.NoError(t, err)
	require.Nil(t, obj.Spec.Template.Spec.Volumes[0].AzureDisk.Kind)
}
