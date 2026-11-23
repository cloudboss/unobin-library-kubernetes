package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyServiceLookup() error {
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
	service, err := api.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if service.UID == "" || service.Spec.ClusterIP == "" {
		return fmt.Errorf("existing Service has no UID or cluster address")
	}
	path := filepath.Join(dir, "service-info-uid")
	if stage == "applied" {
		if err := os.WriteFile(path, []byte(service.UID), 0o600); err != nil {
			return err
		}
	} else {
		previous, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(previous) != string(service.UID) {
			return fmt.Errorf("service identity changed during lookup scenario")
		}
	}
	_, err = api.CoreV1().Services("default").Get(ctx,
		"unobin-it-service-info-absent", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected missing Service, got %v", err)
	}
	fmt.Printf("ok: Service lookup preserved existing and missing objects after %s\n", stage)
	return nil
}
