package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func checkCustomObject(ctx context.Context) (result error) {
	_, namespace, err := objectAPI(ctx)
	if err != nil {
		return err
	}
	group := "object-" + strings.ToLower(rand.Text()[:10]) + ".unobin.cloudboss.io"
	manifest, err := os.ReadFile("testdata/widget-crd.yaml")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	var decoded map[string]any
	data := []byte(strings.ReplaceAll(string(manifest), "GROUP", group))
	if err := yaml.UnmarshalStrict(data, &decoded); err != nil {
		return err
	}

	crd := &svc.ObjectResource{
		Manifest: decoded, DeleteTimeout: new("10s"),
	}
	definition, err := crd.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if len(definition.Namespace) != 0 {
		return fmt.Errorf("expected empty definition.Namespace")
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := crd.Delete(ctx, nil,
				objectPrior{Inputs: *crd, Outputs: definition}); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	dynamic, err := client.NewDynamic(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	definitions, _, err := dynamic.Resolve(schema.GroupVersionKind{
		Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition",
	}, "")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 10*time.Second, true,
		func(ctx context.Context) (bool, error) {
			obj, err := definitions.Get(ctx, definition.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			conditions, _, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
			if err != nil {
				return false, err
			}
			for _, condition := range conditions {
				entry := condition.(map[string]any)
				if entry["type"] == "Established" && entry["status"] == "True" {
					return true, nil
				}
			}
			return false, nil
		})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	instance := map[string]any{
		"apiVersion": group + "/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "example"},
		"spec": map[string]any{"value": "first", "members": []any{
			map[string]any{"name": "app", "value": "managed"},
		}},
	}
	r := &svc.ObjectResource{Manifest: instance, DeleteTimeout: new("5s")}
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
	dynamic, err = client.NewDynamic(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	api, _, err := dynamic.Resolve(schema.GroupVersionKind{
		Group: group, Version: "v1", Kind: "Widget",
	}, "")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("server-default", live.Object["spec"].(map[string]any)["defaulted"]) {
		return fmt.Errorf("unexpected live.Object[\"spec\"].(map[string]any)[\"defaulted\"]")
	}
	other := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1", "kind": "Widget",
		"metadata": map[string]any{"name": created.Name, "namespace": namespace},
		"spec": map[string]any{"members": []any{
			map[string]any{"name": "sidecar", "value": "external"},
		}},
	}}
	live, err = api.Apply(ctx, created.Name, other,
		metav1.ApplyOptions{FieldManager: "other"})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	live.Object["status"] = map[string]any{"phase": "Ready"}
	_, err = api.UpdateStatus(ctx, live, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
	if err := editManifest(ctx, r, func(m map[string]any) {
		m["spec"].(map[string]any)["value"] = "updated"
	}); err != nil {
		return err
	}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p = objectPrior{Inputs: *r, Outputs: updated}
	live, err = api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, string(live.GetUID())) {
		return fmt.Errorf("unexpected string(live.GetUID())")
	}
	if !reflect.DeepEqual("updated", live.Object["spec"].(map[string]any)["value"]) {
		return fmt.Errorf("unexpected live.Object[\"spec\"].(map[string]any)[\"value\"]")
	}
	members := live.Object["spec"].(map[string]any)["members"].([]any)
	sort.Slice(members, func(i, j int) bool {
		return members[i].(map[string]any)["name"].(string) <
			members[j].(map[string]any)["name"].(string)
	})
	if !reflect.DeepEqual([]any{
		map[string]any{"name": "app", "value": "managed"},
		map[string]any{"name": "sidecar", "value": "external"},
	}, members) {
		return fmt.Errorf("custom object members differ")
	}
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

	return nil
}
