package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
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
	result, err := api.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{
			"test.unobin.cloudboss.io/scenario": "storage-v1-storage-class",
		}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario StorageClasses: %w", err)
	}
	if phase == "destroyed" {
		if len(result.Items) != 0 {
			return fmt.Errorf("found %d StorageClasses after destroy", len(result.Items))
		}
		return nil
	}
	if len(result.Items) != 1 {
		return fmt.Errorf("found %d StorageClasses, want 1", len(result.Items))
	}
	class := &result.Items[0]
	if class.Namespace != "" || class.UID == "" || class.GenerateName != "unobin-it-storage-" ||
		!strings.HasPrefix(class.Name, class.GenerateName) {
		return fmt.Errorf("unexpected StorageClass identity")
	}
	if class.Provisioner != "kubernetes.io/no-provisioner" ||
		ptr.Deref(class.ReclaimPolicy, "") != corev1.PersistentVolumeReclaimDelete ||
		ptr.Deref(class.VolumeBindingMode, "") != storagev1.VolumeBindingWaitForFirstConsumer ||
		!slices.Equal(class.MountOptions, []string{"noexec", "nosuid"}) {
		return fmt.Errorf("unexpected StorageClass provisioner, policy, binding, or mount options")
	}
	revision := "initial"
	if phase == "updated" {
		revision = "updated"
	}
	if class.Annotations["test.unobin.cloudboss.io/revision"] != revision ||
		class.AllowVolumeExpansion == nil || *class.AllowVolumeExpansion != (phase == "applied") {
		return fmt.Errorf("StorageClass metadata or expansion differs from the %s configuration", phase)
	}
	uidPath := filepath.Join(buildDir, "storage-class-uid")
	if phase == "applied" {
		if err := os.WriteFile(uidPath, []byte(class.UID), 0o600); err != nil {
			return fmt.Errorf("save StorageClass UID: %w", err)
		}
	} else {
		uid, err := os.ReadFile(uidPath)
		if err != nil {
			return fmt.Errorf("read StorageClass UID: %w", err)
		}
		if string(uid) != string(class.UID) {
			return fmt.Errorf("StorageClass UID changed after update")
		}
	}
	fmt.Printf("ok: StorageClass %s matches the %s configuration\n", class.Name, phase)
	return nil
}
