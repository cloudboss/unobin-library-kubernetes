package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const deploymentName = "unobin-it-deployment-api"

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
	deployments := api.AppsV1().Deployments("default")
	uidPath := filepath.Join(buildDir, "deployment-uid")
	switch phase {
	case "applied":
		deployment, err := verifyPresent(ctx, deployments, 1, "registry.k8s.io/pause:3.9")
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
		deployment, err := verifyPresent(ctx, deployments, 2, "registry.k8s.io/pause:3.10")
		if err != nil {
			return err
		}
		if string(deployment.UID) != string(uid) {
			return fmt.Errorf("deployment UID is %q, want %q", deployment.UID, uid)
		}
	case "destroyed":
		_, err := deployments.Get(ctx, deploymentName, metav1.GetOptions{})
		if err == nil {
			return fmt.Errorf("deployment %s still exists", deploymentName)
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get destroyed Deployment: %w", err)
		}
	}
	fmt.Printf("ok: Deployment %s matches the %s configuration\n", deploymentName, phase)
	return nil
}

func verifyPresent(
	ctx context.Context,
	api typedappsv1.DeploymentInterface,
	replicas int32,
	image string,
) (*appsv1.Deployment, error) {
	deployment, err := api.Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get Deployment %s: %w", deploymentName, err)
	}
	if deployment.Name != deploymentName || deployment.Namespace != "default" {
		return nil, fmt.Errorf("deployment identity is %s/%s",
			deployment.Namespace, deployment.Name)
	}
	if deployment.UID == "" {
		return nil, fmt.Errorf("deployment %s has no UID", deploymentName)
	}
	if deployment.Labels["app"] != deploymentName {
		return nil, fmt.Errorf("deployment app label is %q", deployment.Labels["app"])
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != replicas {
		return nil, fmt.Errorf("deployment replicas do not equal %d", replicas)
	}
	if deployment.Spec.Selector == nil ||
		deployment.Spec.Selector.MatchLabels["app"] != deploymentName {
		return nil, fmt.Errorf("deployment selector does not match app=%s", deploymentName)
	}
	if deployment.Spec.Template.Labels["app"] != deploymentName {
		return nil, fmt.Errorf("deployment template app label is %q",
			deployment.Spec.Template.Labels["app"])
	}
	containers := deployment.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != "app" || containers[0].Image != image {
		return nil, fmt.Errorf("deployment containers do not match app image %q", image)
	}
	return deployment, nil
}
