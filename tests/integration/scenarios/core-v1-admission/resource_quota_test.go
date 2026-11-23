//go:build integration

package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	quota "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestResourceQuotaLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	r := &quota.ResourceQuotaResource{
		Metadata: quota.ServiceMetadata{GenerateName: new("unobin-quota-go-")},
		Spec:     &quota.ResourceQuotaSpec{Hard: &map[string]string{"pods": "4"}},
	}
	created, err := r.Create(ctx, nil)
	require.NoError(t, err)
	prior := runtime.Prior[
		quota.ResourceQuotaResource, *quota.ResourceQuotaResourceOutput, *config.Configuration,
	]{Inputs: *r, Outputs: created}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, r.Delete(ctx, nil, prior))
	})
	require.Contains(t, created.Name, "unobin-quota-go-")
	require.Equal(t, "unobin-quota-go-", created.Metadata.GenerateName)
	require.Equal(t, map[string]string{"pods": "4"}, created.Spec.Hard)
	require.NotEmpty(t, created.UID)
	require.Equal(t, "default", created.Namespace)
	observed, err := r.Read(ctx, nil, prior)
	require.NoError(t, err)
	require.Equal(t, created, observed)
	r.Spec = &quota.ResourceQuotaSpec{Hard: &map[string]string{"pods": "6"}}
	prior.Observed = observed
	updated, err := r.Update(ctx, nil, prior)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, map[string]string{"pods": "6"}, updated.Spec.Hard)
	quotas := api.CoreV1().ResourceQuotas("default")
	live, err := quotas.Get(ctx, created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, corev1.ResourceList{corev1.ResourcePods: resource.MustParse("6")},
		live.Status.Hard)
	require.NoError(t, r.Delete(ctx, nil, prior))
	_, err = quotas.Get(ctx, created.Name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "%v", err)
}
