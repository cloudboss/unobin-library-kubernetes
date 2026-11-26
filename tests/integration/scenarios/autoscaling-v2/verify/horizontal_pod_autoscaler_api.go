package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/autoscaling/v2"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkHPAAcceptanceLifecycle(ctx context.Context) (result error) {
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
	r := &svc.HorizontalPodAutoscalerResource{Object: svc.HPAObject{Metadata: svc.HPAMetadata{
		GenerateName: new("unobin-hpa-api-")}, Spec: svc.HPASpec{MaxReplicas: 4,
		ScaleTargetRef: svc.HPATargetRef{APIVersion: new("apps/v1"), Kind: "Deployment",
			Name: "absent-target"}}}}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.HorizontalPodAutoscalerResource,
		*svc.HorizontalPodAutoscalerResourceOutput, *config.Configuration]{Inputs: *r, Outputs: created}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			live, err := api.AutoscalingV2().HorizontalPodAutoscalers("default").Get(
				ctx, created.Object.Metadata.Name, metav1.GetOptions{},
			)
			if err == nil {
				live.Finalizers = nil
				_, err = api.AutoscalingV2().HorizontalPodAutoscalers("default").Update(
					ctx, live, metav1.UpdateOptions{},
				)
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
	if !reflect.DeepEqual("default", created.Object.Metadata.Namespace) {
		return fmt.Errorf("unexpected created.Object.Metadata.Namespace")
	}
	if len(*created.Object.Spec.Metric) == 0 {
		return fmt.Errorf("expected nonempty *created.Object.Spec.Metric")
	}
	p.Observed, err = r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r.Object.Spec.MaxReplicas = 6
	r.Object.Spec.MinReplicas = new(2)
	r.Object.Metadata.Labels = &map[string]string{"app": "updated"}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.Object.Metadata.UID, updated.Object.Metadata.UID) {
		return fmt.Errorf("unexpected updated.Object.Metadata.UID")
	}
	if !reflect.DeepEqual(6, updated.Object.Spec.MaxReplicas) {
		return fmt.Errorf("unexpected updated.Object.Spec.MaxReplicas")
	}
	if !reflect.DeepEqual(new(2), updated.Object.Spec.MinReplicas) {
		return fmt.Errorf("unexpected updated.Object.Spec.MinReplicas")
	}
	live, err := api.AutoscalingV2().HorizontalPodAutoscalers("default").Get(ctx,
		created.Object.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	live.Finalizers = []string{"example.com/hold"}
	_, err = api.AutoscalingV2().HorizontalPodAutoscalers("default").Update(ctx, live,
		metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	live, err = api.AutoscalingV2().HorizontalPodAutoscalers("default").Get(ctx,
		created.Object.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if live.DeletionTimestamp == nil {
		return fmt.Errorf("expected nonnil live.DeletionTimestamp")
	}

	return nil
}
