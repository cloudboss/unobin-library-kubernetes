package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
)

func TestMetadataJSONPatch(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		before, desired, actual, want map[string]string
	}{
		{"whole map", nil, map[string]string{"new": "value"},
			map[string]string{"internal": "gone"}, map[string]string{"new": "value"}},
		{"escaped keys", map[string]string{"a/b~c": "old", "remove": "gone", "keep": "same"},
			map[string]string{"a/b~c": "changed", "keep": "same", "new": ""}, nil,
			map[string]string{"a/b~c": "changed", "keep": "same", "new": ""}},
		{"clear", map[string]string{"one": "value"}, nil, nil, map[string]string{}},
		{"unchanged", map[string]string{"one": "value"}, map[string]string{"one": "value"},
			nil, map[string]string{"one": "value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := tc.actual
			if actual == nil {
				actual = tc.before
			}
			body, err := json.Marshal(map[string]any{"metadata": map[string]any{"labels": actual}})
			require.NoError(t, err)
			ops := mapPatch("/metadata/labels", &tc.before, &tc.desired)
			if len(ops) > 0 {
				encoded, err := json.Marshal(ops)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(encoded)
				require.NoError(t, err)
				body, err = patch.Apply(body)
				require.NoError(t, err)
			}
			var result struct {
				Metadata struct{ Labels map[string]string }
			}
			require.NoError(t, json.Unmarshal(body, &result))
			require.Equal(t, tc.want, result.Metadata.Labels)
		})
	}
}

func TestReadMetadataAndTokenFiltering(t *testing.T) {
	r := deployment()
	r.Metadata.Labels = new(map[string]string{"custom.kubernetes.io/explicit": "kept"})
	r.IgnoreLabels = new([]string{"ignored"})
	obj := readyDeployment(t)
	obj.Labels = map[string]string{
		"custom.kubernetes.io/hidden": "no", "custom.kubernetes.io/explicit": "kept",
		"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/thing": "value",
		"deprecated.daemonset.template.generation": "hidden", "external": "visible",
		"ignored.example/key": "hidden", "somekubernetes.io/key": "hidden",
	}
	obj.Spec.Template.Annotations = map[string]string{"custom.kubernetes.io/template": "kept"}
	pod := &obj.Spec.Template.Spec
	pod.ServiceAccountName = "custom"
	pod.Volumes = []corev1.Volume{
		{Name: "ordinary"}, {Name: "custom-token-abc12"}, {Name: "custom-token-def34"},
		{Name: "kube-api-access-123"},
	}
	mounts := []corev1.VolumeMount{{Name: "ordinary"}, {Name: "kube-api-access-123"},
		{Name: "custom-token-abc12"}, {Name: "custom-token-def34"}}
	pod.Containers[0].VolumeMounts = mounts
	pod.InitContainers = []corev1.Container{{Name: "init", VolumeMounts: mounts}}
	pod.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready", Operator: "Exists"}}
	out, err := r.output(obj, "server")
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"custom.kubernetes.io/explicit": "kept", "app.kubernetes.io/name": "app",
		"service.beta.kubernetes.io/thing": "value", "external": "visible",
	}, *out.Metadata.Labels)
	require.Equal(t, map[string]string{"custom.kubernetes.io/template": "kept"},
		*out.Spec.Template.Metadata.Annotations)
	volumes := *out.Spec.Template.Spec.Volume
	require.Equal(t, []string{"ordinary", "custom-token-def34", "kube-api-access-123"},
		[]string{*volumes[0].Name, *volumes[1].Name, *volumes[2].Name})
	for _, containers := range []*[]Container{
		out.Spec.Template.Spec.Container, out.Spec.Template.Spec.InitContainer,
	} {
		mounts := *(*containers)[0].VolumeMount
		require.Equal(t, []string{"ordinary", "custom-token-abc12", "custom-token-def34"},
			[]string{mounts[0].Name, mounts[1].Name, mounts[2].Name})
	}
	require.Equal(t, "node.kubernetes.io/not-ready", *(*out.Spec.Template.Spec.Toleration)[0].Key)
	obj.Generation++
	obj.ResourceVersion = "volatile"
	obj.Status.ObservedGeneration++
	next, err := r.output(obj, "server")
	require.NoError(t, err)
	require.Equal(t, out, next)
}
