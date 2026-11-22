package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const daemonSetName = "unobin-it-daemon-set"

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	buildDir, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed, got %q", phase)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	for _, target := range []struct {
		name        string
		replacement bool
		rollout     bool
	}{
		{daemonSetName, false, true},
		{daemonSetName + "-replace", true, true},
		{daemonSetName + "-no-wait", false, false},
	} {
		if phase == "destroyed" {
			if err := verifyDestroyed(ctx, api, target.name); err != nil {
				return err
			}
			continue
		}
		image, epoch := "registry.k8s.io/pause:3.9", "first"
		if phase == "updated" {
			image, epoch = "registry.k8s.io/pause:3.10", "second"
		}
		if !target.replacement {
			epoch = ""
		}
		rollout := target.rollout && os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1"
		var obj *appsv1.DaemonSet
		if rollout {
			err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
				func(ctx context.Context) (bool, error) {
					var checkErr error
					obj, checkErr = verifyPresent(ctx, api, target.name, image, epoch, true)
					return checkErr == nil, nil
				})
		} else {
			obj, err = verifyPresent(ctx, api, target.name, image, epoch, false)
		}
		if err != nil {
			return err
		}
		uidPath := filepath.Join(buildDir, target.name+"-uid")
		if phase == "applied" {
			if err := os.WriteFile(uidPath, []byte(obj.UID), 0600); err != nil {
				return err
			}
		} else {
			old, err := os.ReadFile(uidPath)
			if err != nil {
				return err
			}
			if len(old) == 0 || (string(old) != string(obj.UID)) != target.replacement {
				return fmt.Errorf("DaemonSet %s UID did not match replacement expectation", target.name)
			}
		}
		fmt.Printf("ok: DaemonSet %s matches the %s configuration\n", target.name, phase)
	}
	return nil
}

func verifyPresent(
	ctx context.Context, api kubernetes.Interface, name, image, epoch string, rollout bool,
) (*appsv1.DaemonSet, error) {
	obj, err := api.AppsV1().DaemonSets("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if obj.Name != name || obj.Namespace != "default" || obj.UID == "" {
		return nil, fmt.Errorf("DaemonSet %s identity is incomplete", name)
	}
	labels := map[string]string{"app": name}
	if epoch != "" {
		labels["epoch"] = epoch
	}
	if obj.Labels["app"] != name || obj.Spec.Selector == nil ||
		!reflect.DeepEqual(labels, obj.Spec.Selector.MatchLabels) ||
		!reflect.DeepEqual(labels, obj.Spec.Template.Labels) {
		return nil, fmt.Errorf("DaemonSet %s labels and selector do not match", name)
	}
	if obj.Spec.RevisionHistoryLimit == nil || *obj.Spec.RevisionHistoryLimit != 0 {
		return nil, fmt.Errorf("DaemonSet %s revision history is not explicit zero", name)
	}
	containers := obj.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != "app" || containers[0].Image != image {
		return nil, fmt.Errorf("DaemonSet %s containers do not match %s", name, image)
	}
	memory := containers[0].Resources.Requests[corev1.ResourceMemory]
	if memory.Cmp(resource.MustParse("1Mi")) != 0 {
		return nil, fmt.Errorf("DaemonSet %s memory request is %s", name, memory.String())
	}
	volumes := obj.Spec.Template.Spec.Volumes
	found := false
	for _, volume := range volumes {
		if volume.Name == "scratch" && volume.EmptyDir != nil && volume.EmptyDir.SizeLimit != nil {
			found = volume.EmptyDir.SizeLimit.Cmp(resource.MustParse("1Mi")) == 0
		}
	}
	if !found {
		return nil, fmt.Errorf("DaemonSet %s scratch quantity differs", name)
	}
	if !rollout {
		return obj, nil
	}
	if obj.Generation != obj.Status.ObservedGeneration || obj.Status.DesiredNumberScheduled == 0 ||
		obj.Status.NumberReady != obj.Status.DesiredNumberScheduled {
		return nil, fmt.Errorf("DaemonSet %s rollout incomplete: %+v", name, obj.Status)
	}
	pods, err := api.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + name,
	})
	if err != nil {
		return nil, err
	}
	ready := int32(0)
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		containers := pod.Spec.Containers
		if len(containers) != 1 || containers[0].Name != "app" || containers[0].Image != image {
			return nil, fmt.Errorf("pod %s image does not match %s", pod.Name, image)
		}
		if pod.Status.Phase != corev1.PodRunning {
			return nil, fmt.Errorf("pod %s not running", pod.Name)
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready++
				break
			}
		}
	}
	if ready != obj.Status.DesiredNumberScheduled {
		return nil, fmt.Errorf("DaemonSet %s ready Pods %d, expected %d", name,
			ready, obj.Status.DesiredNumberScheduled)
	}
	return obj, nil
}

func verifyDestroyed(ctx context.Context, api kubernetes.Interface, name string) error {
	return wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			_, err := api.AppsV1().DaemonSets("default").Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				return false, nil
			}
			if !apierrors.IsNotFound(err) {
				return false, err
			}
			pods, err := api.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
				LabelSelector: "app=" + name,
			})
			if err != nil {
				return false, err
			}
			return len(pods.Items) == 0, nil
		})
}
