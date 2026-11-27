package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const namespace = "unobin-it-dynamic-wait"

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	api, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	settings := api.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).
		Namespace(namespace)
	obj, err := settings.Get(ctx, "settings", metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected settings absence after destruction: %w", err)
		}
		return nil
	}
	if phase != "applied" && phase != "updated" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	if err != nil {
		return err
	}
	if err := verifyFactoryOutputs(ctx, dir, obj); err != nil {
		return err
	}
	if phase == "applied" {
		return verifyCustomWaits(ctx, api)
	}
	return nil
}

func verifyFactoryOutputs(ctx context.Context, dir string, obj *unstructured.Unstructured) error {
	command := exec.CommandContext(ctx, filepath.Join(dir, "dynamic-wait"), "state", "show",
		"action.ready", "--format", "json", "-c", "config.ub")
	command.Dir = dir
	body, err := command.Output()
	if err != nil {
		return fmt.Errorf("read wait outputs: %w", err)
	}
	var result struct {
		Entry struct {
			Outputs map[string]any `json:"outputs"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	expected := map[string]any{
		"server": cfg.Host, "api-version": "v1", "kind": "ConfigMap", "name": "settings",
		"namespace": namespace, "predicate": "field", "uid": string(obj.GetUID()),
		"resource-version": obj.GetResourceVersion(),
	}
	if !reflect.DeepEqual(expected, result.Entry.Outputs) {
		return fmt.Errorf("unexpected wait outputs")
	}
	return nil
}

func verifyCustomWaits(ctx context.Context, api dynamic.Interface) (result error) {
	group := "wait-" + strings.ToLower(rand.Text()[:10]) + ".unobin.cloudboss.io"
	definitions := api.Resource(schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	})
	manifest, err := os.ReadFile("testdata/widget-crd.yaml")
	if err != nil {
		return err
	}
	var decoded map[string]any
	data := []byte(strings.ReplaceAll(string(manifest), "GROUP", group))
	if err := yaml.UnmarshalStrict(data, &decoded); err != nil {
		return err
	}

	resource := &svc.ObjectResource{Manifest: decoded}
	definition, err := resource.Create(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		result = errors.Join(result, definitions.Delete(cleanup, definition.Name, metav1.DeleteOptions{}))
	}()
	established := &svc.WaitAction{
		APIVersion: "apiextensions.k8s.io/v1", Kind: "CustomResourceDefinition",
		Metadata: svc.ObjectLookupMetadata{Name: definition.Name}, ExpectedUID: &definition.UID,
		Condition: &svc.WaitCondition{Type: "Established"}, Timeout: new("10s")}
	if _, err := established.Run(ctx, nil); err != nil {
		return err
	}
	widgets := api.Resource(schema.GroupVersionResource{
		Group: group, Version: "v1", Resource: "widgets",
	}).
		Namespace(namespace)
	obj, err := widgets.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "example", "namespace": namespace},
		"spec":     map[string]any{"ready": false},
	}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	uid := string(obj.GetUID())
	field := &svc.WaitAction{APIVersion: group + "/v1", Kind: "Widget", ExpectedUID: &uid,
		Metadata: svc.ObjectLookupMetadata{Name: "example", Namespace: new(namespace)},
		Timeout:  new("10s"),
		Field:    &svc.WaitField{Path: []string{"spec", "ready"}, BooleanEquals: new(true)}}
	err = waitWithChange(ctx, field, func(ctx context.Context) error {
		current, err := widgets.Get(ctx, "example", metav1.GetOptions{})
		if err != nil {
			return err
		}
		current.Object["spec"] = map[string]any{"ready": true}
		_, err = widgets.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return err
	}
	condition := *field
	condition.Field = nil
	condition.Condition = &svc.WaitCondition{Type: "Ready"}
	err = waitWithChange(ctx, &condition, func(ctx context.Context) error {
		current, err := widgets.Get(ctx, "example", metav1.GetOptions{})
		if err != nil {
			return err
		}
		current.Object["status"] = map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "observedGeneration": current.GetGeneration()},
		}}
		_, err = widgets.UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return err
	}
	deletion := *field
	deletion.Field = nil
	deletion.Deleted = new(true)
	return waitWithChange(ctx, &deletion, func(ctx context.Context) error {
		return widgets.Delete(ctx, "example", metav1.DeleteOptions{})
	})
}

func waitWithChange(
	ctx context.Context, action *svc.WaitAction, change func(context.Context) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	changed := make(chan error, 1)
	go func() {
		timer := time.NewTimer(400 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			changed <- ctx.Err()
		case <-timer.C:
			changed <- change(ctx)
		}
	}()
	out, err := action.Run(ctx, nil)
	cancel()
	err = errors.Join(err, <-changed)
	if err != nil {
		return err
	}
	if out.UID == nil || *out.UID != *action.ExpectedUID {
		return fmt.Errorf("wait lost object UID")
	}
	if action.Deleted != nil && out.ResourceVersion != nil {
		return fmt.Errorf("deletion returned a version")
	}
	return nil
}
