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
	"k8s.io/apimachinery/pkg/types"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type statefulSetPrior = runtime.Prior[
	svc.StatefulSetResource, *svc.StatefulSetResourceOutput, *config.Configuration,
]

func statefulSetAPI(ctx context.Context) (typedappsv1.StatefulSetInterface, error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return nil, fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return nil, fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.New(nil)
	if err != nil {
		return nil, fmt.Errorf("API check: %w", err)
	}
	return api.AppsV1().StatefulSets("default"), nil
}

func statefulSetInput(ctx context.Context) *svc.StatefulSetResource {
	name := "unobin-stateful-set-" + strings.ToLower(rand.Text()[:10])
	return &svc.StatefulSetResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.StatefulSetSpec{
			ServiceName: name,
			Replicas:    new("1"),
			Selector:    svc.Selector{MatchLabels: new(map[string]string{"app": name})},
			Template: svc.StatefulSetPodTemplate{
				Metadata: svc.Metadata{Labels: new(map[string]string{"app": name})},
				Spec: &svc.StatefulSetPodSpec{Container: new([]svc.Container{{
					Name: "app", Image: new("registry.k8s.io/pause:3.9"),
				}})},
			},
		},
		CreateTimeout: new("3m"), UpdateTimeout: new("3m"), DeleteTimeout: new("30s"),
	}
}

func checkStatefulSetAPILifecycle(ctx context.Context) (result error) {
	api, err := statefulSetAPI(ctx)
	if err != nil {
		return err
	}
	r := statefulSetInput(ctx)
	r.WaitForRollout = new(false)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := statefulSetPrior{Inputs: *r, Outputs: created}
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
	_, err = api.Patch(ctx, created.Name, types.MergePatchType,
		[]byte(`{"spec":{"template":{"metadata":{"annotations":`+
			`{"kubectl.kubernetes.io/restartedAt":"2026-11-26T00:00:00Z"}}}}}`),
		metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("add external restart annotation: %w", err)
	}
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
	r.Spec.Replicas = new("2")
	r.Spec.Template.Spec.Container = new([]svc.Container{{
		Name: "app", Image: new("registry.k8s.io/pause:3.10"),
	}})
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if !reflect.DeepEqual("2", *updated.Spec.Replicas) {
		return fmt.Errorf("unexpected *updated.Spec.Replicas")
	}
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("registry.k8s.io/pause:3.10", live.Spec.Template.Spec.Containers[0].Image) {
		return fmt.Errorf("unexpected live.Spec.Template.Spec.Containers[0].Image")
	}
	if live.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] !=
		"2026-11-26T00:00:00Z" {
		return fmt.Errorf("stateful-set update lost the external restart annotation")
	}
	p = statefulSetPrior{Inputs: *r, Outputs: updated}
	read, err = r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(updated, read) {
		return fmt.Errorf("unexpected read")
	}
	generation := live.Generation
	r.Metadata.Labels = new(map[string]string{"example.com/managed": "new"})
	updated, err = r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("update StatefulSet metadata with restart annotation: %w", err)
	}
	p = statefulSetPrior{Inputs: *r, Outputs: updated}
	live, err = api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.Generation != generation ||
		live.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] !=
			"2026-11-26T00:00:00Z" {
		return fmt.Errorf("stateful-set metadata update changed its template")
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
