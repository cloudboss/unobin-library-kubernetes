//go:build integration

package pod_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	pod "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestPodDeadlineAPIBoundaries(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	pods := api.CoreV1().Pods("default")
	for _, test := range []struct {
		name          string
		before, after *int64
	}{
		{"shorten", new(int64(3600)), new(int64(1800))},
		{"absent-to-set", nil, new(int64(1800))},
		{"remove", new(int64(3600)), nil},
		{"increase", new(int64(1800)), new(int64(3600))},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			nodeSelector := map[string]string{"test.unobin.cloudboss.io/unscheduled": "true"}
			r := pod.PodResource{
				Metadata: pod.ServiceMetadata{GenerateName: new("unobin-it-pod-deadline-")},
				Spec: pod.PodSpec{
					ActiveDeadlineSeconds: test.before,
					Container: &[]pod.PodContainer{{
						Name: "app", Image: new("registry.k8s.io/pause:3.9"),
					}},
					NodeSelector:                  &nodeSelector,
					TerminationGracePeriodSeconds: new(int64(0)),
				},
				TargetState: &[]string{"Pending"},
			}
			first, err := r.Create(ctx, nil)
			require.NoError(t, err)
			prior := runtime.Prior[pod.PodResource, *pod.PodResourceOutput, *config.Configuration]{
				Inputs: r, Outputs: first, Observed: first,
			}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				require.NoError(t, r.Delete(cleanup, nil, prior))
			})
			// Compare the result with the pinned API's exact replace operation.
			probe, err := pods.Create(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "unobin-it-pod-deadline-probe-"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app", Image: "registry.k8s.io/pause:3.9",
					}},
					NodeSelector:                  *r.Spec.NodeSelector,
					ActiveDeadlineSeconds:         test.before,
					TerminationGracePeriodSeconds: new(int64(0)),
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				require.NoError(t, pods.Delete(cleanup, probe.Name, metav1.DeleteOptions{}))
			})
			value := int64(0)
			if test.after != nil {
				value = *test.after
			}
			patch, err := json.Marshal([]map[string]any{{
				"op": "replace", "path": "/spec/activeDeadlineSeconds", "value": value,
			}})
			require.NoError(t, err)
			_, apiErr := pods.Patch(ctx, probe.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
			if test.name == "shorten" {
				require.NoError(t, apiErr)
			}
			r.Spec.ActiveDeadlineSeconds = test.after
			next, err := r.Update(ctx, nil, prior)
			expected := test.after
			if apiErr == nil {
				require.NoError(t, err)
				require.Equal(t, first.UID, next.UID)
				expected = new(value)
				t.Logf("pinned API accepted the deadline %d", value)
			} else {
				require.Error(t, err)
				require.Equal(t, apierrors.ReasonForError(apiErr), apierrors.ReasonForError(err))
				t.Logf("pinned API rejected the deadline %d: %v", value, apiErr)
				expected = test.before
			}
			live, err := pods.Get(ctx, first.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, first.UID, string(live.UID))
			require.Equal(t, expected, live.Spec.ActiveDeadlineSeconds)
		})
	}
}
