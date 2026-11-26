package main

import (
	"context"
	"crypto/rand"
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
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type configMapPrior = runtime.Prior[
	svc.ConfigMapResource, *svc.ConfigMapResourceOutput, *config.Configuration,
]

func configMapAPI(ctx context.Context) (typedcorev1.ConfigMapInterface, string, error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return nil, "", fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return nil, "", fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.New(nil)
	if err != nil {
		return nil, "", fmt.Errorf("API check: %w", err)
	}
	namespace, err := client.Namespace(nil)
	if err != nil {
		return nil, "", fmt.Errorf("API check: %w", err)
	}
	return api.CoreV1().ConfigMaps(namespace), namespace, nil
}

func generatedConfigMap(namespace string) *svc.ConfigMapResource {
	return &svc.ConfigMapResource{Object: svc.ConfigMapObject{
		Metadata: svc.ConfigMapMetadata{
			GenerateName: new("unobin-config-map-" + strings.ToLower(rand.Text()[:10]) + "-"),
			Namespace:    &namespace,
		},
		Data:       &map[string]string{"value": "first", "removed": "initial", "empty-text": ""},
		BinaryData: &map[string]string{"bytes": "AAE=\n", "empty": ""},
	}}
}

func checkConfigMapLifecycle(ctx context.Context) (result error) {
	api, namespace, err := configMapAPI(ctx)
	if err != nil {
		return err
	}
	r := generatedConfigMap(namespace)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := configMapPrior{Inputs: *r, Outputs: created}
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
	name := created.Object.Metadata.Name
	if !(strings.HasPrefix(name, *r.Object.Metadata.GenerateName)) {
		return fmt.Errorf("expected strings.HasPrefix(name, *r.Object.Metadata.GenerateName) to be true")
	}
	if !reflect.DeepEqual(namespace, created.Object.Metadata.Namespace) {
		return fmt.Errorf("unexpected created.Object.Metadata.Namespace")
	}
	if !reflect.DeepEqual(map[string]string{"bytes": "AAE=", "empty": ""}, created.Object.BinaryData) {
		return fmt.Errorf("unexpected created.Object.BinaryData")
	}
	if created.Object.Immutable {
		return fmt.Errorf("expected created.Object.Immutable to be false")
	}
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
	_, err = api.Patch(ctx, name, types.MergePatchType,
		[]byte(`{"data":{"external":"drift"},
			"metadata":{"annotations":{"kubernetes.io/hidden":"retained"}}}`),
		metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	read, err = r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("drift", read.Object.Data["external"]) {
		return fmt.Errorf("unexpected read.Object.Data[\"external\"]")
	}
	if _, exists := read.Object.Metadata.Annotations["kubernetes.io/hidden"]; exists {
		return fmt.Errorf("unexpected map key")
	}
	p.Observed = read
	r.Object.Data = &map[string]string{"value": "updated", "empty-text": ""}
	r.Object.BinaryData = &map[string]string{"bytes": "AgM=", "empty": ""}
	r.Object.Immutable = new(true)
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.Object.Metadata.UID, updated.Object.Metadata.UID) {
		return fmt.Errorf("unexpected updated.Object.Metadata.UID")
	}
	if !reflect.DeepEqual(*r.Object.Data, updated.Object.Data) {
		return fmt.Errorf("unexpected updated.Object.Data")
	}
	if !reflect.DeepEqual(*r.Object.BinaryData, updated.Object.BinaryData) {
		return fmt.Errorf("unexpected updated.Object.BinaryData")
	}
	if !(updated.Object.Immutable) {
		return fmt.Errorf("expected updated.Object.Immutable to be true")
	}
	live, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("retained", live.Annotations["kubernetes.io/hidden"]) {
		return fmt.Errorf("unexpected live.Annotations[\"kubernetes.io/hidden\"]")
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = r.Read(ctx, nil, p)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}

	return nil
}

func checkConfigMapDeleteWithFinalizer(ctx context.Context) (result error) {
	api, namespace, err := configMapAPI(ctx)
	if err != nil {
		return err
	}
	r := generatedConfigMap(namespace)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := configMapPrior{Inputs: *r, Outputs: created}
	name := created.Object.Metadata.Name
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, err := api.Patch(ctx, name, types.MergePatchType,
				[]byte(`{"metadata":{"finalizers":[]}}`), metav1.PatchOptions{})
			if !apierrors.IsNotFound(err) {
				if err != nil {
					return fmt.Errorf("API check: %w", err)
				}
			}
			if err := r.Delete(ctx, nil, p); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	_, err = api.Patch(ctx, name, types.MergePatchType,
		[]byte(`{"metadata":{"finalizers":["test.unobin.cloudboss.io/hold"]}}`), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	live, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if live.DeletionTimestamp == nil {
		return fmt.Errorf("expected nonnil live.DeletionTimestamp")
	}

	return nil
}

func checkConfigMapReplacementUIDProtection(ctx context.Context) (result error) {
	api, namespace, err := configMapAPI(ctx)
	if err != nil {
		return err
	}
	r := generatedConfigMap(namespace)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := configMapPrior{Inputs: *r, Outputs: created}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	replacement, err := api.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: created.Object.Metadata.Name, Namespace: namespace},
		Data:       map[string]string{"value": "replacement"}, Immutable: new(false),
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			err := api.Delete(ctx, replacement.Name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &replacement.UID},
			})
			if !apierrors.IsNotFound(err) {
				if err != nil {
					return fmt.Errorf("API check: %w", err)
				}
			}

			return nil
		}())
	}()
	_, err = r.Read(ctx, nil, p)
	if err == nil || !strings.Contains(err.Error(), "UID changed") {
		return fmt.Errorf("expected error containing %q, got %v", "UID changed", err)
	}
	r.Object.Data = &map[string]string{"value": "must-not-change"}
	_, err = r.Update(ctx, nil, p)
	if err == nil {
		return fmt.Errorf("expected an error")
	}
	err = r.Delete(ctx, nil, p)
	if !(apierrors.IsConflict(err)) {
		return fmt.Errorf("expected apierrors.IsConflict(err) to be true")
	}
	live, err := api.Get(ctx, replacement.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(replacement.UID, live.UID) {
		return fmt.Errorf("unexpected live.UID")
	}
	if !reflect.DeepEqual(map[string]string{"value": "replacement"}, live.Data) {
		return fmt.Errorf("unexpected live.Data")
	}

	return nil
}
