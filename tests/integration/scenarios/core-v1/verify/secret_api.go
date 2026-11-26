package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkSecretLifecycle(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	namespace, err := client.Namespace(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r := &svc.SecretResource{Object: svc.SecretObject{
		Metadata:   svc.SecretMetadata{GenerateName: new("unobin-secret-api-"), Namespace: &namespace},
		Data:       &map[string]string{"value": "first", "overlap": "text"},
		BinaryData: &map[string]string{"bytes": "AP8=", "overlap": "AAE=", "empty": ""},
	}}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.SecretResource, *svc.SecretResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := r.Delete(ctx, nil, p); err != nil {
				return fmt.Errorf("API check: %w", err)
			}
			return nil
		}())
	}()
	if !reflect.DeepEqual("dGV4dA==", created.Object.BinaryData["overlap"]) {
		return fmt.Errorf("unexpected created.Object.BinaryData[\"overlap\"]")
	}
	if !reflect.DeepEqual("AP8=", created.Object.BinaryData["bytes"]) {
		return fmt.Errorf("unexpected created.Object.BinaryData[\"bytes\"]")
	}
	_, err = api.CoreV1().Secrets(namespace).Patch(ctx, created.Object.Metadata.Name,
		types.MergePatchType, []byte(`{"data":{"external":"ZXh0cmE="}}`), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p.Observed, err = r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r.Object.Data = &map[string]string{"value": "updated"}
	r.Object.BinaryData = &map[string]string{"bytes": "AgM="}
	r.Object.Immutable = new(true)
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.Object.Metadata.UID, updated.Object.Metadata.UID) {
		return fmt.Errorf("unexpected updated.Object.Metadata.UID")
	}
	if !(updated.Object.Immutable) {
		return fmt.Errorf("expected updated.Object.Immutable to be true")
	}
	if !reflect.DeepEqual(*r.Object.Data, updated.Object.Data) {
		return fmt.Errorf("unexpected updated.Object.Data")
	}
	if !reflect.DeepEqual(*r.Object.BinaryData, updated.Object.BinaryData) {
		return fmt.Errorf("unexpected updated.Object.BinaryData")
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = r.Read(ctx, nil, p)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}
	replacement, err := api.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: created.Object.Metadata.Name, Namespace: namespace},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := api.CoreV1().Secrets(namespace).Delete(ctx, replacement.Name,
				metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &replacement.UID}}); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	_, err = r.Read(ctx, nil, p)
	if err == nil || !strings.Contains(err.Error(), "UID changed") {
		return fmt.Errorf("expected error containing %q, got %v", "UID changed", err)
	}
	err = r.Delete(ctx, nil, p)
	if !(apierrors.IsConflict(err)) {
		return fmt.Errorf("expected apierrors.IsConflict(err) to be true")
	}

	return nil
}
