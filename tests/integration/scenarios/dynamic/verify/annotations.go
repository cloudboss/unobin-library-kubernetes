package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"reflect"
	"strings"

	"github.com/cloudboss/unobin/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type annotationsPrior = runtime.Prior[
	svc.AnnotationsResource, *svc.AnnotationsResourceOutput, *config.Configuration,
]

func verifyAnnotationsOwnership(ctx context.Context) error {
	api, err := client.NewDynamic(nil)
	if err != nil {
		return err
	}
	for _, kind := range []schema.GroupVersionKind{
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "batch", Version: "v1", Kind: "CronJob"},
	} {
		workloads, _, err := api.Resolve(kind, "default")
		if err != nil {
			return err
		}
		if err := verifyWorkloadAnnotations(ctx, workloads, kind); err != nil {
			return fmt.Errorf("%s annotation ownership: %w", kind.Kind, err)
		}
	}
	return nil
}

func verifyWorkloadAnnotations(
	ctx context.Context, api dynamic.ResourceInterface, kind schema.GroupVersionKind,
) (result error) {
	name := "unobin-annotations-" + strings.ToLower(rand.Text()[:10])
	workload, template := annotationsWorkload(kind, name)
	fixture, err := api.Create(ctx, workload, metav1.CreateOptions{FieldManager: "outside"})
	if err != nil {
		return err
	}
	defer deleteMetadataFixture(api, fixture, &result)

	specPath := append(append([]string{}, template...), "spec")
	wantSpec, _, err := unstructured.NestedMap(fixture.Object, specPath...)
	if err != nil {
		return err
	}
	annotationPath := append(append([]string{}, template...), "metadata", "annotations")
	check := func(top, nested map[string]string) error {
		live, err := api.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annotations := live.GetAnnotations()
		if kind.Kind == "Deployment" {
			// The Deployment controller updates the revision independently.
			delete(annotations, "deployment.kubernetes.io/revision")
		}
		podAnnotations, _, err := unstructured.NestedStringMap(live.Object, annotationPath...)
		if err != nil {
			return err
		}
		spec, _, err := unstructured.NestedMap(live.Object, specPath...)
		if err != nil {
			return err
		}
		if fixture.GetUID() != live.GetUID() || !maps.Equal(top, annotations) ||
			!maps.Equal(nested, podAnnotations) || !reflect.DeepEqual(spec, wantSpec) {
			return fmt.Errorf("workload identity, annotations or Pod spec differ")
		}
		return nil
	}

	value := svc.AnnotationsResource{
		APIVersion: kind.GroupVersion().String(), Kind: kind.Kind,
		Metadata: svc.ObjectLookupMetadata{Name: name, Namespace: new("default")},
		Annotations: new(map[string]string{
			"note": "line one\nline two", "empty": "",
		}),
		TemplateAnnotations: new(map[string]string{"note": "template"}),
	}
	out, err := value.Create(ctx, nil)
	if err != nil {
		return err
	}
	if !maps.Equal(*value.Annotations, out.Annotations) ||
		!maps.Equal(*value.TemplateAnnotations, out.TemplateAnnotations) {
		return fmt.Errorf("create annotation projection differs")
	}
	if err := check(
		map[string]string{
			"external": "keep", "claim": "outside", "note": "line one\nline two", "empty": "",
		},
		map[string]string{"external": "keep-template", "note": "template"},
	); err != nil {
		return err
	}
	prior := annotationsPrior{Inputs: value, Outputs: out}

	forced := value
	forced.Annotations = new(map[string]string{"claim": "inside"})
	forced.TemplateAnnotations = new(map[string]string{})
	out, err = forced.Update(ctx, nil, prior)
	if out != nil || !isMetadataConflict(err) {
		return fmt.Errorf("expected annotation ownership conflict: %v", err)
	}
	forced.ForceConflicts = new(true)
	out, err = forced.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	if !maps.Equal(*forced.Annotations, out.Annotations) || len(out.TemplateAnnotations) != 0 {
		return fmt.Errorf("forced annotation update projection differs")
	}
	outsideTemplate := map[string]string{"external": "keep-template"}
	claimed := map[string]string{"external": "keep", "claim": "inside"}
	if err := check(claimed, outsideTemplate); err != nil {
		return err
	}
	prior = annotationsPrior{Inputs: forced, Outputs: out}

	second := value
	second.FieldManager = new("independent")
	second.Annotations = new(map[string]string{"second": "top"})
	second.TemplateAnnotations = new(map[string]string{"second": "template"})
	secondOut, err := second.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	if err := check(map[string]string{"external": "keep", "claim": "inside", "second": "top"},
		map[string]string{"external": "keep-template", "second": "template"}); err != nil {
		return err
	}
	secondPrior := annotationsPrior{Inputs: second, Outputs: secondOut}
	if err := second.Delete(ctx, nil, secondPrior); err != nil {
		return err
	}
	if err := check(claimed, outsideTemplate); err != nil {
		return err
	}

	templateOnly := value
	templateOnly.Annotations = nil
	out, err = templateOnly.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	if len(out.Annotations) != 0 ||
		!maps.Equal(*templateOnly.TemplateAnnotations, out.TemplateAnnotations) {
		return fmt.Errorf("template annotation update projection differs")
	}
	outside := map[string]string{"external": "keep"}
	wantTemplate := map[string]string{"external": "keep-template", "note": "template"}
	if err := check(outside, wantTemplate); err != nil {
		return err
	}
	prior = annotationsPrior{Inputs: templateOnly, Outputs: out}
	if err := templateOnly.Delete(ctx, nil, prior); err != nil {
		return err
	}
	return check(outside, outsideTemplate)
}

func annotationsWorkload(
	kind schema.GroupVersionKind, name string,
) (*unstructured.Unstructured, []string) {
	template := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"external": "keep-template"}},
		"spec": map[string]any{"containers": []any{
			map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10"},
		}},
	}
	spec := map[string]any{"template": template}
	path := []string{"spec", "template"}
	if kind.Kind == "CronJob" {
		template["spec"].(map[string]any)["restartPolicy"] = "Never"
		spec = map[string]any{
			"schedule": "0 0 * * *", "suspend": true,
			"jobTemplate": map[string]any{"spec": spec},
		}
		path = []string{"spec", "jobTemplate", "spec", "template"}
	} else {
		template["metadata"].(map[string]any)["labels"] = map[string]any{"app": name}
		spec["selector"] = map[string]any{"matchLabels": map[string]any{"app": name}}
		spec["replicas"] = int64(0)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kind.GroupVersion().String(), "kind": kind.Kind,
		"metadata": map[string]any{
			"name": name, "namespace": "default",
			"annotations": map[string]any{"external": "keep", "claim": "outside"},
		},
		"spec": spec,
	}}, path
}
