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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/batch/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkCronJobAPILifecycle(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	name := "unobin-cron-job-" + strings.ToLower(rand.Text()[:10])
	input := &svc.CronJobResource{
		Metadata: svc.CronJobMetadata{Name: &name},
		Spec: svc.CronJobSpec{Schedule: "@hourly", Suspend: new(true),
			JobTemplate: svc.CronJobTemplate{Spec: svc.JobSpec{
				TTLSecondsAfterFinished: new("30"), Template: svc.PodTemplate{
					Spec: &svc.PodSpec{Container: new([]svc.Container{{Name: "app", Image: new("busybox:1.37"),
						Command: new([]string{"true"})}})},
				},
			}},
		}, DeleteTimeout: new("30s"),
	}
	created, err := input.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.CronJobResource, *svc.CronJobResourceOutput, *config.Configuration]{
		Inputs: *input, Outputs: created,
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := input.Delete(ctx, nil, p); err != nil {
				return fmt.Errorf("API check: %w", err)
			}
			return nil
		}())
	}()
	input.Metadata.Labels = new(map[string]string{"stage": "updated"})
	input.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = new("60")
	updated, err := input.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if !reflect.DeepEqual(new("60"), updated.Spec.JobTemplate.Spec.TTLSecondsAfterFinished) {
		return fmt.Errorf("unexpected updated.Spec.JobTemplate.Spec.TTLSecondsAfterFinished")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	live, err := api.BatchV1().CronJobs("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(new(int32(60)), live.Spec.JobTemplate.Spec.TTLSecondsAfterFinished) {
		return fmt.Errorf("unexpected live.Spec.JobTemplate.Spec.TTLSecondsAfterFinished")
	}
	p.Inputs, p.Outputs = *input, updated
	input.Metadata.Labels = nil
	updated, err = input.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if updated.Metadata.Labels != nil {
		return fmt.Errorf("expected nil updated.Metadata.Labels")
	}
	p.Inputs, p.Outputs = *input, updated
	if err := input.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = input.Read(ctx, nil, p)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}

	return nil
}
