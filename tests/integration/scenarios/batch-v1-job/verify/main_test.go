package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func validJob() *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiJobName, Namespace: "default", UID: "uid",
			Labels:      map[string]string{"app": "job-api", "stage": "initial"},
			Annotations: map[string]string{"stage": "initial"},
		},
		Spec: batchv1.JobSpec{
			Parallelism: new(int32(1)), BackoffLimit: new(int32(6)), Completions: new(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": apiJobName}},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "app", Image: "busybox:1.37"}},
				},
			},
		},
	}
}

func TestVerifyPresent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*batchv1.Job)
	}{
		{name: "valid"}, {name: "UID", change: func(j *batchv1.Job) { j.UID = "" }},
		{name: "labels", change: func(j *batchv1.Job) { j.Labels["stage"] = "wrong" }},
		{name: "annotations", change: func(j *batchv1.Job) { j.Annotations["stage"] = "wrong" }},
		{name: "parallelism", change: func(j *batchv1.Job) { j.Spec.Parallelism = new(int32(3)) }},
		{name: "backoff", change: func(j *batchv1.Job) { j.Spec.BackoffLimit = nil }},
		{name: "completion default", change: func(j *batchv1.Job) { j.Spec.Completions = new(int32(2)) }},
		{name: "restart default", change: func(j *batchv1.Job) {
			j.Spec.Template.Spec.RestartPolicy = "Always"
		}},
		{name: "template labels", change: func(j *batchv1.Job) { j.Spec.Template.Labels = nil }},
		{name: "image", change: func(j *batchv1.Job) {
			j.Spec.Template.Spec.Containers[0].Image = "wrong"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := validJob()
			if tc.change != nil {
				tc.change(obj)
			}
			api := fake.NewClientset(obj)
			_, err := verifyPresent(t.Context(), api.BatchV1().Jobs("default"), apiJobName,
				1, 6, "initial", false)
			if tc.change == nil {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestVerifyCompletionRequiresTrueCondition(t *testing.T) {
	for _, status := range []corev1.ConditionStatus{
		"", corev1.ConditionFalse, corev1.ConditionUnknown, corev1.ConditionTrue,
	} {
		obj := validJob()
		obj.Name = completionJobName
		obj.Spec.Template.Labels["app"] = completionJobName
		obj.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: status}}
		api := fake.NewClientset(obj)
		_, err := verifyPresent(t.Context(), api.BatchV1().Jobs("default"), completionJobName,
			1, 6, "initial", true)
		if status == corev1.ConditionTrue {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
		_, err = verifyPresent(t.Context(), api.BatchV1().Jobs("default"), completionJobName,
			1, 6, "initial", false)
		require.NoError(t, err)
	}
}
