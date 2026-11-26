package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	crd "github.com/cloudboss/unobin-library-kubernetes/internal/api/apiextensions/v1"
)

func verifyNativeLifecycle(ctx context.Context, cfg *rest.Config) (result error) {
	r := &crd.CustomResourceDefinitionResource{
		Metadata: crd.CRDMetadata{Name: "samples.unobin-native.example.test"},
		Spec: crd.CRDSpec{
			Group: "unobin-native.example.test", Scope: "Namespaced",
			Names: crd.CRDNames{Plural: "samples", Kind: "Sample"},
			Versions: []crd.CRDVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &map[string]any{
					"type": "object", "properties": map[string]any{
						"spec": map[string]any{
							"type": "object", "x-kubernetes-preserve-unknown-fields": true,
						},
					},
				},
			}},
		},
	}
	out, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("native CRD create: %w", err)
	}
	p := runtime.Prior[
		crd.CustomResourceDefinitionResource, *crd.CustomResourceDefinitionResourceOutput,
		*config.Configuration,
	]{Inputs: *r, Outputs: out}
	deleted := false
	defer func() {
		if deleted {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, r.Delete(cleanup, nil, p))
	}()
	_, err = r.Create(ctx, nil)
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("CRD create must reject adoption, got %v", err)
	}
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return err
	}
	if read.UID != out.UID || read.Spec.Names.Kind != "Sample" {
		return fmt.Errorf("CRD read lost stable identity or actual spec")
	}
	objects, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	samples := objects.Resource(schema.GroupVersionResource{
		Group: r.Spec.Group, Version: "v1", Resource: "samples",
	}).Namespace(namespace)
	_, err = samples.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": r.Spec.Group + "/v1", "kind": "Sample",
		"metadata": map[string]any{"name": "delete-with-crd", "namespace": namespace},
		"spec":     map[string]any{"message": "preserved"},
	}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create Sample after CRD discovery: %w", err)
	}
	r.Spec.Versions[0].Schema = &map[string]any{"type": "unsupported-openapi-type"}
	_, err = r.Update(ctx, nil, p)
	if !apierrors.IsInvalid(err) {
		return fmt.Errorf("invalid structural schema must return API rejection, got %v", err)
	}
	r.Spec.Versions = append([]crd.CRDVersion(nil), p.Inputs.Spec.Versions...)
	r.Spec.Versions[0].Schema = &map[string]any{
		"type": "object", "x-kubernetes-preserve-unknown-fields": true,
	}
	r.Spec.Versions[0].Storage = false
	r.Spec.Versions = append(r.Spec.Versions, crd.CRDVersion{
		Name: "v2", Served: true, Storage: true, Schema: r.Spec.Versions[0].Schema,
	})
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("add CRD storage version without migrating old data: %w", err)
	}
	if updated.UID != out.UID {
		return fmt.Errorf("mutable CRD version update replaced identity")
	}
	p.Inputs, p.Outputs = *r, updated
	r.Spec.Versions = []crd.CRDVersion{r.Spec.Versions[1]}
	_, err = r.Update(ctx, nil, p)
	if !apierrors.IsInvalid(err) {
		return fmt.Errorf("stored-version removal must remain an API error, got %v", err)
	}
	// Teardown uses saved identity even when desired schema is invalid.
	p.Inputs.Spec.Versions[0].Schema = ptr.To(map[string]any{"invalid-schema": true})
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("delete CRD and associated instances: %w", err)
	}
	deleted = true
	_, err = samples.Get(ctx, "delete-with-crd", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting CRD must remove its Sample instances, got %v", err)
	}
	return nil
}
