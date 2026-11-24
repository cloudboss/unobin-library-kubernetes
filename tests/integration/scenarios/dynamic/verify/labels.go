package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type labelsPrior = runtime.Prior[
	svc.LabelsResource, *svc.LabelsResourceOutput, *config.Configuration,
]

func verifyLabelsOwnership(ctx context.Context) (result error) {
	api, err := client.NewDynamic(nil)
	if err != nil {
		return err
	}
	configMaps, _, err := api.Resolve(
		schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "default",
	)
	if err != nil {
		return err
	}
	name := "unobin-labels-" + strings.ToLower(rand.Text()[:10])
	fixture, err := configMaps.Create(ctx, metadataConfigMap(name),
		metav1.CreateOptions{FieldManager: "external"})
	if err != nil {
		return err
	}
	defer deleteMetadataFixture(configMaps, fixture, &result)

	value := svc.LabelsResource{
		APIVersion: "v1", Kind: "ConfigMap",
		Metadata: svc.ObjectLookupMetadata{Name: name, Namespace: new("default")},
		Labels:   map[string]string{"app": "first", "empty": ""},
	}
	out, err := value.Create(ctx, nil)
	if err != nil {
		return err
	}
	if string(fixture.GetUID()) != out.UID || !maps.Equal(value.Labels, out.Labels) {
		return fmt.Errorf("labels create projection differs")
	}
	before := map[string]string{
		"external": "keep", "claimed": "outside", "app": "first", "empty": "",
	}
	if _, err := checkLabelsFixture(ctx, configMaps, fixture, before); err != nil {
		return err
	}
	prior := labelsPrior{Inputs: value, Outputs: out}
	next := value
	next.Labels = map[string]string{"app": "second", "added": "new", "claimed": "inside"}
	out, err = next.Update(ctx, nil, prior)
	if out != nil || !isMetadataConflict(err) {
		return fmt.Errorf("expected label ownership conflict: %v", err)
	}
	if _, err := checkLabelsFixture(ctx, configMaps, fixture, before); err != nil {
		return err
	}
	next.ForceConflicts = new(true)
	out, err = next.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	if !maps.Equal(next.Labels, out.Labels) {
		return fmt.Errorf("forced label apply projection differs")
	}
	live, err := checkLabelsFixture(ctx, configMaps, fixture, map[string]string{
		"external": "keep", "claimed": "inside", "app": "second", "added": "new",
	})
	if err != nil {
		return err
	}
	if err := checkLabelsOwners(live); err != nil {
		return err
	}

	second := value
	second.FieldManager = new("independent")
	second.Labels = map[string]string{"second": "retained"}
	secondOut, err := second.Create(ctx, nil)
	if err != nil {
		return err
	}
	if !maps.Equal(second.Labels, secondOut.Labels) {
		return fmt.Errorf("independent label apply projection differs")
	}
	empty := next
	empty.Labels = map[string]string{}
	out, err = empty.Update(ctx, nil, labelsPrior{Inputs: next, Outputs: out})
	if err != nil {
		return err
	}
	if len(out.Labels) != 0 {
		return fmt.Errorf("empty label apply retains projected fields")
	}
	remaining := map[string]string{"external": "keep", "second": "retained"}
	if _, err := checkLabelsFixture(ctx, configMaps, fixture, remaining); err != nil {
		return err
	}
	reapplied, err := value.Update(ctx, nil, labelsPrior{Inputs: empty, Outputs: out})
	if err != nil {
		return err
	}
	if err := value.Delete(ctx, nil, labelsPrior{Inputs: value, Outputs: reapplied}); err != nil {
		return err
	}
	if _, err := checkLabelsFixture(ctx, configMaps, fixture, remaining); err != nil {
		return err
	}
	if err := second.Delete(ctx, nil, labelsPrior{Inputs: second, Outputs: secondOut}); err != nil {
		return err
	}
	_, err = checkLabelsFixture(ctx, configMaps, fixture, map[string]string{"external": "keep"})
	return err
}

func metadataConfigMap(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{
			"name": name, "namespace": "default",
			"labels": map[string]any{"external": "keep", "claimed": "outside"},
		},
		"data": map[string]any{"value": "outside"},
	}}
}

func deleteMetadataFixture(
	api dynamic.ResourceInterface, fixture *unstructured.Unstructured, result *error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), metadataCleanupTimeout)
	defer cancel()
	uid := fixture.GetUID()
	err := api.Delete(ctx, fixture.GetName(), metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		*result = errors.Join(*result, fmt.Errorf("delete metadata fixture: %w", err))
	}
}

func isMetadataConflict(err error) bool {
	_, cause := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict)
	return apierrors.IsConflict(err) && cause && strings.Contains(err.Error(), "force-conflicts")
}

func checkLabelsFixture(
	ctx context.Context, api dynamic.ResourceInterface, fixture *unstructured.Unstructured,
	want map[string]string,
) (*unstructured.Unstructured, error) {
	live, err := api.Get(ctx, fixture.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if fixture.GetUID() != live.GetUID() || !maps.Equal(live.GetLabels(), want) ||
		!reflect.DeepEqual(live.Object["data"], map[string]any{"value": "outside"}) {
		return nil, fmt.Errorf("ConfigMap %s: identity, labels or data differ", fixture.GetName())
	}
	return live, nil
}

func checkLabelsOwners(live *unstructured.Unstructured) error {
	owners := map[string]string{}
	for _, entry := range live.GetManagedFields() {
		if entry.FieldsV1 == nil {
			continue
		}
		var fields struct {
			Metadata struct {
				Labels map[string]json.RawMessage `json:"f:labels"`
				UID    json.RawMessage            `json:"f:uid"`
			} `json:"f:metadata"`
			Data json.RawMessage `json:"f:data"`
		}
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			return err
		}
		for _, key := range []string{"claimed", "external"} {
			if fields.Metadata.Labels["f:"+key] != nil {
				owners[key] = entry.Manager
			}
		}
		if fields.Metadata.UID != nil {
			owners["uid"] = entry.Manager
		}
		if fields.Data != nil {
			owners["data"] = entry.Manager
		}
	}
	if !maps.Equal(owners, map[string]string{
		"claimed": "Terraform", "external": "external", "data": "external",
	}) {
		return fmt.Errorf("unexpected managed label and data fields: %v", owners)
	}
	return nil
}
