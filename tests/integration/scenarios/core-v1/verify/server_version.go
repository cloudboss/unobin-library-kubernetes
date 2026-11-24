package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	goversion "github.com/hashicorp/go-version"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyServerVersion() error {
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
			ServerVersion map[string]string `json:"server-version"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode factory outputs: %w", err)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	info, err := api.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("discover Kubernetes version: %w", err)
	}
	normalized, err := goversion.NewVersion(info.GitVersion)
	if err != nil {
		return fmt.Errorf("normalize Kubernetes version: %w", err)
	}
	want := map[string]string{
		"version":        normalized.String(),
		"build-date":     info.BuildDate,
		"compiler":       info.Compiler,
		"git-commit":     info.GitCommit,
		"git-tree-state": info.GitTreeState,
		"git-version":    info.GitVersion,
		"go-version":     info.GoVersion,
		"major":          info.Major,
		"minor":          info.Minor,
		"platform":       info.Platform,
	}
	if got := result.Outputs.ServerVersion; !maps.Equal(got, want) {
		return fmt.Errorf("server version differs: got %v, want %v", got, want)
	}
	return nil
}
