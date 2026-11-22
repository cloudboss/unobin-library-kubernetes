package v1

import (
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func statefulSet() *StatefulSetResource {
	return &StatefulSetResource{Metadata: Metadata{Name: new("example")}, Spec: StatefulSetSpec{
		ServiceName: "example", Selector: Selector{MatchLabels: new(map[string]string{"app": "example"})},
		Template: StatefulSetPodTemplate{Metadata: Metadata{Labels: new(map[string]string{
			"app": "example"})}, Spec: &StatefulSetPodSpec{Container: new([]Container{{Name: "app",
			Image: new("nginx")}})}},
	}, WaitForRollout: new(false)}
}
func TestStatefulSetCreateDefaults(t *testing.T) {
	r := statefulSet()
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, "default", obj.Namespace)
	require.Nil(t, obj.Spec.Replicas)
	require.Equal(t, int32(0), *obj.Spec.RevisionHistoryLimit)
	require.NotNil(t, obj.Spec.PersistentVolumeClaimRetentionPolicy)
	require.Empty(t, obj.Spec.UpdateStrategy.Type)
	r.Spec.MinReadySeconds = new(int64(10))
	obj, err = r.expand(nil)
	require.NoError(t, err)
	require.Zero(t, obj.Spec.MinReadySeconds)
}
func TestStatefulSetRollout(t *testing.T) {
	obj := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Spec: appsv1.StatefulSetSpec{Replicas: new(int32(1)),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: "RollingUpdate"}},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 1, ObservedGeneration: 2}}
	ready, err := statefulSetReady(obj)
	require.NoError(t, err)
	require.True(t, ready)
}
