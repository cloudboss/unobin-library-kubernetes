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
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type daemonSetPrior = runtime.Prior[
	svc.DaemonSetResource, *svc.DaemonSetResourceOutput, *config.Configuration,
]

func daemonSetAPI(ctx context.Context) (typedappsv1.DaemonSetInterface, error) {
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
	return api.AppsV1().DaemonSets("default"), nil
}

func daemonSetInput(ctx context.Context) *svc.DaemonSetResource {
	name := "unobin-daemon-set-" + strings.ToLower(rand.Text()[:10])
	return &svc.DaemonSetResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.DaemonSetSpec{
			Selector: &svc.Selector{MatchLabels: new(map[string]string{"app": name})},
			Template: svc.DaemonSetPodTemplate{
				Metadata: svc.Metadata{Labels: new(map[string]string{"app": name})},
				Spec: &svc.PodSpec{Container: new([]svc.Container{{
					Name: "app", Image: new("registry.k8s.io/pause:3.9"),
				}})},
			},
		},
		CreateTimeout: new("3m"), UpdateTimeout: new("3m"), DeleteTimeout: new("30s"),
	}
}

func checkDaemonSetAPILifecycle(ctx context.Context) (result error) {
	api, err := daemonSetAPI(ctx)
	if err != nil {
		return err
	}
	r := daemonSetInput(ctx)
	r.WaitForRollout = new(false)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := daemonSetPrior{Inputs: *r, Outputs: created}
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
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
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
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("registry.k8s.io/pause:3.10", live.Spec.Template.Spec.Containers[0].Image) {
		return fmt.Errorf("unexpected live.Spec.Template.Spec.Containers[0].Image")
	}
	p = daemonSetPrior{Inputs: *r, Outputs: updated}
	read, err = r.Read(ctx, nil, p)
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

func checkDaemonSetControllerRollout(ctx context.Context) (result error) {
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		return errRequiresNodes
	}
	api, err := daemonSetAPI(ctx)
	if err != nil {
		return err
	}
	r := daemonSetInput(ctx)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := daemonSetPrior{Inputs: *r, Outputs: created}
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
	if err := assertReadyDaemonSet(ctx, live); err != nil {
		return err
	}
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
	if err := assertReadyDaemonSet(ctx, live); err != nil {
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
				if !reflect.DeepEqual("registry.k8s.io/pause:3.10", pod.Spec.Containers[0].Image) {
					return fmt.Errorf("unexpected pod.Spec.Containers[0].Image")
				}
				if !reflect.DeepEqual("updated", pod.Annotations["rollout"]) {
					return fmt.Errorf("unexpected pod.Annotations[\"rollout\"]")
				}
				ready++
			}
		}
	}
	if !reflect.DeepEqual(int(live.Status.DesiredNumberScheduled), ready) {
		return fmt.Errorf("unexpected ready")
	}
	p = daemonSetPrior{Inputs: *r, Outputs: updated}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}

	return nil
}

func assertReadyDaemonSet(ctx context.Context, obj *appsv1.DaemonSet) (result error) {
	if !reflect.DeepEqual(obj.Generation, obj.Status.ObservedGeneration) {
		return fmt.Errorf("unexpected obj.Status.ObservedGeneration")
	}
	if obj.Status.DesiredNumberScheduled <= 0 {
		return fmt.Errorf("expected positive obj.Status.DesiredNumberScheduled")
	}
	if !reflect.DeepEqual(obj.Status.DesiredNumberScheduled, obj.Status.NumberReady) {
		return fmt.Errorf("unexpected obj.Status.NumberReady")
	}

	return nil
}

func checkDaemonSetOnDeleteProjection(ctx context.Context) (result error) {
	api, err := daemonSetAPI(ctx)
	if err != nil {
		return err
	}
	r := daemonSetInput(ctx)
	r.WaitForRollout = new(false)
	r.Spec.Strategy = &svc.DaemonSetStrategy{
		Type: new("OnDelete"),
		RollingUpdate: &svc.DaemonSetRollingUpdate{
			MaxSurge: new("0"), MaxUnavailable: new("0"),
		},
	}
	created, err := r.Create(ctx, nil)
	if created != nil {
		p := daemonSetPrior{Inputs: *r, Outputs: created}
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
	}
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if created == nil {
		return fmt.Errorf("expected nonnil created")
	}
	if !reflect.DeepEqual(*r.Metadata.Name, created.Name) {
		return fmt.Errorf("unexpected created.Name")
	}
	if !reflect.DeepEqual("default", created.Namespace) {
		return fmt.Errorf("unexpected created.Namespace")
	}
	if len(created.UID) == 0 {
		return fmt.Errorf("expected nonempty created.UID")
	}
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(live.Name, created.Name) {
		return fmt.Errorf("unexpected created.Name")
	}
	if !reflect.DeepEqual(live.Namespace, created.Namespace) {
		return fmt.Errorf("unexpected created.Namespace")
	}
	if !reflect.DeepEqual(string(live.UID), created.UID) {
		return fmt.Errorf("unexpected created.UID")
	}
	if !reflect.DeepEqual(appsv1.OnDeleteDaemonSetStrategyType, live.Spec.UpdateStrategy.Type) {
		return fmt.Errorf("unexpected live.Spec.UpdateStrategy.Type")
	}
	strategy := &svc.DaemonSetStrategy{Type: new(string(live.Spec.UpdateStrategy.Type))}
	if rolling := live.Spec.UpdateStrategy.RollingUpdate; rolling != nil {
		strategy.RollingUpdate = &svc.DaemonSetRollingUpdate{
			MaxSurge:       new(rolling.MaxSurge.String()),
			MaxUnavailable: new(rolling.MaxUnavailable.String()),
		}
	}
	if !reflect.DeepEqual(strategy, created.Spec.Strategy) {
		return fmt.Errorf("unexpected created.Spec.Strategy")
	}
	read, err := r.Read(ctx, nil, daemonSetPrior{Inputs: *r, Outputs: created})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}

	return nil
}
