package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkServiceLifecycle(ctx context.Context) (result error) {
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
	r := &svc.ServiceResource{Metadata: svc.ServiceMetadata{GenerateName: new("unobin-service-")},
		Spec: svc.ServiceSpec{Selector: &map[string]string{"app": "first"},
			Port: &[]svc.ServicePort{{Port: 80}}}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.ServiceResource, *svc.ServiceResourceOutput, *config.Configuration]{
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
	if len(created.UID) == 0 {
		return fmt.Errorf("expected nonempty created.UID")
	}
	if len(created.Spec.ClusterIP) == 0 {
		return fmt.Errorf("expected nonempty created.Spec.ClusterIP")
	}
	if !reflect.DeepEqual([]string{created.Spec.ClusterIP}, created.Spec.ClusterIps) {
		return fmt.Errorf("unexpected created.Spec.ClusterIps")
	}
	if !reflect.DeepEqual("80", created.Spec.Port[0].TargetPort) {
		return fmt.Errorf("unexpected created.Spec.Port[0].TargetPort")
	}
	observed, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, observed) {
		return fmt.Errorf("unexpected observed")
	}
	p.Observed = observed
	r.Spec.Selector = &map[string]string{"app": "updated"}
	r.Spec.Port = &[]svc.ServicePort{{Port: 80, Name: new("http")}}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if !reflect.DeepEqual(created.Spec.ClusterIP, updated.Spec.ClusterIP) {
		return fmt.Errorf("unexpected updated.Spec.ClusterIP")
	}
	if !reflect.DeepEqual(created.Spec.Port[0].TargetPort, updated.Spec.Port[0].TargetPort) {
		return fmt.Errorf("unexpected updated.Spec.Port[0].TargetPort")
	}
	live, err := api.CoreV1().Services("default").Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(map[string]string{"app": "updated"}, live.Spec.Selector) {
		return fmt.Errorf("unexpected live.Spec.Selector")
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = api.CoreV1().Services("default").Get(ctx, created.Name, metav1.GetOptions{})
	if !(apierrors.IsNotFound(err)) {
		return fmt.Errorf("expected apierrors.IsNotFound(err) to be true")
	}

	return nil
}
