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

func checkServiceInfoReadOnly(ctx context.Context) (result error) {
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
	before, err := api.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r := &svc.ServiceDataSource{Metadata: svc.ServiceLookupMetadata{Name: new("kubernetes"),
		Annotations: &map[string]string{"EXAMPLE.COM/Lookup": "not-a-selector"},
		Labels:      &map[string]string{"unobin-lookup": "not-a-selector"}}}
	out, err := r.Read(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(string(before.UID), out.Metadata.UID) {
		return fmt.Errorf("unexpected out.Metadata.UID")
	}
	if !reflect.DeepEqual(before.Spec.ClusterIP, out.Spec.ClusterIP) {
		return fmt.Errorf("unexpected out.Spec.ClusterIP")
	}
	if !reflect.DeepEqual(before.Spec.ClusterIPs, out.Spec.ClusterIps) {
		return fmt.Errorf("unexpected out.Spec.ClusterIps")
	}
	if out.Status == nil {
		return fmt.Errorf("expected nonnil out.Status")
	}
	r.Metadata.Name = new("unobin-it-service-info-absent")
	missing, err := r.Read(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("unobin-it-service-info-absent", missing.Metadata.Name) {
		return fmt.Errorf("unexpected missing.Metadata.Name")
	}
	if !reflect.DeepEqual("default", missing.Metadata.Namespace) {
		return fmt.Errorf("unexpected missing.Metadata.Namespace")
	}
	if missing.Spec != nil {
		return fmt.Errorf("expected nil missing.Spec")
	}
	if missing.Status != nil {
		return fmt.Errorf("expected nil missing.Status")
	}
	after, err := api.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
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
