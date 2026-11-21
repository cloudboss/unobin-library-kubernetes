package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	scenarioName  = "core-v1-config-map"
	scenarioLabel = "test.unobin.cloudboss.io/scenario"
	namePrefix    = "unobin-it-config-map-"
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
	list, err := api.CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{scenarioLabel: scenarioName}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario ConfigMaps: %w", err)
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found %d scenario ConfigMaps after destroy, want 0", len(list.Items))
		}
		fmt.Println("ok: scenario ConfigMaps are gone")
		return nil
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("found %d scenario ConfigMaps, want 1", len(list.Items))
	}
	configMap := list.Items[0]
	if !strings.HasPrefix(configMap.Name, namePrefix) || configMap.Name == namePrefix {
		return fmt.Errorf("ConfigMap name is %q, want generated prefix %q", configMap.Name, namePrefix)
	}
	if configMap.GenerateName != namePrefix || configMap.Namespace != metav1.NamespaceDefault {
		return fmt.Errorf("ConfigMap namespace or generate-name differs from the scenario")
	}
	if configMap.UID == "" {
		return fmt.Errorf("ConfigMap has no UID")
	}
	if configMap.Immutable == nil || !*configMap.Immutable {
		return fmt.Errorf("ConfigMap is not immutable")
	}
	value := "first"
	binaryValue := []byte{0, 1}
	if phase == "updated" {
		value = "updated"
		binaryValue = []byte{2, 3}
	}
	if !maps.Equal(configMap.Data, map[string]string{"value": value}) {
		return fmt.Errorf("ConfigMap data is %v, want value %q", configMap.Data, value)
	}
	expectedBinary := map[string][]byte{"bytes": binaryValue, "empty": {}}
	if !maps.EqualFunc(configMap.BinaryData, expectedBinary, bytes.Equal) {
		return fmt.Errorf("ConfigMap binary-data is %v, want %v", configMap.BinaryData, expectedBinary)
	}
	uidPath := filepath.Join(buildDir, "config-map-uid")
	if phase == "applied" {
		if err := os.WriteFile(uidPath, []byte(configMap.UID), 0o600); err != nil {
			return fmt.Errorf("save initial ConfigMap UID: %w", err)
		}
	} else {
		initialUID, err := os.ReadFile(uidPath)
		if err != nil {
			return fmt.Errorf("read initial ConfigMap UID: %w", err)
		}
		if len(initialUID) == 0 || string(configMap.UID) == string(initialUID) {
			return fmt.Errorf("ConfigMap UID did not change after immutable content replacement")
		}
	}
	fmt.Printf("ok: ConfigMap %s matches the %s configuration\n", configMap.Name, phase)
	return nil
}
