package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkNamespaceInfoReadOnly(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	before, err := api.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r := &svc.NamespaceDataSource{Metadata: svc.NamespaceLookupMetadata{Name: "default",
		Annotations: &map[string]*string{"EXAMPLE.COM/Lookup": new("not-a-selector")},
		Labels:      &map[string]*string{"unobin-lookup": new("not-a-selector")}}}
	out, err := r.Read(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(string(before.UID), out.Metadata.UID) {
		return fmt.Errorf("unexpected out.Metadata.UID")
	}
	finalizers := []string{}
	for _, value := range before.Spec.Finalizers {
		finalizers = append(finalizers, string(value))
	}
	if !reflect.DeepEqual(finalizers, out.Spec.Finalizers) {
		return fmt.Errorf("unexpected out.Spec.Finalizers")
	}
	annotations, labels := before.Annotations, before.Labels
	if annotations == nil {
		annotations = map[string]string{}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	if !reflect.DeepEqual(svc.NamespaceLookupMetadataOutput{
		Name: before.Name, UID: string(before.UID), Generation: before.Generation,
		ResourceVersion: before.ResourceVersion, Annotations: annotations, Labels: labels,
	}, out.Metadata) {
		return fmt.Errorf("unexpected out.Metadata")
	}
	r.Metadata.Name = "unobin-it-namespace-info-absent"
	missing, err := r.Read(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("unobin-it-namespace-info-absent", missing.Metadata.Name) {
		return fmt.Errorf("unexpected missing.Metadata.Name")
	}
	if missing.Spec != nil {
		return fmt.Errorf("expected nil missing.Spec")
	}
	after, err := api.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(before.UID, after.UID) {
		return fmt.Errorf("unexpected after.UID")
	}
	if !reflect.DeepEqual(before.Spec, after.Spec) {
		return fmt.Errorf("unexpected after.Spec")
	}

	return nil
}
