package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyNamespaceList() error {
	config := "config.ub"
	switch stage := os.Getenv("VERIFY_PHASE"); stage {
	case "applied":
	case "updated":
		config = "update/config.ub"
	case "destroyed":
		return nil
	default:
		return fmt.Errorf("invalid VERIFY_PHASE %q", stage)
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "core-v1"), "output",
		"--format", "json", "-c", config)
	cmd.Dir = dir
	body, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read factory outputs: %w", err)
	}
	var result struct {
		Outputs struct {
			Namespaces []string `json:"namespaces"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode factory outputs: %w", err)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	list, err := api.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Namespaces directly: %w", err)
	}
	want := make([]string, 0, len(list.Items))
	for i := range list.Items {
		want = append(want, list.Items[i].Name)
	}
	if got := result.Outputs.Namespaces; got == nil || !slices.Equal(got, want) {
		return fmt.Errorf("namespace list differs: got %q, want %q", got, want)
	}
	return nil
}
