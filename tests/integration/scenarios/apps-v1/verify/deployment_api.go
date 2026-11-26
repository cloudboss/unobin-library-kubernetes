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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type deploymentPrior = runtime.Prior[
	svc.DeploymentResource, *svc.DeploymentResourceOutput, *config.Configuration,
]

func deploymentAPI(ctx context.Context) (typedappsv1.DeploymentInterface, error) {
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
	return api.AppsV1().Deployments("default"), nil
}

func deploymentInput(ctx context.Context) *svc.DeploymentResource {
	name := "unobin-deployment-" + strings.ToLower(rand.Text()[:10])
	return &svc.DeploymentResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.DeploymentSpec{
			Replicas: new("1"),
			Selector: &svc.Selector{MatchLabels: new(map[string]string{"app": name})},
			Template: svc.PodTemplate{
				Metadata: svc.Metadata{Labels: new(map[string]string{"app": name})},
				Spec: svc.PodSpec{Container: new([]svc.Container{{
					Name: "app", Image: new("registry.k8s.io/pause:3.9"),
				}})},
			},
		},
		CreateTimeout: new("3m"), UpdateTimeout: new("3m"), DeleteTimeout: new("30s"),
	}
}

func checkDeploymentAPILifecycle(ctx context.Context) (result error) {
	api, err := deploymentAPI(ctx)
	if err != nil {
		return err
	}
	r := deploymentInput(ctx)
	r.WaitForRollout = new(false)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := deploymentPrior{Inputs: *r, Outputs: created}
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
		return fmt.Errorf("deployment update lost the external restart annotation")
	}
	p = deploymentPrior{Inputs: *r, Outputs: updated}
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
		return fmt.Errorf("update Deployment metadata with restart annotation: %w", err)
	}
	p = deploymentPrior{Inputs: *r, Outputs: updated}
	live, err = api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.Generation != generation ||
		live.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] !=
			"2026-11-26T00:00:00Z" {
		return fmt.Errorf("deployment metadata update changed its template")
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

func checkDeploymentControllerRollout(ctx context.Context) (result error) {
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		return errRequiresNodes
	}
	api, err := deploymentAPI(ctx)
	if err != nil {
		return err
	}
	r := deploymentInput(ctx)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := deploymentPrior{Inputs: *r, Outputs: created}
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
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if err := assertReadyDeployment(ctx, live, 1); err != nil {
		return err
	}
	r.Spec.Replicas = new("2")
	r.Spec.Template.Spec.Container = new([]svc.Container{{
		Name: "app", Image: new("registry.k8s.io/pause:3.10"),
	}})
	r.Spec.Template.Metadata.Annotations = new(map[string]string{"rollout": "updated"})
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	live, err = api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if err := assertReadyDeployment(ctx, live, 2); err != nil {
		return err
	}
	if !reflect.DeepEqual("registry.k8s.io/pause:3.10", live.Spec.Template.Spec.Containers[0].Image) {
		return fmt.Errorf("unexpected live.Spec.Template.Spec.Containers[0].Image")
	}
	core, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	pods, err := core.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + created.Name,
	})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	ready := 0
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" &&
				pod.DeletionTimestamp == nil {
				ready++
			}
		}
	}
	if !reflect.DeepEqual(2, ready) {
		return fmt.Errorf("unexpected ready")
	}
	p = deploymentPrior{Inputs: *r, Outputs: updated}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}

	return nil
}

func assertReadyDeployment(
	ctx context.Context, obj *appsv1.Deployment, replicas int32,
) (result error) {
	if !reflect.DeepEqual(obj.Generation, obj.Status.ObservedGeneration) {
		return fmt.Errorf("unexpected obj.Status.ObservedGeneration")
	}
	if !reflect.DeepEqual(replicas, obj.Status.UpdatedReplicas) {
		return fmt.Errorf("unexpected obj.Status.UpdatedReplicas")
	}
	if !reflect.DeepEqual(replicas, obj.Status.ReadyReplicas) {
		return fmt.Errorf("unexpected obj.Status.ReadyReplicas")
	}
	if !reflect.DeepEqual(replicas, obj.Status.AvailableReplicas) {
		return fmt.Errorf("unexpected obj.Status.AvailableReplicas")
	}

	return nil
}
