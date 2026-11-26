package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestStatefulSetPartitionOperationWidth(t *testing.T) {
	for _, value := range []int64{0, 3, -2, 4294967297, -4294967295} {
		t.Run(strconv.FormatInt(value, 10), func(t *testing.T) {
			r := statefulSet()
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new("RollingUpdate"),
				RollingUpdate: &StatefulSetRollingUpdate{
					Partition: new(value)}}
			obj, err := r.expand(nil)
			require.NoError(t, err)
			require.Equal(t, int32(value), *obj.Spec.UpdateStrategy.RollingUpdate.Partition)
			for _, insert := range []bool{false, true} {
				before := statefulSet()
				before.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new("RollingUpdate")}
				if !insert {
					before.Spec.UpdateStrategy.RollingUpdate = &StatefulSetRollingUpdate{Partition: new(int64(8))}
				}
				native, err := before.expand(nil)
				require.NoError(t, err)
				native.UID = "uid"
				observed, err := before.statefulSetOutput(native, "server", nil)
				require.NoError(t, err)
				next, err := r.statefulSetSpecMap(&observed.Spec)
				require.NoError(t, err)
				patch, err := statefulSetSpecPatch(before.Spec, observed.Spec, r.Spec, next)
				require.NoError(t, err)
				want := []patchOperation{{Op: "replace",
					Path: "/spec/updateStrategy/rollingUpdate/partition", Value: value}}
				if insert {
					want = []patchOperation{{Op: "add", Path: "/spec/updateStrategy/rollingUpdate",
						Value: map[string]any{}},
						{Op: "add", Path: "/spec/updateStrategy/rollingUpdate/partition", Value: value}}
				}
				require.Equal(t, want, patch)
				if value >= -2147483648 && value <= 2147483647 {
					updated := applyStatefulSetPatch(t, native, patch)
					require.Equal(t, int32(value), *updated.Spec.UpdateStrategy.RollingUpdate.Partition)
				}
			}
		})
	}
}

func TestStatefulSetWidePartitionHTTPRejection(t *testing.T) {
	for _, insert := range []bool{false, true} {
		for _, value := range []int64{4294967297, -4294967295, 4294967296, -4294967296} {
			t.Run(strconv.FormatBool(insert)+"/"+strconv.FormatInt(value, 10), func(t *testing.T) {
				before := statefulSet()
				before.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{}
				if !insert {
					before.Spec.UpdateStrategy.RollingUpdate = &StatefulSetRollingUpdate{}
				}
				obj, err := before.expand(nil)
				require.NoError(t, err)
				defaultStatefulSet(obj)
				if insert {
					obj.Spec.UpdateStrategy.RollingUpdate = nil
				}
				var calls []string
				c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method)
					if req.Method == http.MethodGet {
						require.NoError(t, json.NewEncoder(w).Encode(obj))
						return
					}
					require.Equal(t, "PATCH", req.Method)
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					var operations []struct {
						Path  string
						Value json.RawMessage
					}
					require.NoError(t, json.Unmarshal(body, &operations))
					require.Equal(t, "/spec/updateStrategy/rollingUpdate/partition",
						operations[len(operations)-1].Path)
					require.JSONEq(t, strconv.FormatInt(value, 10), string(operations[len(operations)-1].Value))
					apiError(t, w, 422)
				})
				observed, err := before.statefulSetOutput(obj, *c.Server, nil)
				require.NoError(t, err)
				next := statefulSet()
				next.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{
					Partition: new(value)}}
				_, err = next.Update(t.Context(), c, statefulSetPrior{Inputs: *before, Outputs: observed})
				require.Error(t, err)
				require.Equal(t, []string{"GET", "PATCH"}, calls)
			})
		}
	}
}

func TestStatefulSetRevisionOriginalWidth(t *testing.T) {
	before, after := statefulSet(), statefulSet()
	before.Spec.RevisionHistoryLimit = new(int64(1))
	after.Spec.RevisionHistoryLimit = new(int64(4294967297))
	obj, err := after.expand(nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), *obj.Spec.RevisionHistoryLimit)
	require.False(t, equivalentStatefulSetSpec(before.Spec, after.Spec))
	require.True(t, statefulSetReplacement(before.Spec, after.Spec))
	after.Spec.RevisionHistoryLimit = nil
	require.True(t, equivalentStatefulSetSpec(before.Spec, after.Spec))
	require.False(t, statefulSetReplacement(before.Spec, after.Spec))
}

