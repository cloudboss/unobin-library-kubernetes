package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/sdk/state"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	name = "unobin-it-dynamic-object-info"
)

func main() {
	if err := verifyAPI(); err != nil {
		log.Fatal(err)
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	current, err := api.CoreV1().ConfigMaps("default").Get(ctx, name, metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected deleted ConfigMap, got %v", err)
		}
		if _, err := api.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{}); err != nil {
			return err
		}
		return verifyEnvDestroyed(ctx, api, dir)
	}
	if err != nil {
		return err
	}
	value, config := "first", "config.ub"
	switch phase {
	case "applied":
	case "updated":
		value, config = "updated", "update/config.ub"
	default:
		return fmt.Errorf("unknown verification phase %q", phase)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "dynamic"), "state", "pull", "-c", config)
	cmd.Dir = dir
	body, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read scenario state: %w", err)
	}
	snapshot, err := state.DecodeSnapshot(body)
	if err != nil {
		return err
	}
	liveNamespace, err := api.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return err
	}
	for _, want := range []struct{ key, kind, name, uid string }{
		{"config-map", "ConfigMap", name, string(current.UID)},
		{"namespace", "Namespace", "default", string(liveNamespace.UID)},
	} {
		object, ok := snapshot.Outputs[want.key].(map[string]any)
		if !ok {
			return fmt.Errorf("expected an object for %s", want.key)
		}
		decoded := &unstructured.Unstructured{Object: object}
		if decoded.GetAPIVersion() != "v1" || decoded.GetKind() != want.kind ||
			decoded.GetName() != want.name || string(decoded.GetUID()) != want.uid {
			return fmt.Errorf("unexpected %s identity for %s", want.key, phase)
		}
		if want.key == "config-map" && (decoded.GetNamespace() != "default" ||
			!reflect.DeepEqual(object["data"], map[string]any{"value": value})) {
			return fmt.Errorf("unexpected ConfigMap content for %s", phase)
		}
	}
	if absent, ok := snapshot.Outputs["absent"]; !ok || absent != nil {
		return fmt.Errorf("missing ConfigMap lookup must return null")
	}
	if phase == "applied" {
		if err := verifyLabelsOwnership(ctx); err != nil {
			return fmt.Errorf("labels: %w", err)
		}
		if err := verifyAnnotationsOwnership(ctx); err != nil {
			return fmt.Errorf("annotations: %w", err)
		}
		if err := verifyMetadataUIDGuards(ctx); err != nil {
			return err
		}
	}
	return verifyEnv(ctx, api, snapshot, phase, dir)
}
