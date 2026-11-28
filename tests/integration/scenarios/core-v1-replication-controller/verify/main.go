package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type prior = runtime.Prior[
	svc.ReplicationControllerResource, *svc.ReplicationControllerResourceOutput, *config.Configuration,
]

const controllerName = "unobin-it-replication-controller"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if os.Getenv("UNOBIN_KUBERNETES_INTEGRATION") != "1" || os.Getenv("KUBECONFIG") == "" {
		return fmt.Errorf("integration requires an explicit test cluster")
	}
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	manual := os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1"
	if phase == "applied" {
		if err := protocol(ctx, api, manual); err != nil {
			return err
		}
		if manual {
			fmt.Println("ok: API-only lifecycle with manual fully-labelled status, drain and GC simulation")
		} else {
			fmt.Println("ok: controller-created Pods, scale, replacement UID and Pod drain")
		}
	}
	controllers := api.CoreV1().ReplicationControllers("default")
	live, err := controllers.Get(ctx, controllerName, metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("controller still exists after destroy: %v", err)
		}
		return requirePods(ctx, api, controllerName, "", 0)
	}
	if err != nil {
		return err
	}
	desired := int32(1)
	image := "registry.k8s.io/pause:3.9"
	if phase == "updated" {
		desired = 2
		image = "registry.k8s.io/pause:3.10"
	}
	if manual {
		desired = 0
	}
	if live.Spec.Replicas == nil || *live.Spec.Replicas != desired ||
		live.Status.FullyLabeledReplicas != desired ||
		live.Spec.Template.Spec.Containers[0].Image != image {
		return fmt.Errorf("unexpected factory controller spec or status: %+v", live)
	}
	uidFile := filepath.Join(dir, "replication-controller-uid")
	switch phase {
	case "applied":
		if err := os.WriteFile(uidFile, []byte(live.UID), 0o600); err != nil {
			return err
		}
	case "updated":
		uid, err := os.ReadFile(uidFile)
		if err != nil {
			return err
		}
		if string(uid) == string(live.UID) {
			return fmt.Errorf("image edit did not replace controller UID")
		}
	default:
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	if !manual {
		return requirePods(ctx, api, controllerName, string(live.UID), int(desired))
	}
	return nil
}

func protocol(ctx context.Context, api kubernetes.Interface, manual bool) (result error) {
	name := "unobin-rc-protocol-" + strings.ToLower(rand.Text()[:8])
	stop := func() error { return nil }
	if manual {
		stop = simulate(ctx, api, name)
	}
	defer func() { result = errors.Join(result, stop()) }()
	r := svc.ReplicationControllerResource{
		Metadata: svc.ServiceMetadata{Name: &name},
		Spec: svc.ReplicationControllerSpec{Selector: map[string]string{},
			Template: svc.ReplicationControllerTemplate{
				Metadata: svc.ReplicationControllerTemplateMetadata{Labels: new(map[string]string{
					"app": name})},
				Spec: svc.ReplicationControllerPodSpec{Container: new([]svc.PodContainer{{
					Name: "app", Image: new("registry.k8s.io/pause:3.9"),
				}})},
			}}, CreateTimeout: new("2m"), UpdateTimeout: new("2m"), DeleteTimeout: new("2m"),
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return err
	}
	p := prior{Inputs: r, Outputs: created}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		result = errors.Join(result, r.Delete(cleanup, nil, p))
	}()
	if created.UID == "" || !maps.Equal(created.Spec.Selector, map[string]string{"app": name}) {
		return fmt.Errorf("missing UID or API-defaulted selector")
	}
	if !manual {
		if err := requirePods(ctx, api, name, created.UID, 1); err != nil {
			return err
		}
	}
	r.Spec.Replicas = new(int64(2))
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return err
	}
	if updated.UID != created.UID || *updated.Spec.Replicas != 2 {
		return fmt.Errorf("scale did not preserve controller identity")
	}
	p = prior{Inputs: r, Outputs: updated}
	if !manual {
		if err := requirePods(ctx, api, name, created.UID, 2); err != nil {
			return err
		}
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return err
	}
	if err := requirePods(ctx, api, name, created.UID, 0); err != nil {
		return err
	}
	(*r.Spec.Template.Spec.Container)[0].Image = new("registry.k8s.io/pause:3.10")
	replacement, err := r.Create(ctx, nil)
	if err != nil {
		return err
	}
	p = prior{Inputs: r, Outputs: replacement}
	if replacement.UID == created.UID {
		return fmt.Errorf("replacement retained old UID")
	}
	if !manual {
		return requirePods(ctx, api, name, replacement.UID, 2)
	}
	return nil
}

func requirePods(ctx context.Context, api kubernetes.Interface, name, uid string, count int) error {
	return wait.PollUntilContextCancel(ctx, 50*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			pods, err := api.CoreV1().Pods("default").List(ctx,
				metav1.ListOptions{LabelSelector: "app=" + name})
			if err != nil {
				return false, err
			}
			actual := 0
			for _, pod := range pods.Items {
				for _, owner := range pod.OwnerReferences {
					if owner.Kind == "ReplicationController" && (uid == "" || string(owner.UID) == uid) {
						actual++
						break
					}
				}
			}
			return actual == count, nil
		})
}

func simulate(ctx context.Context, api kubernetes.Interface, name string) func() error {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		var result error
		defer func() { done <- result }()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		controllers := api.CoreV1().ReplicationControllers("default")
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				live, err := controllers.Get(ctx, name, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					continue
				}
				if err != nil {
					if ctx.Err() == nil {
						result = err
					}
					return
				}
				if live.DeletionTimestamp != nil && len(live.Finalizers) > 0 {
					_, err = controllers.Patch(ctx, name, types.MergePatchType,
						[]byte(`{"metadata":{"finalizers":[]}}`),
						metav1.PatchOptions{})
				} else if live.Spec.Replicas != nil && live.Status.FullyLabeledReplicas != *live.Spec.Replicas {
					live.Status.FullyLabeledReplicas = *live.Spec.Replicas
					live.Status.Replicas = *live.Spec.Replicas
					_, err = controllers.UpdateStatus(ctx, live, metav1.UpdateOptions{})
				}
				if err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
					if ctx.Err() == nil {
						result = err
					}
					return
				}
			}
		}
	}()
	return func() error { cancel(); return <-done }
}
