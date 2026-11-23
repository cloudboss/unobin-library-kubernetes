package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyPersistentVolumeClaim() error {
	phase := os.Getenv("VERIFY_PHASE")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid phase %q", phase)
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	claims := api.CoreV1().PersistentVolumeClaims("default")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nameFile, uidFile := filepath.Join(dir, "claim-name"), filepath.Join(dir, "claim-uid")
	if phase == "destroyed" {
		name, err := os.ReadFile(nameFile)
		if err != nil {
			return err
		}
		obj, err := claims.Get(ctx, string(name), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		uid, err := os.ReadFile(uidFile)
		if err != nil {
			return err
		}
		if string(obj.UID) == string(uid) {
			return fmt.Errorf("managed PVC still exists")
		}
		return nil
	}
	list, err := claims.List(ctx, metav1.ListOptions{LabelSelector: labels.Set{
		"test.unobin.cloudboss.io/scenario": "core-v1-persistent-volume-claim"}.String()})
	if err != nil {
		return err
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("expected one PVC, got %d", len(list.Items))
	}
	obj := list.Items[0]
	value := "first"
	if phase == "updated" {
		value = "updated"
	}
	if !strings.HasPrefix(obj.Name, "unobin-it-claim-") || obj.UID == "" ||
		obj.GenerateName != "unobin-it-claim-" || obj.Labels["value"] != value ||
		obj.Annotations["example.com/value"] != value || len(obj.Spec.AccessModes) != 1 ||
		obj.Spec.AccessModes[0] != "ReadWriteOnce" ||
		obj.Spec.Resources.Requests.Storage().String() != "1Gi" ||
		len(obj.Spec.Resources.Requests) != 1 || len(obj.Spec.Resources.Limits) != 0 {
		return fmt.Errorf("PVC identity, requested spec or metadata differs")
	}
	if phase == "applied" {
		if err := os.WriteFile(nameFile, []byte(obj.Name), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(uidFile, []byte(obj.UID), 0600); err != nil {
			return err
		}
	} else {
		uid, err := os.ReadFile(uidFile)
		if err != nil {
			return err
		}
		if string(uid) != string(obj.UID) {
			return fmt.Errorf("PVC UID changed")
		}
	}
	// The API-only harness has no protection controller to release this unused claim.
	if phase == "updated" && os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		_, err = claims.Patch(ctx, obj.Name, types.MergePatchType,
			[]byte(`{"metadata":{"finalizers":null}}`), metav1.PatchOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}
