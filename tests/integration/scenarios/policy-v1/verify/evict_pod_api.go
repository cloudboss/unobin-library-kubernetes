package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	policy "github.com/cloudboss/unobin-library-kubernetes/internal/api/policy/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkEvictPod(ctx context.Context) (result error) {
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	namespace, err := api.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "unobin-eviction-api-"}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, api.CoreV1().Pods(namespace.Name).DeleteCollection(cleanup,
			metav1.DeleteOptions{GracePeriodSeconds: new(int64(0))}, metav1.ListOptions{}),
			api.PolicyV1().PodDisruptionBudgets(namespace.Name).DeleteCollection(cleanup,
				metav1.DeleteOptions{}, metav1.ListOptions{}),
			api.CoreV1().Namespaces().Delete(cleanup, namespace.Name, metav1.DeleteOptions{}))
	}()
	pods := api.CoreV1().Pods(namespace.Name)
	pod, err := pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "disposable", Labels: map[string]string{"app": "evict"}},
		Spec: corev1.PodSpec{AutomountServiceAccountToken: new(false),
			NodeSelector: map[string]string{"test.unobin.cloudboss.io/unscheduled": "true"},
			Containers:   []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.9"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	action := &policy.EvictPodAction{Name: pod.Name, Namespace: &namespace.Name,
		ExpectedUID: string(pod.UID), GracePeriodSeconds: new(int64(0)), Timeout: new("10s")}
	out, err := action.Run(ctx, nil)
	if err != nil {
		return err
	}
	if !out.Accepted || !out.Deleted || !out.Waited || out.UID != string(pod.UID) {
		return fmt.Errorf("unexpected eviction output: %+v", out)
	}
	if _, err = pods.Get(ctx, pod.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("evicted Pod still exists: %w", err)
	}
	out, err = action.Run(ctx, nil)
	if err != nil {
		return err
	}
	if out.Accepted || !out.Deleted || !out.Waited {
		return fmt.Errorf("absence output differs")
	}
	pod, err = pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "protected", Labels: pod.Labels},
		Spec:       pod.Spec,
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{
		Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
	pod, err = pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	budgets := api.PolicyV1().PodDisruptionBudgets(namespace.Name)
	minimum := intstr.FromInt32(1)
	budget, err := budgets.Create(ctx, &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "blocked"}, Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minimum, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app": "evict"}}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	action.Name = pod.Name
	action.ExpectedUID = string(pod.UID)
	action.Timeout = new("200ms")
	out, err = action.Run(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) || out != nil {
		return fmt.Errorf("expected PDB refusal and deadline, got output=%+v error=%w", out, err)
	}
	if !apierrors.IsTooManyRequests(err) && !strings.Contains(err.Error(), "HTTP 429") {
		return fmt.Errorf("expected PDB refusal context, got: %w", err)
	}
	current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.UID != pod.UID || current.DeletionTimestamp != nil {
		return fmt.Errorf("PDB refused eviction but target changed")
	}
	canceled, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	action.Timeout = new("10s")
	out, err = action.Run(canceled, nil)
	if !errors.Is(err, context.Canceled) || out != nil {
		return fmt.Errorf("expected canceled refusal, got output=%+v error=%w", out, err)
	}
	if err = budgets.Delete(ctx, budget.Name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	out, err = action.Run(ctx, nil)
	if err != nil {
		return err
	}
	if !out.Accepted || !out.Deleted {
		return fmt.Errorf("allowed eviction did not complete")
	}
	return nil
}

func verifyEvictionFactory(ctx context.Context) error {
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	namespaces, err := api.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: "test.unobin.cloudboss.io/scenario=policy-v1-evict-pod"})
	if err != nil {
		return err
	}
	if os.Getenv("VERIFY_PHASE") == "destroyed" {
		if len(namespaces.Items) != 0 {
			return fmt.Errorf("eviction namespace remains after destroy")
		}
		return nil
	}
	if len(namespaces.Items) != 1 {
		return fmt.Errorf("expected one isolated eviction namespace")
	}
	namespace := namespaces.Items[0]
	if !strings.HasPrefix(namespace.Name, "unobin-it-eviction-") || namespace.UID == "" {
		return fmt.Errorf("unexpected eviction namespace identity")
	}
	_, err = api.CoreV1().Pods(namespace.Name).Get(ctx, "disposable", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("factory eviction did not remove disposable Pod: %w", err)
	}
	fmt.Println("ok: compiled action evicted its isolated disposable Pod")
	return nil
}
