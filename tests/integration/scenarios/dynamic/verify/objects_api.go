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

	"github.com/cloudboss/unobin/pkg/sdk/state"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkCompiledObjectsOnKubernetes(ctx context.Context) (result error) {
	namespace := "dynamic-objects-" + strings.ToLower(rand.Text()[:10])
	group := namespace + ".unobin.cloudboss.io"
	core, err := client.New(nil)
	if err != nil {
		return err
	}
	_, err = core.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		result = errors.Join(result, core.CoreV1().Namespaces().Delete(cleanup, namespace,
			metav1.DeleteOptions{}))
	}()
	for name, tier := range map[string]string{
		"edge-first": "edge", "edge-second": "edge", "unrelated": "storage",
	} {
		_, err := core.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"tier": tier}},
			Data:       map[string]string{"value": name},
		}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	}
	source, err := os.ReadFile("testdata/mouse-crd.yaml")
	if err != nil {
		return err
	}
	var decoded map[string]any
	data := []byte(strings.ReplaceAll(string(source), "GROUP", group))
	if err := yaml.UnmarshalStrict(data, &decoded); err != nil {
		return err
	}
	cleanupCRD, err := createLookupObject(ctx, decoded)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, cleanupCRD()) }()
	var mice dynamic.ResourceInterface
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			api, err := client.NewDynamicContext(ctx, nil)
			if err != nil {
				return false, err
			}
			mice, _, err = api.Resolve(schema.GroupVersionKind{
				Group: group, Version: "v2", Kind: "Mouse",
			}, namespace)
			if meta.IsNoMatchError(err) {
				return false, nil
			}
			return err == nil, err
		})
	if err != nil {
		return err
	}
	spec := map[string]any{
		"targets": []any{int64(8080), "https", false, nil}, "serial": int64(9007199254740993),
	}
	mouse, err := mice.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v2", "kind": "Mouse",
		"metadata": map[string]any{"name": "example", "namespace": namespace}, "spec": spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	api, err := client.NewDynamicContext(ctx, nil)
	if err != nil {
		return err
	}
	configMaps, _, err := api.Resolve(
		schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, namespace)
	if err != nil {
		return err
	}
	first, err := configMaps.Get(ctx, "edge-first", metav1.GetOptions{})
	if err != nil {
		return err
	}
	second, err := configMaps.Get(ctx, "edge-second", metav1.GetOptions{})
	if err != nil {
		return err
	}
	factory, err := compileFixture(ctx, "testdata/ub/objects", "dynamic-objects")
	if err != nil {
		return err
	}
	defer factory.cleanup(&result)
	if err := factory.config("testdata/ub/objects/config.ub", "config.ub",
		strings.NewReplacer("LOOKUP_NAMESPACE", namespace, "LOOKUP_GROUP", group)); err != nil {
		return err
	}
	if _, err := factory.run("plan", "-c", "config.ub", "-o", "objects.ubp"); err != nil {
		return err
	}
	factory.applied = true
	if _, err := factory.run("apply", "objects.ubp"); err != nil {
		return err
	}
	body, err := factory.run("state", "pull", "-c", "config.ub")
	if err != nil {
		return err
	}
	snapshot, err := state.DecodeSnapshot(body)
	if err != nil {
		return err
	}
	for name, items := range map[string][]any{
		"selected": {second.Object}, "limited": {first.Object}, "empty": {}, "custom": {mouse.Object},
	} {
		entry := snapshot.Find("data-source." + name)
		if entry == nil || !reflect.DeepEqual(map[string]any{"objects": items}, entry.Outputs) {
			return fmt.Errorf("compiled objects lookup %s differs", name)
		}
	}
	if err := factory.commands(
		[]string{"plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp"},
		[]string{"apply", "destroy.ubp"},
	); err != nil {
		return err
	}
	factory.applied = false
	body, err = factory.run("state", "pull", "-c", "config.ub")
	if err != nil {
		return err
	}
	snapshot, err = state.DecodeSnapshot(body)
	if err != nil {
		return err
	}
	for _, name := range []string{"selected", "limited", "empty", "custom"} {
		if snapshot.Find("data-source."+name) != nil {
			return fmt.Errorf("destroy retained %s", name)
		}
	}
	return nil
}
