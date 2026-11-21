package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestVerifyPresentRejectsIncompleteRollout(t *testing.T) {
	tests := []struct {
		name   string
		change func(*appsv1.Deployment)
	}{
		{"unobserved generation", func(d *appsv1.Deployment) { d.Status.ObservedGeneration = 0 }},
		{"old replicas", func(d *appsv1.Deployment) { d.Status.Replicas = 2 }},
		{"not updated", func(d *appsv1.Deployment) { d.Status.UpdatedReplicas = 0 }},
		{"not ready", func(d *appsv1.Deployment) { d.Status.ReadyReplicas = 0 }},
		{"not available", func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 0 }},
		{"wrong image", func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Image = "old-image"
		}},
		{"changed UID", func(d *appsv1.Deployment) { d.UID = "replacement-uid" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := readyDeployment()
			tt.change(deployment)
			api := fake.NewClientset(deployment, readyPod())
			_, err := verifyPresent(t.Context(), api,
				1, "registry.k8s.io/pause:3.9", "deployment-uid")
			require.Error(t, err)
		})
	}
}

func TestVerifyPresentChecksReadyPods(t *testing.T) {
	tests := []struct {
		name   string
		change func(*corev1.Pod)
		valid  bool
	}{
		{name: "ready", valid: true},
		{name: "wrong image", change: func(p *corev1.Pod) {
			p.Spec.Containers[0].Image = "old-image"
		}},
		{name: "not ready", change: func(p *corev1.Pod) {
			p.Status.Conditions[0].Status = corev1.ConditionFalse
		}},
		{name: "not running", change: func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }},
		{name: "deleting", change: func(p *corev1.Pod) {
			p.DeletionTimestamp = new(metav1.Now())
		}},
		{name: "unrelated label", change: func(p *corev1.Pod) { p.Labels["app"] = "other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := readyPod()
			if tt.change != nil {
				tt.change(pod)
			}
			api := fake.NewClientset(readyDeployment(), pod)
			_, err := verifyPresent(t.Context(), api,
				1, "registry.k8s.io/pause:3.9", "deployment-uid")
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestVerifyDestroyedWaitsForAllObjects(t *testing.T) {
	for _, obj := range []runtime.Object{
		readyDeployment(), readyPod(), &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "replica-set", Namespace: "default", Labels: map[string]string{"app": deploymentName},
		}},
	} {
		t.Run(string(obj.GetObjectKind().GroupVersionKind().Kind), func(t *testing.T) {
			api := fake.NewClientset(obj)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, verifyDestroyed(ctx, api), context.DeadlineExceeded)
		})
	}
	api := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "unrelated", Namespace: "default", Labels: map[string]string{"app": "other"},
	}})
	require.NoError(t, verifyDestroyed(t.Context(), api))

	api = fake.NewClientset(readyPod())
	reads := 0
	api.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		reads++
		if reads == 2 {
			require.NoError(t, api.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"),
				"default", "ready-pod"))
		}
		return false, nil, nil
	})
	require.NoError(t, verifyDestroyed(t.Context(), api))
	require.Equal(t, 2, reads)
}

func TestVerifyDestroyedPropagatesAPIErrors(t *testing.T) {
	for _, resource := range []string{"deployments", "replicasets", "pods"} {
		t.Run(resource, func(t *testing.T) {
			api := fake.NewClientset()
			sentinel := errors.New("access denied")
			api.PrependReactor("*", resource,
				func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, sentinel
				})
			require.ErrorIs(t, verifyDestroyed(t.Context(), api), sentinel)
		})
	}
}

func readyPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ready-pod", Namespace: "default", Labels: map[string]string{"app": deploymentName},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", Image: "registry.k8s.io/pause:3.9"},
		}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func readyDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: deploymentName, Namespace: "default", UID: "deployment-uid", Generation: 1,
			Labels: map[string]string{"app": deploymentName},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": deploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": deploymentName}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "app", Image: "registry.k8s.io/pause:3.9"},
				}},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1,
			AvailableReplicas: 1,
		},
	}
}
