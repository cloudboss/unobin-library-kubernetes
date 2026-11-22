package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func job() *JobResource {
	return &JobResource{
		Metadata: Metadata{Name: new("example")},
		Spec: JobSpec{Template: PodTemplate{
			Metadata: TemplateMetadata{},
			Spec:     &PodSpec{Container: new([]Container{{Name: "app", Image: new("busybox")}})},
		}},
	}
}

func TestJobDefaults(t *testing.T) {
	obj, err := job().expand(nil)
	require.NoError(t, err)
	require.Equal(t, "default", obj.Namespace)
	require.Equal(t, int32(1), *obj.Spec.Completions)
	require.Equal(t, int32(6), *obj.Spec.BackoffLimit)
	require.Equal(t, corev1.RestartPolicyNever, obj.Spec.Template.Spec.RestartPolicy)
}
