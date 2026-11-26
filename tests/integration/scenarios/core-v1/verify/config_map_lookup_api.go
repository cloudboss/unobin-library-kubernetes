package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkConfigMapInfoReadOnly(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	obj, err := api.CoreV1().ConfigMaps("default").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "unobin-it-config-map-info-read-"},
		Data:       map[string]string{"empty": "", "unicode": "雪"},
		BinaryData: map[string][]byte{"bytes": {0, 255}}, Immutable: new(true),
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := api.CoreV1().ConfigMaps("default").Delete(
				ctx, obj.Name, metav1.DeleteOptions{}); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	r := &svc.ConfigMapDataSource{Metadata: svc.ConfigMapLookupMetadata{Name: &obj.Name,
		Annotations: &map[string]string{"EXAMPLE.COM/Lookup": "not-a-selector"},
		Labels:      &map[string]string{"lookup": "not-a-selector"}}, Immutable: new(false)}
	out, err := r.Read(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(string(obj.UID), out.Metadata.UID) {
		return fmt.Errorf("unexpected out.Metadata.UID")
	}
	if !reflect.DeepEqual(obj.Data, out.Data) {
		return fmt.Errorf("unexpected out.Data")
	}
	if !reflect.DeepEqual(map[string]string{"bytes": "AP8="}, out.BinaryData) {
		return fmt.Errorf("unexpected out.BinaryData")
	}
	if !(out.Immutable) {
		return fmt.Errorf("expected out.Immutable to be true")
	}
	r.Metadata.Name = new("unobin-it-config-map-info-absent")
	missing, err := r.Read(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("unobin-it-config-map-info-absent", missing.Metadata.Name) {
		return fmt.Errorf("unexpected missing.Metadata.Name")
	}
	if !reflect.DeepEqual("default", missing.Metadata.Namespace) {
		return fmt.Errorf("unexpected missing.Metadata.Namespace")
	}
	if !reflect.DeepEqual(map[string]string{}, missing.Data) {
		return fmt.Errorf("unexpected missing.Data")
	}
	if !reflect.DeepEqual(map[string]string{}, missing.BinaryData) {
		return fmt.Errorf("unexpected missing.BinaryData")
	}
	if missing.Immutable {
		return fmt.Errorf("expected missing.Immutable to be false")
	}
	after, err := api.CoreV1().ConfigMaps("default").Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(obj, after) {
		return fmt.Errorf("unexpected after")
	}

	return nil
}
