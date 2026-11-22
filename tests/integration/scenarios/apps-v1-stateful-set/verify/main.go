package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const statefulSetName = "unobin-it-stateful-set"
const noWaitName = "unobin-it-stateful-set-nowait"

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	phase := os.Getenv("VERIFY_PHASE")
	build := os.Getenv("VERIFY_BUILD_DIR")
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if phase == "destroyed" {
		return verifyDestroyed(ctx, api)
	}
	replicas, image := int32(2), "registry.k8s.io/pause:3.9"
	if phase == "updated" {
		replicas, image = 1, "registry.k8s.io/pause:3.10"
	}
	var obj *appsv1.StatefulSet
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true, func(ctx context.Context) (
		bool, error) {
		var err error
		obj, err = verifyPresent(ctx, api, replicas, image)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("verify controller rollout: %w", err)
	}
	noWait, err := verifyValues(ctx, api, noWaitName, replicas, image)
	if err != nil {
		return err
	}
	identity := string(obj.UID) + "\n" + string(noWait.UID)
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1" {
		claim, err := api.CoreV1().PersistentVolumeClaims("default").Get(ctx,
			"data-"+statefulSetName+"-0", metav1.GetOptions{})
		if err != nil {
			return err
		}
		identity += "\n" + string(claim.UID)
	}
	path := filepath.Join(build, "stateful-set-identity")
	if phase == "applied" {
		return os.WriteFile(path, []byte(identity), 0o600)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(saved) != identity {
		return fmt.Errorf("stateful-set or retained ordinal-zero claim UID changed")
	}
	return nil
}

func verifyPresent(ctx context.Context, api kubernetes.Interface, replicas int32,
	image string) (*appsv1.StatefulSet, error) {
	obj, err := verifyValues(ctx, api, statefulSetName, replicas, image)
	if err != nil {
		return nil, err
	}
	policy := obj.Spec.PersistentVolumeClaimRetentionPolicy
	if policy == nil || policy.WhenDeleted != "Delete" || policy.WhenScaled != "Delete" {
		return nil, fmt.Errorf("claim retention policy differs")
	}
	service, err := api.CoreV1().Services("default").Get(ctx, statefulSetName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if service.Spec.ClusterIP != "None" || obj.Spec.ServiceName != service.Name {
		return nil, fmt.Errorf("governing Service is not headless")
	}
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		return obj, nil
	}
	if obj.Status.ObservedGeneration < obj.Generation || obj.Status.ReadyReplicas != replicas ||
		obj.Status.UpdatedReplicas != replicas {
		return nil, fmt.Errorf("stateful-set rollout incomplete")
	}
	pods, err := api.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + statefulSetName})
	if err != nil {
		return nil, err
	}
	if int32(len(pods.Items)) != replicas {
		return nil, fmt.Errorf("found %d pods, want %d", len(pods.Items), replicas)
	}
	for ordinal := int32(0); ordinal < replicas; ordinal++ {
		name := fmt.Sprintf("%s-%d", statefulSetName, ordinal)
		pod, err := api.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if pod.Name != name || pod.Spec.Hostname != name || pod.Spec.Subdomain != statefulSetName ||
			pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			return nil, fmt.Errorf("pod %s lacks ordered identity or running status", name)
		}
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.UID != obj.UID {
			return nil, fmt.Errorf("pod %s owner differs", name)
		}
		if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != image {
			return nil, fmt.Errorf("pod %s image differs", name)
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			return nil, fmt.Errorf("pod %s is not ready", name)
		}
		claim, err := api.CoreV1().PersistentVolumeClaims("default").Get(ctx, "data-"+name,
			metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if claim.UID == "" || claim.Status.Phase != corev1.ClaimBound || claim.Spec.VolumeName == "" {
			return nil, fmt.Errorf("claim data-%s is not bound", name)
		}
		found := false
		for _, volume := range pod.Spec.Volumes {
			if volume.Name == "data" && volume.PersistentVolumeClaim != nil &&
				volume.PersistentVolumeClaim.ClaimName == claim.Name {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("pod %s does not use its ordinal claim", name)
		}
	}
	if replicas == 1 {
		_, err := api.CoreV1().PersistentVolumeClaims("default").Get(ctx,
			"data-"+statefulSetName+"-1", metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("scaled-down claim was not deleted: %v", err)
		}
	}
	return obj, nil
}

func verifyDestroyed(ctx context.Context, api kubernetes.Interface) error {
	return wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true, func(ctx context.Context) (
		bool, error) {
		for _, name := range []string{statefulSetName, noWaitName} {
			_, err := api.AppsV1().StatefulSets("default").Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				return false, nil
			}
			if !apierrors.IsNotFound(err) {
				return false, err
			}
		}
		if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1" {
			for _, name := range []string{statefulSetName, noWaitName} {
				pods, err := api.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
					LabelSelector: "app=" + name})
				if err != nil {
					return false, err
				}
				if len(pods.Items) != 0 {
					return false, nil
				}
			}
			for ordinal := 0; ordinal < 2; ordinal++ {
				_, err := api.CoreV1().PersistentVolumeClaims("default").Get(ctx, fmt.Sprintf("data-%s-%d",
					statefulSetName, ordinal), metav1.GetOptions{})
				if err == nil {
					return false, nil
				}
				if !apierrors.IsNotFound(err) {
					return false, err
				}
			}
		}
		for ordinal := 0; ordinal < 2; ordinal++ {
			_, err := api.CoreV1().PersistentVolumes().Get(ctx, fmt.Sprintf(
				"unobin-it-stateful-set-pv-%d", ordinal), metav1.GetOptions{})
			if err == nil {
				return false, nil
			}
			if !apierrors.IsNotFound(err) {
				return false, err
			}
		}
		_, err := api.CoreV1().Services("default").Get(ctx, statefulSetName, metav1.GetOptions{})
		if err == nil {
			return false, nil
		}
		return apierrors.IsNotFound(err), errUnlessNotFound(err)
	})
}

func errUnlessNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func verifyValues(ctx context.Context, api kubernetes.Interface, name string,
	replicas int32, image string) (*appsv1.StatefulSet, error) {
	obj, err := api.AppsV1().StatefulSets("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if obj.Name != name || obj.Namespace != "default" || obj.UID == "" {
		return nil, fmt.Errorf("unexpected StatefulSet identity for %s", name)
	}
	if obj.Spec.Replicas == nil || *obj.Spec.Replicas != replicas ||
		obj.Spec.PodManagementPolicy != "OrderedReady" {
		return nil, fmt.Errorf("unexpected StatefulSet scale or pod ordering policy")
	}
	if obj.Labels["app"] != name || obj.Spec.Selector == nil ||
		obj.Spec.Selector.MatchLabels["app"] != name || obj.Spec.Template.Labels["app"] != name {
		return nil, fmt.Errorf("StatefulSet %s labels or selector differ", name)
	}
	containers := obj.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != "app" || containers[0].Image != image {
		return nil, fmt.Errorf("StatefulSet %s containers differ", name)
	}
	if obj.Spec.ServiceName != statefulSetName {
		return nil, fmt.Errorf("StatefulSet %s governing Service differs", name)
	}
	return obj, nil
}
