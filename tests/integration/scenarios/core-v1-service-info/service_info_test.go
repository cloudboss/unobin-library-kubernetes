//go:build integration

package serviceinfo_test

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

func TestServiceInfoReadOnly(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	require.NoError(t, err)
	before, err := api.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	require.NoError(t, err)
	r := &svc.ServiceDataSource{Metadata: svc.ServiceLookupMetadata{Name: new("kubernetes"),
		Annotations: &map[string]string{"EXAMPLE.COM/Lookup": "not-a-selector"},
		Labels:      &map[string]string{"unobin-lookup": "not-a-selector"}}}
	out, err := r.Read(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, string(before.UID), out.Metadata.UID)
	require.Equal(t, before.Spec.ClusterIP, out.Spec.ClusterIP)
	require.Equal(t, before.Spec.ClusterIPs, out.Spec.ClusterIps)
	require.NotNil(t, out.Status)
	r.Metadata.Name = new("unobin-it-service-info-absent")
	missing, err := r.Read(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "unobin-it-service-info-absent", missing.Metadata.Name)
	require.Equal(t, "default", missing.Metadata.Namespace)
	require.Nil(t, missing.Spec)
	require.Nil(t, missing.Status)
	after, err := api.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, before.UID, after.UID)
	require.Equal(t, before.Spec, after.Spec)
}