func TestStatefulSetMountPropagationNativeDefaults(t *testing.T) {
	for _, mode := range []*corev1.MountPropagationMode{nil, new(corev1.MountPropagationNone),
		new(corev1.MountPropagationHostToContainer), new(corev1.MountPropagationBidirectional)} {
		name := "nil"
		want := "None"
		if mode != nil {
			name, want = string(*mode), string(*mode)
		}
		t.Run(name, func(t *testing.T) {
			r := statefulSet()
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			containers := []corev1.Container{{Name: "app", VolumeMounts: []corev1.VolumeMount{
				{Name: "kube-api-access-token", MountPath: "/token"},
				{Name: "data", MountPath: "/data", MountPropagation: mode}}}}
			obj.Spec.Template.Spec.Containers = containers
			obj.Spec.Template.Spec.InitContainers = containers
			out, err := r.statefulSetOutput(obj, "server", nil)
			require.NoError(t, err)
			wantContainers := []Container{{
				Name: "app", Image: new(""), Resources: &Resources{},
				Stdin: new(false), StdinOnce: new(false), TTY: new(false),
				TerminationMessagePath: new(""), WorkingDir: new(""),
				VolumeMount: new([]Mount{{Name: "data", MountPath: "/data",
					ReadOnly: new(false), SubPath: new(""), SubPathExpr: new(""),
					MountPropagation: &want}}),
			}}
			require.NotNil(t, out.Spec.Template.Spec.Container)
			require.Equal(t, wantContainers, *out.Spec.Template.Spec.Container)
			require.NotNil(t, out.Spec.Template.Spec.InitContainer)
			require.Equal(t, wantContainers, *out.Spec.Template.Spec.InitContainer)
			require.Nil(t, obj.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPropagation)
		})
	}
}

func TestStatefulSetPostWriteMetadataTracking(t *testing.T) {
	var stored *appsv1.StatefulSet
	writes := 0
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case "POST":
			stored = &appsv1.StatefulSet{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			defaultStatefulSet(stored)
		case "PATCH":
			writes++
			var operations []patchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			stored = applyStatefulSetPatch(t, stored, operations)
		case "GET":
		default:
			t.Fatalf("unexpected request %s", req.Method)
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	r := statefulSet()
	r.IgnoreAnnotations, r.IgnoreLabels = new([]string{"ignored"}), new([]string{"ignored"})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	prior := *r
	values := map[string]string{"kubernetes.io/new": "internal", "example.com/ignored": "ignored"}
	r.Metadata.Labels, r.Metadata.Annotations = &values, &values
	out, err = r.Update(t.Context(), c, statefulSetPrior{Inputs: prior, Outputs: out})
	require.NoError(t, err)
	require.NotNil(t, out.Metadata.Labels)
	require.NotNil(t, out.Metadata.Annotations)
	require.Equal(t, values, *out.Metadata.Labels)
	require.Equal(t, values, *out.Metadata.Annotations)
	stored.Labels["kubernetes.io/new"] = "current"
	stored.Annotations["example.com/ignored"] = "current"
	out, err = r.Read(t.Context(), c, statefulSetPrior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, "current", (*out.Metadata.Labels)["kubernetes.io/new"])
	require.Equal(t, "current", (*out.Metadata.Annotations)["example.com/ignored"])
	prior = *r
	r.Metadata.Labels, r.Metadata.Annotations = nil, nil
	out, err = r.Update(t.Context(), c, statefulSetPrior{Inputs: prior, Outputs: out})
	require.NoError(t, err)
	require.Empty(t, metadataMap(out.Metadata.Labels))
	require.Empty(t, metadataMap(out.Metadata.Annotations))
	require.Empty(t, stored.Labels)
	require.Empty(t, stored.Annotations)
	stored.Labels = map[string]string{"kubernetes.io/new": "untracked"}
	stored.Annotations = map[string]string{"example.com/ignored": "untracked"}
	out, err = r.Read(t.Context(), c, statefulSetPrior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Empty(t, metadataMap(out.Metadata.Labels))
	require.Empty(t, metadataMap(out.Metadata.Annotations))
	require.Equal(t, 2, writes)
}

func TestStatefulSetCreateWideIntegers(t *testing.T) {
	for _, partition := range []int64{4294967297, -4294967295} {
		t.Run(strconv.FormatInt(partition, 10), func(t *testing.T) {
			var stored *appsv1.StatefulSet
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "POST" {
					stored = &appsv1.StatefulSet{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
					require.Equal(t, int32(1), *stored.Spec.RevisionHistoryLimit)
					require.Equal(t, int32(1), *stored.Spec.UpdateStrategy.RollingUpdate.Partition)
					defaultStatefulSet(stored)
				} else {
					require.Equal(t, "GET", req.Method)
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			r := statefulSet()
			r.Spec.RevisionHistoryLimit = new(int64(4294967297))
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{
				Partition: new(partition)}}
			_, err := r.Create(t.Context(), c)
			require.NoError(t, err)
		})
	}
}
