package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const name = "unobin-it-config-map-info"

func verifyConfigMapLookup() error {
	phase := os.Getenv("VERIFY_PHASE")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
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
	obj, err := api.CoreV1().ConfigMaps("default").Get(ctx, name, metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected deleted ConfigMap, got %v", err)
		}
	} else {
		if err != nil {
			return err
		}
		value, binary := "first", []byte{0, 1}
		if phase == "updated" {
			value, binary = "updated", []byte{255, 0}
		}
		if obj.UID == "" || !maps.Equal(obj.Data, map[string]string{
			"value": value, "unicode": "雪", "empty-text": "",
		}) || !maps.EqualFunc(obj.BinaryData, map[string][]byte{
			"bytes": binary, "empty-bytes": {},
		}, bytes.Equal) {
			return fmt.Errorf("ConfigMap content differs from the %s configuration", phase)
		}
		path := filepath.Join(dir, "config-map-info-uid")
		if phase == "applied" {
			if err := os.WriteFile(path, []byte(obj.UID), 0o600); err != nil {
				return err
			}
		} else {
			previous, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(previous) != string(obj.UID) {
				return fmt.Errorf("ConfigMap identity changed during update")
			}
		}
		lookup := &svc.ConfigMapDataSource{Metadata: svc.ConfigMapLookupMetadata{
			Name: new(name), Namespace: new("default"),
		}, Immutable: new(true)}
		out, err := lookup.Read(ctx, nil)
		if err != nil {
			return err
		}
		if out.Metadata.UID != string(obj.UID) || out.Immutable || !maps.Equal(out.Data, obj.Data) {
			return fmt.Errorf("ConfigMap lookup differs from the remote object")
		}
	}
	lookup := &svc.ConfigMapDataSource{Metadata: svc.ConfigMapLookupMetadata{
		Name: new(name + "-absent"), Namespace: new("default"),
	}, Immutable: new(true)}
	missing, err := lookup.Read(ctx, nil)
	if err != nil {
		return err
	}
	if missing.Metadata.Name != name+"-absent" || !missing.Immutable ||
		!maps.Equal(missing.Data, map[string]string{}) ||
		!maps.Equal(missing.BinaryData, map[string]string{}) {
		return fmt.Errorf("missing ConfigMap lookup differs from its fallback values")
	}
	fmt.Printf("ok: ConfigMap lookup matches the %s configuration\n", phase)
	return nil
}
