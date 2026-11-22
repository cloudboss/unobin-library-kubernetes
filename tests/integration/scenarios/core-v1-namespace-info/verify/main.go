package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	stage := os.Getenv("VERIFY_PHASE")
	if stage != "applied" && stage != "updated" && stage != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return verifyNamespaces(ctx, api, dir, stage)
}

type namespaceSnapshot struct {
	UID         string
	Annotations map[string]string
	Labels      map[string]string
	Finalizers  []string
}

func verifyNamespaces(
	ctx context.Context, api kubernetes.Interface, dir, stage string,
) error {
	snapshots := map[string]namespaceSnapshot{}
	for _, name := range []string{"default", "kube-public"} {
		obj, err := api.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if obj.UID == "" {
			return fmt.Errorf("namespace %s has no UID", name)
		}
		value := namespaceSnapshot{
			UID: string(obj.UID), Annotations: obj.Annotations, Labels: obj.Labels, Finalizers: []string{},
		}
		for _, finalizer := range obj.Spec.Finalizers {
			value.Finalizers = append(value.Finalizers, string(finalizer))
		}
		snapshots[name] = value
	}
	_, err := api.CoreV1().Namespaces().Get(ctx,
		"unobin-it-namespace-info-absent", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected missing Namespace, got %v", err)
	}
	path := filepath.Join(dir, "namespace-info.json")
	if stage == "applied" {
		body, err := json.Marshal(snapshots)
		if err != nil {
			return err
		}
		return os.WriteFile(path, body, 0o600)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	previous := map[string]namespaceSnapshot{}
	if err := json.Unmarshal(body, &previous); err != nil {
		return err
	}
	if !reflect.DeepEqual(previous, snapshots) {
		return fmt.Errorf("namespace identity, metadata, or finalizers changed during lookup scenario")
	}
	fmt.Printf("ok: Namespace lookups preserved existing and missing objects after %s\n", stage)
	return nil
}
