package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyNodes() error {
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	stage := os.Getenv("VERIFY_PHASE")
	switch stage {
	case "applied", "updated":
		plan := "plan.ubp"
		if stage == "updated" {
			plan = "plan-update.ubp"
		}
		if err := verifyPlan(filepath.Join(dir, plan)); err != nil {
			return err
		}
	case "destroyed":
	default:
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nodes, err := api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	current := make(map[string]string, len(nodes.Items))
	for _, node := range nodes.Items {
		if node.Name != nodeTaintFixtureName && node.Name != cordonNodeFixtureName {
			current[node.Name] = string(node.UID)
		}
	}
	if len(current) == 0 {
		return fmt.Errorf("test cluster has no Nodes")
	}
	path := filepath.Join(dir, "node-identities.json")
	if stage == "applied" {
		body, err := json.Marshal(current)
		if err != nil {
			return err
		}
		return os.WriteFile(path, body, 0o600)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var previous map[string]string
	if err := json.Unmarshal(body, &previous); err != nil {
		return err
	}
	if !maps.Equal(previous, current) {
		return fmt.Errorf("node identities changed during %s: was %v, got %v", stage,
			previous, current)
	}
	return nil
}

func verifyPlan(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	for _, step := range plan.Steps {
		if step.Address != "data-source.nodes-selected" {
			continue
		}
		want := map[string]any{"nodes": []any{}}
		if step.Decision != runtime.DecisionRead || !reflect.DeepEqual(step.ObservedOutputs, want) {
			return fmt.Errorf("expected a read with an empty Node list, got %s with %v",
				step.Decision, step.ObservedOutputs)
		}
		return nil
	}
	return fmt.Errorf("plan lacks data-source.nodes-selected")
}
