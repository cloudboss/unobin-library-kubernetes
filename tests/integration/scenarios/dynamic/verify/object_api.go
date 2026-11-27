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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type objectPrior = runtime.Prior[
	svc.ObjectResource, *svc.ObjectResourceOutput, *config.Configuration,
]

func objectAPI(ctx context.Context) (dynamic.ResourceInterface, string, error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return nil, "", fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return nil, "", fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.NewDynamic(nil)
	if err != nil {
		return nil, "", fmt.Errorf("API check: %w", err)
	}
	resource, namespace, err := api.Resolve(
		schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "")
	if err != nil {
		return nil, "", fmt.Errorf("API check: %w", err)
	}
	return resource, namespace, nil
}

func configMap(ctx context.Context) (*svc.ObjectResource, error) {
	manifest := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "unobin-object-" + strings.ToLower(rand.Text()[:10])},
		"data":     map[string]any{"value": "first", "removed": "initial"},
	}
	return &svc.ObjectResource{Manifest: manifest, DeleteTimeout: new("5s")}, nil
}

func editManifest(
	ctx context.Context, r *svc.ObjectResource, change func(map[string]any),
) (result error) {
	manifest := (&unstructured.Unstructured{Object: r.Manifest.(map[string]any)}).DeepCopy().Object
	change(manifest)
	r.Manifest = manifest

	return nil
}

func checkObjectLifecycle(ctx context.Context) (result error) {
	api, namespace, err := objectAPI(ctx)
	if err != nil {
		return err
	}
	r, err := configMap(ctx)
	if err != nil {
		return err
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := objectPrior{Inputs: *r, Outputs: created}
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
	if !reflect.DeepEqual(namespace, created.Namespace) {
		return fmt.Errorf("unexpected created.Namespace")
	}
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
	patch := []byte(`{"metadata":{"labels":{"external":"retained"}}}`)
	_, err = api.Patch(ctx, created.Name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: "other"})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	read, err = r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
	if err := editManifest(ctx, r, func(m map[string]any) {
		m["data"] = map[string]any{"value": "updated"}
	}); err != nil {
		return err
	}
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
	if !reflect.DeepEqual(map[string]any{"value": "updated"}, live.Object["data"]) {
		return fmt.Errorf("unexpected live.Object[\"data\"]")
	}
	if !reflect.DeepEqual("retained", live.GetLabels()["external"]) {
		return fmt.Errorf("unexpected live.GetLabels()[\"external\"]")
	}
	p = objectPrior{Inputs: *r, Outputs: updated}
	_, err = api.Patch(ctx, created.Name, types.MergePatchType,
		[]byte(`{"data":{"value":"drift"}}`), metav1.PatchOptions{FieldManager: "other"})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	read, err = r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if reflect.DeepEqual(updated.Values, read.Values) {
		return fmt.Errorf("unexpected read.Values")
	}
	_, err = r.Update(ctx, nil, p)
	if !(apierrors.IsConflict(err)) {
		return fmt.Errorf("expected apierrors.IsConflict(err) to be true")
	}
	r.ForceConflicts = new(true)
	updated, err = r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p = objectPrior{Inputs: *r, Outputs: updated}
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

func checkReplacementUIDProtection(ctx context.Context) (result error) {
	api, _, err := objectAPI(ctx)
	if err != nil {
		return err
	}
	r, err := configMap(ctx)
	if err != nil {
		return err
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := objectPrior{Inputs: *r, Outputs: created}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	replacement := (&unstructured.Unstructured{Object: r.Manifest.(map[string]any)}).DeepCopy()
	replacement, err = api.Create(ctx, replacement, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			uid := replacement.GetUID()
			err := api.Delete(ctx, created.Name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &uid},
			})
			if err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	_, err = r.Read(ctx, nil, p)
	if err == nil || !strings.Contains(err.Error(), "UID changed") {
		return fmt.Errorf("expected error containing %q, got %v", "UID changed", err)
	}
	r.ForceConflicts = new(true)
	if err := editManifest(ctx, r, func(m map[string]any) {
		m["data"] = map[string]any{"value": "must-not-be-applied"}
	}); err != nil {
		return err
	}
	_, err = r.Update(ctx, nil, p)
	if err == nil {
		return fmt.Errorf("expected an error")
	}
	if err := api.Delete(ctx, created.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: new(replacement.GetUID())},
	}); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = r.Update(ctx, nil, p)
	if err == nil {
		return fmt.Errorf("expected an error")
	}
	_, err = api.Get(ctx, created.Name, metav1.GetOptions{})
	if !(apierrors.IsNotFound(err)) {
		return fmt.Errorf("expected apierrors.IsNotFound(err) to be true")
	}
	replacement.SetUID("")
	replacement.SetResourceVersion("")
	newObject, err := api.Create(ctx, replacement, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	replacement = newObject
	err = r.Delete(ctx, nil, p)
	if !(apierrors.IsConflict(err)) {
		return fmt.Errorf("expected apierrors.IsConflict(err) to be true")
	}
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(replacement.GetUID(), live.GetUID()) {
		return fmt.Errorf("unexpected live.GetUID()")
	}
	if !reflect.DeepEqual("first", live.Object["data"].(map[string]any)["value"]) {
		return fmt.Errorf("unexpected live.Object[\"data\"].(map[string]any)[\"value\"]")
	}

	return nil
}

func checkDeletionWaitsForFinalizers(ctx context.Context) (result error) {
	api, _, err := objectAPI(ctx)
	if err != nil {
		return err
	}
	r, err := configMap(ctx)
	if err != nil {
		return err
	}
	if err := editManifest(ctx, r, func(m map[string]any) {
		m["metadata"].(map[string]any)["finalizers"] = []any{"unobin.cloudboss.io/test"}
	}); err != nil {
		return err
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := objectPrior{Inputs: *r, Outputs: created}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, err := api.Patch(ctx, created.Name, types.MergePatchType,
				[]byte(`{"metadata":{"finalizers":[]}}`), metav1.PatchOptions{})
			if !apierrors.IsNotFound(err) {
				if err != nil {
					return fmt.Errorf("API check: %w", err)
				}
			}
			cleanupPrior := p
			cleanupPrior.Inputs.DeleteTimeout = new("10s")
			if err := r.Delete(ctx, nil, cleanupPrior); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	p.Inputs.DeleteTimeout = new("5s")
	err = r.Delete(ctx, nil, p)
	if !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("expected %v, got %v", context.DeadlineExceeded, err)
	}
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if live.GetDeletionTimestamp() == nil {
		return fmt.Errorf("expected nonnil live.GetDeletionTimestamp()")
	}
	_, err = r.Read(ctx, nil, p)
	if err == nil || !strings.Contains(err.Error(), "being deleted") {
		return fmt.Errorf("expected error containing %q, got %v", "being deleted", err)
	}
	_, err = api.Patch(ctx, created.Name, types.MergePatchType,
		[]byte(`{"metadata":{"finalizers":[]}}`), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p.Inputs.DeleteTimeout = new("5s")
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}

	return nil
}
