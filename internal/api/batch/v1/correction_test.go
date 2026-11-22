package v1

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestFailurePolicyContainerName(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input, want *string
	}{
		{name: "omitted"},
		{name: "empty", input: new("")},
		{name: "nonempty", input: new("app"), want: new("app")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := job()
			r.WaitForCompletion = new(false)
			r.Spec.PodFailurePolicy = &PodFailurePolicy{Rule: []PodFailureRule{{
				OnExitCodes: &ExitCodes{ContainerName: tc.input, Values: []int64{1}},
			}}}
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, "POST", req.Method)
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				policy := body["spec"].(map[string]any)["podFailurePolicy"].(map[string]any)
				rule := policy["rules"].([]any)[0].(map[string]any)
				matcher := rule["onExitCodes"].(map[string]any)
				if tc.want == nil {
					require.NotContains(t, matcher, "containerName")
				} else {
					require.Equal(t, *tc.want, matcher["containerName"])
				}
				body["metadata"].(map[string]any)["uid"] = "uid"
				require.NoError(t, json.NewEncoder(w).Encode(body))
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, tc.want, out.Spec.PodFailurePolicy.Rule[0].OnExitCodes.ContainerName)
			obj := readyJob(t)
			obj.Spec.PodFailurePolicy = &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
				OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
					ContainerName: tc.input, Values: []int32{1},
				},
			}}}
			original := obj.DeepCopy()
			out, err = r.output(obj, "server")
			require.NoError(t, err)
			require.Equal(t, tc.want, out.Spec.PodFailurePolicy.Rule[0].OnExitCodes.ContainerName)
			require.Equal(t, original, obj)
		})
	}
	a, b := job(), job()
	a.Spec.PodFailurePolicy = &PodFailurePolicy{Rule: []PodFailureRule{{
		OnExitCodes: &ExitCodes{Values: []int64{1}},
	}}}
	b.Spec.PodFailurePolicy = &PodFailurePolicy{Rule: []PodFailureRule{{
		OnExitCodes: &ExitCodes{ContainerName: new(""), Values: []int64{1}},
	}}}
	require.True(t, equivalentSpec(a.Spec, b.Spec))
	require.True(t, equivalentSpec(b.Spec, a.Spec))
	require.False(t, immutableChanged(a.Spec, b.Spec))
	require.False(t, immutableChanged(b.Spec, a.Spec))
	for _, pair := range [][2]*JobResource{{a, b}, {b, a}} {
		stored, err := pair[0].expand(nil)
		require.NoError(t, err)
		stored.UID = "uid"
		var patches [][]patchOperation
		c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
			if req.Method == "PATCH" {
				patches = append(patches, patchJob(t, req, stored))
			}
			require.NoError(t, json.NewEncoder(w).Encode(stored))
		})
		out, err := pair[0].output(stored, *c.Server)
		require.NoError(t, err)
		desired := *pair[1]
		desired.WaitForCompletion = new(false)
		desired.Spec.Parallelism = new(int64(2))
		out, err = desired.Update(t.Context(), c, prior{Inputs: *pair[0], Outputs: out})
		require.NoError(t, err)
		require.Equal(t, "uid", out.UID)
		require.Equal(t, [][]patchOperation{{
			{Op: "test", Path: "/metadata/uid", Value: "uid"},
			{Op: "replace", Path: "/spec/parallelism", Value: float64(2)},
		}}, patches)
	}
	b.Spec.PodFailurePolicy.Rule[0].OnExitCodes.ContainerName = new("app")
	require.True(t, immutableChanged(a.Spec, b.Spec))
}

func TestReadMountPropagation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input *corev1.MountPropagationMode
		want  string
	}{
		{name: "omitted", want: "None"},
		{name: "none", input: new(corev1.MountPropagationNone), want: "None"},
		{name: "host", input: new(corev1.MountPropagationHostToContainer), want: "HostToContainer"},
		{name: "bidirectional", input: new(corev1.MountPropagationBidirectional), want: "Bidirectional"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := job()
			containers := []Container{{Name: "app", VolumeMount: new([]Mount{{
				Name: "data", MountPath: "/data",
			}})}}
			r.Spec.Template.Spec.Container = &containers
			r.Spec.Template.Spec.InitContainer = &containers
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			for _, list := range [][]corev1.Container{
				obj.Spec.Template.Spec.Containers, obj.Spec.Template.Spec.InitContainers,
			} {
				list[0].VolumeMounts[0].MountPropagation = tc.input
			}
			original := obj.DeepCopy()
			projected, err := r.output(obj, "server")
			require.NoError(t, err)
			require.Equal(t, original, obj)
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, "GET", req.Method)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			projected.Server = *c.Server
			out, err := r.Read(t.Context(), c, prior{Inputs: *r, Outputs: projected})
			require.NoError(t, err)
			for _, result := range []*JobResourceOutput{projected, out} {
				for _, list := range []*[]Container{
					result.Spec.Template.Spec.Container, result.Spec.Template.Spec.InitContainer,
				} {
					require.Equal(t, tc.want, *(*(*list)[0].VolumeMount)[0].MountPropagation)
				}
			}
			for _, list := range []*[]Container{
				r.Spec.Template.Spec.Container, r.Spec.Template.Spec.InitContainer,
			} {
				(*(*list)[0].VolumeMount)[0].MountPropagation = new(tc.want)
			}
			baseline := job()
			baseline.Spec.Template.Spec.Container = new([]Container{{
				Name: "app", VolumeMount: (*projected.Spec.Template.Spec.Container)[0].VolumeMount,
			}})
			baseline.Spec.Template.Spec.InitContainer = new([]Container{{
				Name: "app", VolumeMount: (*projected.Spec.Template.Spec.InitContainer)[0].VolumeMount,
			}})
			require.True(t, equivalentSpec(baseline.Spec, r.Spec))
			require.False(t, immutableChanged(baseline.Spec, r.Spec))
		})
	}
}
