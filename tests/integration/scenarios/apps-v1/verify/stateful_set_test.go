package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func controllerObjects() []runtime.Object {
	obj := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: statefulSetName,
		Namespace: "default", UID: "uid", Generation: 1,
		Labels: map[string]string{"app": statefulSetName}}, Spec: appsv1.StatefulSetSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": statefulSetName}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": statefulSetName}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "image"}}}},
		Replicas: new(int32(1)), PodManagementPolicy: "OrderedReady", ServiceName: statefulSetName,
		PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: "Delete", WhenScaled: "Delete"},
	}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 1, UpdatedReplicas: 1}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: statefulSetName,
		Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIP: "None"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: statefulSetName + "-0",
		Namespace: "default", Labels: map[string]string{"app": statefulSetName},
		OwnerReferences: []metav1.OwnerReference{{UID: "uid", Controller: new(true)}}},
		Spec: corev1.PodSpec{Hostname: statefulSetName + "-0", Subdomain: statefulSetName,
			Containers: []corev1.Container{{Name: "app", Image: "image"}}, Volumes: []corev1.Volume{{
				Name: "data", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "data-" + statefulSetName + "-0"}}}}}, Status: corev1.PodStatus{
			Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady,
				Status: corev1.ConditionTrue}}}}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "data-" + statefulSetName + "-0", Namespace: "default", UID: "claim"},
		Spec:   corev1.PersistentVolumeClaimSpec{VolumeName: "pv"},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	return []runtime.Object{obj, service, pod, claim}
}

func TestControllerVerifier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]runtime.Object)
		valid  bool
	}{
		{name: "complete", valid: true},
		{name: "not observed", change: func(o []runtime.Object) {
			o[0].(*appsv1.StatefulSet).Status.ObservedGeneration = 0
		}},
		{name: "retention", change: func(o []runtime.Object) {
			o[0].(*appsv1.StatefulSet).Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled = "Retain"
		}},
		{name: "headless", change: func(o []runtime.Object) {
			o[1].(*corev1.Service).Spec.ClusterIP =
				"10.0.0.1"
		}},
		{name: "ordinal", change: func(o []runtime.Object) {
			o[2].(*corev1.Pod).Spec.Hostname =
				"other"
		}},
		{name: "image", change: func(o []runtime.Object) {
			o[2].(*corev1.Pod).Spec.Containers[0].Image = "old"
		}},
		{name: "ready", change: func(o []runtime.Object) {
			o[2].(*corev1.Pod).Status.Conditions[0].Status = corev1.ConditionFalse
		}},
		{name: "bound", change: func(o []runtime.Object) {
			o[3].(*corev1.PersistentVolumeClaim).Status.Phase = corev1.ClaimPending
		}},
		{name: "owner", change: func(o []runtime.Object) {
			o[2].(*corev1.Pod).OwnerReferences[0].UID =
				"other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := controllerObjects()
			if tc.change != nil {
				tc.change(objects)
			}
			_, err := verifyPresentStatefulSet(t.Context(), fake.NewClientset(objects...), 1, "image")
			require.Equal(t, tc.valid, err == nil, err)
		})
	}
	objects := controllerObjects()
	objects = append(objects, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "data-" + statefulSetName + "-1", Namespace: "default"}})
	_, err := verifyPresentStatefulSet(t.Context(), fake.NewClientset(objects...), 1, "image")
	require.ErrorContains(t, err, "scaled-down claim")
}

func TestControllerDestroyChecksDependents(t *testing.T) {
	objects := controllerObjects()
	objects = append(objects, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{
		Name: "unobin-it-stateful-set-pv-0"}})
	objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: noWaitName + "-0", Namespace: "default", Labels: map[string]string{"app": noWaitName}}})
	for _, obj := range objects {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		require.ErrorIs(t, verifyDestroyedStatefulSet(ctx, fake.NewClientset(obj)), context.DeadlineExceeded)
		cancel()
	}
	require.NoError(t, verifyDestroyedStatefulSet(t.Context(), fake.NewClientset()))
}

func TestAPIModeVerifier(t *testing.T) {
	t.Setenv("UNOBIN_KUBERNETES_API_ONLY", "1")
	for _, tc := range []struct {
		name   string
		change func(*appsv1.StatefulSet)
		valid  bool
	}{
		{name: "API values without controllers", valid: true},
		{name: "replicas", change: func(o *appsv1.StatefulSet) { o.Spec.Replicas = new(int32(2)) }},
		{name: "image", change: func(o *appsv1.StatefulSet) {
			o.Spec.Template.Spec.Containers[0].Image = "other"
		}},
		{name: "UID", change: func(o *appsv1.StatefulSet) { o.UID = "" }},
		{name: "label", change: func(o *appsv1.StatefulSet) { o.Labels["app"] = "other" }},
		{name: "selector", change: func(o *appsv1.StatefulSet) { o.Spec.Selector = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := controllerObjects()[0].(*appsv1.StatefulSet)
			obj.Status = appsv1.StatefulSetStatus{}
			if tc.change != nil {
				tc.change(obj)
			}
			api := fake.NewClientset(obj, controllerObjects()[1])
			_, err := verifyPresentStatefulSet(t.Context(), api, 1, "image")
			require.Equal(t, tc.valid, err == nil, err)
			for _, action := range api.Actions() {
				require.NotContains(t, []string{"pods", "persistentvolumeclaims"},
					action.GetResource().Resource)
			}
		})
	}
}

func TestNoWaitVerifier(t *testing.T) {
	obj := controllerObjects()[0].(*appsv1.StatefulSet)
	obj.Name = noWaitName
	obj.Labels["app"] = noWaitName
	obj.Spec.Selector.MatchLabels["app"] = noWaitName
	obj.Spec.Template.Labels["app"] = noWaitName
	obj.Status = appsv1.StatefulSetStatus{}
	_, err := verifyValues(t.Context(), fake.NewClientset(obj), noWaitName, 1, "image")
	require.NoError(t, err)
	obj.Spec.Template.Spec.Containers[0].Image = "other"
	_, err = verifyValues(t.Context(), fake.NewClientset(obj), noWaitName, 1, "image")
	require.Error(t, err)
}

func TestAPIModeDestroyChecksAPIResources(t *testing.T) {
	t.Setenv("UNOBIN_KUBERNETES_API_ONLY", "1")
	for _, obj := range []runtime.Object{
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: noWaitName, Namespace: "default"}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "unobin-it-stateful-set-pv-0"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: statefulSetName, Namespace: "default"}},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		require.ErrorIs(t, verifyDestroyedStatefulSet(ctx, fake.NewClientset(obj)), context.DeadlineExceeded)
		cancel()
	}
	api := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "uncontrolled"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "uncontrolled"}})
	require.NoError(t, verifyDestroyedStatefulSet(t.Context(), api))
	for _, action := range api.Actions() {
		require.NotContains(t, []string{"pods", "persistentvolumeclaims"},
			action.GetResource().Resource)
	}
}
