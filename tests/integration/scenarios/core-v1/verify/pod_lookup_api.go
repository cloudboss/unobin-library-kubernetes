package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkCompiledPodInfo(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	pods := api.CoreV1().Pods("default")
	const image = "registry.k8s.io/pause:3.10"
	pod, err := pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-pod-info-",
			Annotations:  map[string]string{"deployment.kubernetes.io/revision": "1"},
			Labels:       map[string]string{"pod-template-hash": "lookup-fixture"},
		},
		Spec: corev1.PodSpec{
			SchedulerName:                "unobin-unscheduled",
			AutomountServiceAccountToken: new(false),
			Containers:                   []corev1.Container{{Name: "test", Image: image}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{
				Preconditions:      &metav1.Preconditions{UID: &pod.UID},
				GracePeriodSeconds: new(int64(0)),
			})
			if err != nil {
				return fmt.Errorf("API check: %w", err)
			}
			if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second,
				true, func(ctx context.Context) (bool, error) {
					_, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return false, err
				}); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	if !reflect.DeepEqual(corev1.PodPending, pod.Status.Phase) {
		return fmt.Errorf("unexpected pod.Status.Phase")
	}

	factory, err := compileFixture(ctx, "testdata/ub/pod-lookup", "pod-info")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer factory.cleanup(&result)
	if err := factory.config("testdata/ub/pod-lookup/config.ub", "config.ub",
		strings.NewReplacer("POD_INFO_NAME", pod.Name)); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if err := factory.commands([]string{"plan", "-c", "config.ub", "-o", "plan.ubp"}); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	factory.applied = true
	if err := factory.commands([]string{"apply", "plan.ubp"}); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	body, err := factory.run("output", "--format", "json", "-c", "config.ub")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	var outputs struct {
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(body, &outputs); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(map[string]any{
		"name": pod.Name, "namespace": "default", "prefix": pod.GenerateName, "uid": string(pod.UID),
		"annotations": map[string]any{"deployment.kubernetes.io/revision": "1"},
		"labels":      map[string]any{"pod-template-hash": "lookup-fixture"},
		"phase":       "Pending", "node": "", "scheduler": "unobin-unscheduled",
		"images": []any{image}, "grace": float64(30), "volumes": []any{},
		"missing-metadata": map[string]any{
			"name": pod.Name + "-absent", "generate-name": "missing-", "namespace": "default",
			"annotations": map[string]any{"EXAMPLE.COM/Lookup": "missing"},
			"labels":      map[string]any{"app": "missing"},
			"generation":  float64(0), "resource-version": "", "uid": "",
		},
		"missing-phase": nil, "missing-spec": nil,
	}, outputs.Outputs) {
		return fmt.Errorf("unexpected outputs.Outputs")
	}
	if err := factory.commands(
		[]string{"plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp"},
		[]string{"apply", "destroy.ubp"},
	); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	factory.applied = false
	after, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(pod.UID, after.UID) {
		return fmt.Errorf("unexpected after.UID")
	}
	if !reflect.DeepEqual(pod.ResourceVersion, after.ResourceVersion) {
		return fmt.Errorf("unexpected after.ResourceVersion")
	}
	if !reflect.DeepEqual(pod.Spec, after.Spec) {
		return fmt.Errorf("unexpected after.Spec")
	}
	if !reflect.DeepEqual(pod.Status, after.Status) {
		return fmt.Errorf("unexpected after.Status")
	}

	return nil
}
