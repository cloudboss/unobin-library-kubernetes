package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkConfiguration(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	version, err := api.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if len(version.GitVersion) == 0 {
		return fmt.Errorf("expected nonempty version.GitVersion")
	}
	namespace, err := client.Namespace(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	maps := api.CoreV1().ConfigMaps(namespace)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	created, err := maps.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "unobin-config-"},
		Data:       map[string]string{"value": "created"},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := maps.Delete(ctx, created.Name, metav1.DeleteOptions{})
			if !apierrors.IsNotFound(err) {
				if err != nil {
					return fmt.Errorf("API check: %w", err)
				}
			}

			return nil
		}())
	}()
	read, err := maps.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, read.UID) {
		return fmt.Errorf("unexpected read.UID")
	}
	if !reflect.DeepEqual(map[string]string{"value": "created"}, read.Data) {
		return fmt.Errorf("unexpected read.Data")
	}
	dynamic, err := client.NewDynamic(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	resource, resolvedNamespace, err := dynamic.Resolve(
		schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(namespace, resolvedNamespace) {
		return fmt.Errorf("unexpected resolvedNamespace")
	}
	object, err := resource.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, object.GetUID()) {
		return fmt.Errorf("unexpected object.GetUID()")
	}
	read.Data["value"] = "updated"
	_, err = maps.Update(ctx, read, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	read, err = maps.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(map[string]string{"value": "updated"}, read.Data) {
		return fmt.Errorf("unexpected read.Data")
	}
	if err := maps.Delete(ctx, created.Name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = maps.Get(ctx, created.Name, metav1.GetOptions{})
	if !(apierrors.IsNotFound(err)) {
		return fmt.Errorf("expected apierrors.IsNotFound(err) to be true")
	}

	return nil
}
