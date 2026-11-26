package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	policy "github.com/cloudboss/unobin-library-kubernetes/internal/api/policy/v1"
)

func checkPodDisruptionBudgetLifecycle(ctx context.Context) (result error) {
	r := &policy.PodDisruptionBudgetResource{
		Metadata: policy.PodDisruptionBudgetMetadata{GenerateName: new("unobin-pdb-api-")},
		Spec:     policy.PodDisruptionBudgetSpec{MinAvailable: new("1")},
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[
		policy.PodDisruptionBudgetResource, *policy.PodDisruptionBudgetResourceOutput,
		*config.Configuration,
	]{Inputs: *r, Outputs: created}
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
	r.Metadata.Labels = &map[string]string{"test": "updated"}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if !reflect.DeepEqual(map[string]string{"test": "updated"}, updated.Metadata.Labels) {
		return fmt.Errorf("unexpected updated.Metadata.Labels")
	}
	p.Outputs = updated
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(updated, read) {
		return fmt.Errorf("unexpected read")
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
