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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	quota "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkResourceQuotaLifecycle(ctx context.Context) (result error) {
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
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	r := &quota.ResourceQuotaResource{
		Metadata: quota.ServiceMetadata{GenerateName: new("unobin-quota-go-")},
		Spec:     &quota.ResourceQuotaSpec{Hard: &map[string]string{"pods": "4"}},
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	prior := runtime.Prior[
		quota.ResourceQuotaResource, *quota.ResourceQuotaResourceOutput, *config.Configuration,
	]{Inputs: *r, Outputs: created}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := r.Delete(ctx, nil, prior); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	if !strings.Contains(created.Name, "unobin-quota-go-") {
		return fmt.Errorf("expected matching generated name")
	}
	if !reflect.DeepEqual("unobin-quota-go-", created.Metadata.GenerateName) {
		return fmt.Errorf("unexpected created.Metadata.GenerateName")
	}
	if !reflect.DeepEqual(map[string]string{"pods": "4"}, created.Spec.Hard) {
		return fmt.Errorf("unexpected created.Spec.Hard")
	}
	if len(created.UID) == 0 {
		return fmt.Errorf("expected nonempty created.UID")
	}
	if !reflect.DeepEqual("default", created.Namespace) {
		return fmt.Errorf("unexpected created.Namespace")
	}
	observed, err := r.Read(ctx, nil, prior)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, observed) {
		return fmt.Errorf("unexpected observed")
	}
	r.Spec = &quota.ResourceQuotaSpec{Hard: &map[string]string{"pods": "6"}}
	prior.Observed = observed
	updated, err := r.Update(ctx, nil, prior)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if !reflect.DeepEqual(map[string]string{"pods": "6"}, updated.Spec.Hard) {
		return fmt.Errorf("unexpected updated.Spec.Hard")
	}
	quotas := api.CoreV1().ResourceQuotas("default")
	live, err := quotas.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(corev1.ResourceList{
		corev1.ResourcePods: resource.MustParse("6"),
	}, live.Status.Hard) {
		return fmt.Errorf("unexpected live.Status.Hard")
	}
	if err := r.Delete(ctx, nil, prior); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = quotas.Get(ctx, created.Name, metav1.GetOptions{})
	if !(apierrors.IsNotFound(err)) {
		return fmt.Errorf("expected apierrors.IsNotFound(err) to be true")
	}

	return nil
}
