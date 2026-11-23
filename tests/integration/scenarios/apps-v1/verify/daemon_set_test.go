package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func readyDaemonSet() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: daemonSetName, Namespace: "default", UID: "uid", Generation: 2,
			Labels: map[string]string{"app": daemonSetName},
		},
		Spec: appsv1.DaemonSetSpec{
			RevisionHistoryLimit: new(int32(0)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": daemonSetName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": daemonSetName}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app", Image: "pause", Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1024Ki")},
						}}},
					Volumes: []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: new(resource.MustParse("1Mi"))},
					}}},
				},
			},
		},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration: 2, DesiredNumberScheduled: 1, NumberReady: 1,
		},
	}
}

func readyPodDaemonSet() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod", Namespace: "default", Labels: map[string]string{"app": daemonSetName},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "pause"}}},
		Status: corev1.PodStatus{Phase: "Running", Conditions: []corev1.PodCondition{{
			Type: "Ready", Status: "True",
		}}},
	}
}

func TestVerifyPresent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*appsv1.DaemonSet, *corev1.Pod)
		valid  bool
	}{
		{"ready", func(*appsv1.DaemonSet, *corev1.Pod) {}, true},
		{"generation", func(d *appsv1.DaemonSet, _ *corev1.Pod) { d.Status.ObservedGeneration-- }, false},
		{"readiness", func(d *appsv1.DaemonSet, _ *corev1.Pod) { d.Status.NumberReady = 0 }, false},
		{"image", func(d *appsv1.DaemonSet, _ *corev1.Pod) {
			d.Spec.Template.Spec.Containers[0].Image = "old"
		}, false},
		{"quantity", func(d *appsv1.DaemonSet, _ *corev1.Pod) {
			d.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] =
				resource.MustParse("2Mi")
		}, false},
		{"Pod image", func(_ *appsv1.DaemonSet, p *corev1.Pod) {
			p.Spec.Containers[0].Image = "old"
		}, false},
		{"Pod readiness", func(_ *appsv1.DaemonSet, p *corev1.Pod) {
			p.Status.Conditions[0].Status = "False"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, p := readyDaemonSet(), readyPodDaemonSet()
			tc.change(d, p)
			_, err := verifyPresentDaemonSet(t.Context(), fake.NewClientset(d, p), daemonSetName, "pause", "", true)
			require.Equal(t, tc.valid, err == nil, "%v", err)
		})
	}
	d := readyDaemonSet()
	d.Status = appsv1.DaemonSetStatus{}
	api := fake.NewClientset(d)
	api.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		t.Error("false-wait verifier requested Pods")
		return true, nil, errors.New("unexpected Pods")
	})
	_, err := verifyPresentDaemonSet(t.Context(), api, daemonSetName, "pause", "", false)
	require.NoError(t, err)
}

func TestVerifyDestroyed(t *testing.T) {
	for _, obj := range []runtime.Object{readyDaemonSet(), readyPodDaemonSet()} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		require.ErrorIs(t, verifyDestroyedDaemonSet(ctx, fake.NewClientset(obj), daemonSetName),
			context.DeadlineExceeded)
		cancel()
	}
	require.NoError(t, verifyDestroyedDaemonSet(t.Context(), fake.NewClientset(), daemonSetName))
	for _, kind := range []string{"daemonsets", "pods"} {
		api := fake.NewClientset()
		sentinel := errors.New("forbidden")
		api.PrependReactor("*", kind, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, sentinel
		})
		require.ErrorIs(t, verifyDestroyedDaemonSet(t.Context(), api, daemonSetName), sentinel)
	}
}
