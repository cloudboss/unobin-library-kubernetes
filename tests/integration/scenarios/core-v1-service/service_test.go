//go:build integration

package service_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestServiceLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.ServiceResource{Metadata: svc.ServiceMetadata{GenerateName: new("unobin-service-")},
		Spec: svc.ServiceSpec{Selector: &map[string]string{"app": "first"},
			Port: &[]svc.ServicePort{{Port: 80}}}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	created, err := r.Create(ctx, nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.ServiceResource, *svc.ServiceResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, r.Delete(ctx, nil, p))
	})
	require.NotEmpty(t, created.UID)
	require.NotEmpty(t, created.Spec.ClusterIP)
	require.Equal(t, []string{created.Spec.ClusterIP}, created.Spec.ClusterIps)
	require.Equal(t, "80", created.Spec.Port[0].TargetPort)
	observed, err := r.Read(ctx, nil, p)
	require.NoError(t, err)
	require.Equal(t, created, observed)
	p.Observed = observed
	r.Spec.Selector = &map[string]string{"app": "updated"}
	r.Spec.Port = &[]svc.ServicePort{{Port: 80, Name: new("http")}}
	updated, err := r.Update(ctx, nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, created.Spec.ClusterIP, updated.Spec.ClusterIP)
	require.Equal(t, created.Spec.Port[0].TargetPort, updated.Spec.Port[0].TargetPort)
	live, err := api.CoreV1().Services("default").Get(ctx, created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"app": "updated"}, live.Spec.Selector)
	require.NoError(t, r.Delete(ctx, nil, p))
	_, err = api.CoreV1().Services("default").Get(ctx, created.Name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "%v", err)
}
