package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	scenarioName  = "core-v1-namespace"
	scenarioLabel = "test.unobin.cloudboss.io/scenario"
	namePrefix    = "unobin-it-namespace-"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	phase := os.Getenv("VERIFY_PHASE")
	switch phase {
	case "applied", "updated", "destroyed":
	default:
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed, got %q", phase)
	}
	buildDir := os.Getenv("VERIFY_BUILD_DIR")
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	namePath := filepath.Join(buildDir, "namespace-name")
	uidPath := filepath.Join(buildDir, "namespace-uid")
	if phase == "destroyed" {
		name, err := os.ReadFile(namePath)
		if err != nil {
			return fmt.Errorf("read initial Namespace name: %w", err)
		}
		_, err = api.CoreV1().Namespaces().Get(ctx, string(name), metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("namespace %s still exists or lookup failed: %v", name, err)
		}
		fmt.Println("ok: scenario Namespace is gone")
		return nil
	}
	list, err := api.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{scenarioLabel: scenarioName}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario Namespaces: %w", err)
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("found %d scenario Namespaces, want 1", len(list.Items))
	}
	namespace := list.Items[0]
	if !strings.HasPrefix(namespace.Name, namePrefix) || namespace.Name == namePrefix ||
		namespace.GenerateName != namePrefix || namespace.UID == "" {
		return fmt.Errorf("namespace has an invalid generated name or UID")
	}
	value := "first"
	if phase == "updated" {
		value = "updated"
	}
	if namespace.Labels["value"] != value || namespace.Annotations["example.com/value"] != value {
		return fmt.Errorf("namespace metadata differs from value %q", value)
	}
	sa, err := api.CoreV1().ServiceAccounts(namespace.Name).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read default ServiceAccount: %w", err)
	}
	if sa.Name != "default" || sa.Namespace != namespace.Name {
		return fmt.Errorf("default ServiceAccount has an invalid name or namespace")
	}
	if phase == "applied" {
		if err := os.WriteFile(namePath, []byte(namespace.Name), 0o600); err != nil {
			return fmt.Errorf("save initial Namespace name: %w", err)
		}
		if err := os.WriteFile(uidPath, []byte(namespace.UID), 0o600); err != nil {
			return fmt.Errorf("save initial Namespace UID: %w", err)
		}
	} else {
		initialUID, err := os.ReadFile(uidPath)
		if err != nil {
			return fmt.Errorf("read initial Namespace UID: %w", err)
		}
		if string(namespace.UID) != string(initialUID) {
			return fmt.Errorf("namespace UID changed after metadata update")
		}
	}
	fmt.Printf("ok: Namespace %s matches the %s configuration\n", namespace.Name, phase)
	return nil
}
