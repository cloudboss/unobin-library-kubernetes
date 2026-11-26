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

func checkJobAPILifecycle(ctx context.Context) (result error) {
	if err := testJobLifecycle(ctx, false); err != nil {
		return err
	}
	return nil
}
func checkJobControllerCompletion(ctx context.Context) (result error) {
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		return errRequiresNodes
	}
	if err := testJobLifecycle(ctx, true); err != nil {
		return err
	}

	return nil
}

func testJobLifecycle(ctx context.Context, waiting bool) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	name := "unobin-job-" + strings.ToLower(rand.Text()[:10])
	r := &svc.JobResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.JobSpec{Template: svc.PodTemplate{
			Metadata: svc.TemplateMetadata{},
			Spec: &svc.PodSpec{Container: new([]svc.Container{{
				Name: "app", Image: new("busybox:1.37"), Command: new([]string{"true"}),
			}})},
		}},
		WaitForCompletion: &waiting, DeleteTimeout: new("30s"),
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.JobResource, *svc.JobResourceOutput, *config.Configuration]{
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
	r.Spec.Parallelism = new(int64(2))
	r.Spec.BackoffLimit = new(int64(4))
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if !reflect.DeepEqual(int64(2), *updated.Spec.Parallelism) {
		return fmt.Errorf("unexpected *updated.Spec.Parallelism")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	live, err := api.BatchV1().Jobs("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(int32(4), *live.Spec.BackoffLimit) {
		return fmt.Errorf("unexpected *live.Spec.BackoffLimit")
	}
	p.Inputs = *r
	p.Outputs = updated
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = r.Read(ctx, nil, p)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}

	return nil
}
