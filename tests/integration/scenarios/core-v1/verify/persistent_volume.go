package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyPersistentVolume() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid phase %q", phase)
	}
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	volumes := api.CoreV1().PersistentVolumes()
	for _, test := range []struct {
		name     string
		path     string
		capacity string
		replaced bool
	}{
		{"unobin-it-pv-update", "/unobin/update", "1Gi", false},
		{"unobin-it-pv-replacement", "/unobin/first", "1Gi", true},
	} {
		live, err := volumes.Get(ctx, test.name, metav1.GetOptions{})
		if phase == "destroyed" && apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if phase == "destroyed" {
			return fmt.Errorf("PersistentVolume %q still exists", test.name)
		}
		if phase == "updated" {
			if test.replaced {
				test.path = "/unobin/second"
			} else {
				test.capacity = "2Gi"
			}
		}
		if live.Name != test.name || live.UID == "" || live.Spec.HostPath == nil ||
			live.Spec.HostPath.Path != test.path || live.Status.Phase != corev1.VolumeAvailable ||
			live.Spec.Capacity.Storage().String() != test.capacity ||
			!slices.Equal(live.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			}) {
			return fmt.Errorf("PersistentVolume %q identity, phase, or spec differs", test.name)
		}
		file := filepath.Join(dir, test.name)
		if phase == "applied" {
			if err := os.WriteFile(file, []byte(live.UID), 0600); err != nil {
				return err
			}
			continue
		}
		uid, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if (string(live.UID) != string(uid)) != test.replaced {
			return fmt.Errorf("PersistentVolume %q UID change differs", test.name)
		}
	}
	return nil
}
