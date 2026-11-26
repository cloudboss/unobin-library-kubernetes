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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkNamespaceLifecycle(ctx context.Context) (result error) {
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
	r := &svc.NamespaceResource{Object: svc.NamespaceObject{Metadata: svc.NamespaceMetadata{
		GenerateName: new("unobin-namespace-"),
		Labels:       &map[string]string{"app": "namespace-test", "value": "first"},
		Annotations:  &map[string]string{"example.com/value": "first"},
	}}, WaitForDefaultServiceAccount: new(true), DeleteTimeout: new("30s")}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.NamespaceResource, *svc.NamespaceResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := r.Delete(ctx, nil, p); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	name := created.Object.Metadata.Name
	if !(strings.HasPrefix(name, "unobin-namespace-")) {
		return fmt.Errorf("expected strings.HasPrefix(name, \"unobin-namespace-\") to be true")
	}
	if len(created.Object.Metadata.UID) == 0 {
		return fmt.Errorf("expected nonempty created.Object.Metadata.UID")
	}
	sa, err := api.CoreV1().ServiceAccounts(name).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("default", sa.Name) {
		return fmt.Errorf("unexpected sa.Name")
	}
	observed, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, observed) {
		return fmt.Errorf("unexpected observed")
	}
	p.Observed = observed
	r.Object.Metadata.Labels = &map[string]string{"app": "namespace-test", "value": "updated"}
	r.Object.Metadata.Annotations = &map[string]string{"example.com/value": "updated"}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.Object.Metadata.UID, updated.Object.Metadata.UID) {
		return fmt.Errorf("unexpected updated.Object.Metadata.UID")
	}
	live, err := api.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("updated", live.Labels["value"]) {
		return fmt.Errorf("unexpected live.Labels[\"value\"]")
	}
	if !reflect.DeepEqual("updated", live.Annotations["example.com/value"]) {
		return fmt.Errorf("unexpected live.Annotations[\"example.com/value\"]")
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = api.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if !(apierrors.IsNotFound(err)) {
		return fmt.Errorf("expected apierrors.IsNotFound(err) to be true")
	}

	return nil
}
