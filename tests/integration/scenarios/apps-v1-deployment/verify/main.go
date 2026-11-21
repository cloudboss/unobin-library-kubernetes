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

const (
	deploymentName       = "unobin-it-deployment"
	noWaitDeploymentName = "unobin-it-deployment-no-wait"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	buildDir := os.Getenv("VERIFY_BUILD_DIR")
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	phase := os.Getenv("VERIFY_PHASE")
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
		name    string
		rollout bool
	}{
		{deploymentName, os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1"},
		{noWaitDeploymentName, false},
	} {
		uidPath := filepath.Join(buildDir, target.name+"-uid")
		switch phase {
		case "applied":
			deployment, err := verifyPresent(ctx, api, target.name, 1,
				"registry.k8s.io/pause:3.9", "", target.rollout)
			if err != nil {
				return err
			}
			if err := os.WriteFile(uidPath, []byte(deployment.UID), 0o600); err != nil {
				return fmt.Errorf("save Deployment UID: %w", err)
			}
		case "updated":
			uid, err := os.ReadFile(uidPath)
			if err != nil {
				return fmt.Errorf("read Deployment UID: %w", err)
			}
			if len(uid) == 0 {
				return fmt.Errorf("saved Deployment UID is empty")
			}
			if _, err := verifyPresent(ctx, api, target.name, 2,
				"registry.k8s.io/pause:3.10", string(uid), target.rollout); err != nil {
				return err
			}
		case "destroyed":
			if err := verifyDestroyed(ctx, api, target.name); err != nil {
				return err
			}
		}
		fmt.Printf("ok: Deployment %s matches the %s configuration\n", target.name, phase)
	}
	return nil
}

func verifyPresent(
	ctx context.Context,
	api kubernetes.Interface,
	name string,
	replicas int32,
	image string,
	uid string,
	rollout bool,
) (*appsv1.Deployment, error) {
	deployment, err := api.AppsV1().Deployments("default").Get(ctx,
		name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get Deployment %s: %w", name, err)
	}
	if deployment.Name != name || deployment.Namespace != "default" {
		return nil, fmt.Errorf("deployment identity is %s/%s",
			deployment.Namespace, deployment.Name)
	}
	if deployment.UID == "" {
		return nil, fmt.Errorf("deployment %s has no UID", name)
	}
	if uid != "" && string(deployment.UID) != uid {
		return nil, fmt.Errorf("deployment UID is %q, want %q", deployment.UID, uid)
	}
	if deployment.Labels["app"] != name {
		return nil, fmt.Errorf("deployment app label is %q", deployment.Labels["app"])
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != replicas {
		return nil, fmt.Errorf("deployment replicas do not equal %d", replicas)
	}
	if deployment.Spec.Selector == nil ||
		deployment.Spec.Selector.MatchLabels["app"] != name {
		return nil, fmt.Errorf("deployment selector does not match app=%s", name)
	}
	if deployment.Spec.Template.Labels["app"] != name {
		return nil, fmt.Errorf("deployment template app label is %q",
			deployment.Spec.Template.Labels["app"])
	}
	containers := deployment.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != "app" || containers[0].Image != image {
		return nil, fmt.Errorf("deployment containers do not match app image %q", image)
	}
	if !rollout {
		return deployment, nil
	}
	status := deployment.Status
	if deployment.Generation != status.ObservedGeneration || status.Replicas != replicas ||
		status.UpdatedReplicas != replicas || status.ReadyReplicas != replicas ||
		status.AvailableReplicas != replicas {
		return nil, fmt.Errorf("deployment rollout is incomplete: generation %d, status %+v",
			deployment.Generation, status)
	}
	pods, err := api.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + name,
	})
	if err != nil {
		return nil, fmt.Errorf("list Deployment Pods: %w", err)
	}
	ready := int32(0)
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		containers := pod.Spec.Containers
		if len(containers) != 1 || containers[0].Name != "app" || containers[0].Image != image {
			return nil, fmt.Errorf("pod %s containers do not match app image %q", pod.Name, image)
		}
		if pod.Status.Phase != corev1.PodRunning {
			return nil, fmt.Errorf("pod %s is not running", pod.Name)
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready++
				break
			}
		}
	}
	if ready != replicas {
		return nil, fmt.Errorf("found %d ready Deployment Pods, want %d", ready, replicas)
	}
	return deployment, nil
}

func verifyDestroyed(ctx context.Context, api kubernetes.Interface, name string) error {
	err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			_, err := api.AppsV1().Deployments("default").Get(ctx,
				name, metav1.GetOptions{})
			if err == nil {
				return false, nil
			}
			if !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("get destroyed Deployment: %w", err)
			}
			options := metav1.ListOptions{LabelSelector: "app=" + name}
			replicaSets, err := api.AppsV1().ReplicaSets("default").List(ctx, options)
			if err != nil {
				return false, fmt.Errorf("list destroyed Deployment ReplicaSets: %w", err)
			}
			pods, err := api.CoreV1().Pods("default").List(ctx, options)
			if err != nil {
				return false, fmt.Errorf("list destroyed Deployment Pods: %w", err)
			}
			return len(replicaSets.Items) == 0 && len(pods.Items) == 0, nil
		})
	if err != nil {
		return fmt.Errorf("wait for Deployment, ReplicaSets, and Pods to disappear: %w", err)
	}
	return nil
}
