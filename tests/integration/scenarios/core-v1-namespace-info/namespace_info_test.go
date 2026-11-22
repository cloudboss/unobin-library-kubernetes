//go:build integration

package namespaceinfo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestNamespaceInfoReadOnly(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	require.NoError(t, err)
	before, err := api.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
	require.NoError(t, err)
	r := &svc.NamespaceDataSource{Metadata: svc.NamespaceLookupMetadata{Name: "default",
		Annotations: &map[string]*string{"EXAMPLE.COM/Lookup": new("not-a-selector")},
		Labels:      &map[string]*string{"unobin-lookup": new("not-a-selector")}}}
	out, err := r.Read(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, string(before.UID), out.Metadata.UID)
	finalizers := []string{}
	for _, value := range before.Spec.Finalizers {
		finalizers = append(finalizers, string(value))
	}
	require.Equal(t, finalizers, out.Spec.Finalizers)
	annotations, labels := before.Annotations, before.Labels
	if annotations == nil {
		annotations = map[string]string{}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	require.Equal(t, svc.NamespaceLookupMetadataOutput{
		Name: before.Name, UID: string(before.UID), Generation: before.Generation,
		ResourceVersion: before.ResourceVersion, Annotations: annotations, Labels: labels,
	}, out.Metadata)
	r.Metadata.Name = "unobin-it-namespace-info-absent"
	missing, err := r.Read(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "unobin-it-namespace-info-absent", missing.Metadata.Name)
	require.Nil(t, missing.Spec)
	after, err := api.CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, before.UID, after.UID)
	require.Equal(t, before.Spec, after.Spec)
}
