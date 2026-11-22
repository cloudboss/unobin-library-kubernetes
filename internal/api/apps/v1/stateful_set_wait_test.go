package v1

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

func TestStatefulSetRolloutPredicateMatrix(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		replicas             *int32
		ready, updated       int32
		generation, observed int64
		strategy             string
		rolling              *appsv1.RollingUpdateStatefulSetStrategy
		current, update      string
		want, fail           bool
	}{
		{name: "under ready", replicas: new(int32(2)), ready: 1, strategy: "OnDelete"},
		{name: "excess ready", replicas: new(int32(1)), ready: 2, strategy: "RollingUpdate"},
		{name: "zero unobserved", replicas: new(int32(0)), strategy: "RollingUpdate"},
		{name: "zero observed", replicas: new(int32(0)), observed: 1, strategy: "RollingUpdate",
			want: true},
		{name: "stale", replicas: new(int32(1)), ready: 1, generation: 2, observed: 1,
			strategy: "RollingUpdate"},
		{name: "equal", replicas: new(int32(1)), ready: 1, generation: 2, observed: 2,
			strategy: "RollingUpdate", want: true},
		{name: "ahead", replicas: new(int32(1)), ready: 1, generation: 2, observed: 3,
			strategy: "RollingUpdate", want: true},
		{name: "OnDelete gate", replicas: new(int32(1)), ready: 1, strategy: "OnDelete", fail: true},
		{name: "nil replicas", ready: 1, observed: 1, strategy: "RollingUpdate", want: true},
		{name: "partition zero boundary", replicas: new(int32(2)), ready: 2, updated: 2, observed: 1,
			strategy: "RollingUpdate", rolling: &appsv1.RollingUpdateStatefulSetStrategy{Partition: new(
				int32(0))}, current: "old", update: "new", want: true},
		{name: "partition under", replicas: new(int32(2)), ready: 2, updated: 0, observed: 1,
			strategy: "RollingUpdate", rolling: &appsv1.RollingUpdateStatefulSetStrategy{Partition: new(
				int32(1))}},
		{name: "partition boundary", replicas: new(int32(2)), ready: 2, updated: 1, observed: 1,
			strategy: "RollingUpdate", rolling: &appsv1.RollingUpdateStatefulSetStrategy{Partition: new(
				int32(1))}, want: true},
		{name: "partition exceeds replicas", replicas: new(int32(2)), ready: 2, observed: 1,
			strategy: "RollingUpdate", rolling: &appsv1.RollingUpdateStatefulSetStrategy{Partition: new(
				int32(3))}, want: true},
		{name: "nil partition ignores revision", replicas: new(int32(1)), ready: 1, observed: 1,
			strategy: "RollingUpdate", rolling: &appsv1.RollingUpdateStatefulSetStrategy{},
			current: "old", update: "new", want: true},
		{name: "revision mismatch without object", replicas: new(int32(1)), ready: 1, observed: 1,
			strategy: "RollingUpdate", current: "old", update: "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Generation: tc.generation},
				Spec: appsv1.StatefulSetSpec{Replicas: tc.replicas,
					UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
						Type: appsv1.StatefulSetUpdateStrategyType(tc.strategy), RollingUpdate: tc.rolling}},
				Status: appsv1.StatefulSetStatus{ReadyReplicas: tc.ready, UpdatedReplicas: tc.updated,
					ObservedGeneration: tc.observed, CurrentRevision: tc.current, UpdateRevision: tc.update}}
			ready, err := statefulSetReady(obj)
			require.Equal(t, tc.fail, err != nil)
			require.Equal(t, tc.want, ready)
		})
	}
}

type statefulSetDeadlineClient struct {
	typedappsv1.StatefulSetInterface
	t        *testing.T
	deadline time.Time
	obj      *appsv1.StatefulSet
	calls    int
}

func (c *statefulSetDeadlineClient) Get(ctx context.Context, _ string, _ metav1.GetOptions) (
	*appsv1.StatefulSet, error) {
	c.calls++
	deadline, ok := ctx.Deadline()
	require.True(c.t, ok)
	require.Equal(c.t, c.deadline, deadline)
	return c.obj, nil
}

func TestStatefulSetPostWriteDeadlineIsUnchanged(t *testing.T) {
	r := statefulSet()
	r.ReadTimeout = new("1ms")
	obj, err := r.expand(nil)
	require.NoError(t, err)
	defaultStatefulSet(obj)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	api := &statefulSetDeadlineClient{t: t, deadline: deadline, obj: obj}
	out, err := r.statefulSetOutput(obj, "server", nil)
	require.NoError(t, err)
	_, err = r.finishStatefulSet(ctx, api, out.target(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, api.calls)
	for _, field := range []string{"create-timeout", "read-timeout", "update-timeout",
		"delete-timeout"} {
		timeout, err := duration(nil, field)
		require.NoError(t, err)
		require.Equal(t, 10*time.Minute, timeout)
	}
}
