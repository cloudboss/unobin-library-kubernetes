package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func checkCustomObjectInfo(ctx context.Context) (result error) {
	if os.Getenv("UNOBIN_KUBERNETES_INTEGRATION") != "1" || os.Getenv("KUBECONFIG") == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	group := "object-info-" + strings.ToLower(rand.Text()[:10]) + ".unobin.cloudboss.io"
	source, err := os.ReadFile("testdata/lookup-widget-crd.yaml")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	cleanupCRD, err := createLookupObject(ctx, strings.ReplaceAll(string(source), "GROUP", group))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, cleanupCRD()) }()

	version := group + "/v2"
	gvk := schema.GroupVersionKind{Group: group, Version: "v2", Kind: "Widget"}
	var widgets dynamic.ResourceInterface
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			api, err := client.NewDynamicContext(ctx, nil)
			if err != nil {
				return false, err
			}
			widgets, _, err = api.Resolve(gvk, "default")
			if meta.IsNoMatchError(err) {
				return false, nil
			}
			return err == nil, err
		})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	spec := map[string]any{
		"nested": map[string]any{
			"targets": []any{int64(8080), "https"},
			"serial":  int64(9007199254740993),
		},
	}
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": version, "kind": "Widget", "spec": spec,
		"metadata": map[string]any{"name": "example", "namespace": "default"},
	})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	cleanupObject, err := createLookupObject(ctx, string(manifest))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, cleanupObject()) }()

	live, err := widgets.Get(ctx, "example", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	lookup := svc.ObjectDataSource{
		APIVersion: version, Kind: "Widget", Metadata: svc.ObjectLookupMetadata{Name: "example"},
	}
	output, err := lookup.Read(ctx, &config.Configuration{Namespace: new("other")})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(&svc.ObjectDataSourceOutput{Object: &live.Object}, output) {
		return fmt.Errorf("unexpected output")
	}
	if !reflect.DeepEqual(spec, (*output.Object)["spec"]) {
		return fmt.Errorf("unexpected (*output.Object)[\"spec\"]")
	}

	return nil
}

func createLookupObject(ctx context.Context, manifest string) (func() error, error) {
	resource := svc.ObjectResource{Manifest: manifest, DeleteTimeout: new("20s")}
	out, err := resource.Create(ctx, nil)
	if err != nil {
		return nil, err
	}
	return func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return resource.Delete(cleanup, nil,
			runtime.Prior[svc.ObjectResource, *svc.ObjectResourceOutput, *config.Configuration]{
				Inputs: resource, Outputs: out,
			})
	}, nil
}
