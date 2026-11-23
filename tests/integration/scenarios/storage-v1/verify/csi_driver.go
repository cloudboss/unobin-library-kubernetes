package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyCsiDriver() error {
	phase, buildDir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := api.StorageV1().CSIDrivers().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{
			"test.unobin.cloudboss.io/scenario": "storage-v1-csi-driver",
		}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario CSI drivers: %w", err)
	}
	if phase == "destroyed" {
		if len(result.Items) != 0 {
			return fmt.Errorf("found CSI drivers after destroy: %v", result.Items)
		}
		return nil
	}
	if len(result.Items) != 1 {
		return fmt.Errorf("expected one CSI driver, got %v", result.Items)
	}
	driver := &result.Items[0]
	if driver.Namespace != "" || driver.UID == "" || driver.GenerateName != "unobin-it-csi-" ||
		!strings.HasPrefix(driver.Name, driver.GenerateName) || driver.Name == driver.GenerateName {
		return fmt.Errorf("unexpected CSI driver identity: %s/%s", driver.Name, driver.UID)
	}
	revision := "initial"
	if phase == "updated" {
		revision = "updated"
	}
	if driver.Annotations["test.unobin.cloudboss.io/revision"] != revision {
		return fmt.Errorf("unexpected CSI driver revision: %v", driver.Annotations)
	}
	spec := driver.Spec
	if spec.AttachRequired == nil || !*spec.AttachRequired ||
		spec.PodInfoOnMount == nil || *spec.PodInfoOnMount != (phase == "updated") ||
		!slices.Equal(spec.VolumeLifecycleModes, []storagev1.VolumeLifecycleMode{
			storagev1.VolumeLifecyclePersistent,
		}) {
		return fmt.Errorf("unexpected CSI driver spec: %v", spec)
	}
	uidPath := filepath.Join(buildDir, "csi-driver-uid")
	if phase == "applied" {
		return os.WriteFile(uidPath, []byte(driver.UID), 0o600)
	}
	uid, err := os.ReadFile(uidPath)
	if err != nil {
		return fmt.Errorf("read CSI driver UID: %w", err)
	}
	if string(uid) != string(driver.UID) {
		return fmt.Errorf("CSI driver UID changed from %s to %s", uid, driver.UID)
	}
	return nil
}
