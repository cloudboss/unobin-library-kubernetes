//go:build integration

package hpa_test

import (
	"context"
	"os"
	"testing"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/autoscaling/v2"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHPAAcceptanceLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.HorizontalPodAutoscalerResource{Object: svc.HPAObject{Metadata: svc.HPAMetadata{
		GenerateName: new("unobin-hpa-api-")}, Spec: svc.HPASpec{MaxReplicas: 4,
		ScaleTargetRef: svc.HPATargetRef{APIVersion: new("apps/v1"), Kind: "Deployment",
			Name: "absent-target"}}}}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.HorizontalPodAutoscalerResource,
		*svc.HorizontalPodAutoscalerResourceOutput, *config.Configuration]{Inputs: *r, Outputs: created}
	t.Cleanup(func() {
		live, err := api.AutoscalingV2().HorizontalPodAutoscalers("default").Get(
			context.Background(), created.Object.Metadata.Name, metav1.GetOptions{},
		)
		if err == nil {
			live.Finalizers = nil
			_, err = api.AutoscalingV2().HorizontalPodAutoscalers("default").Update(
				context.Background(), live, metav1.UpdateOptions{},
			)
			require.NoError(t, err)
		}
		require.NoError(t, r.Delete(context.Background(), nil, p))
	})
	require.Equal(t, "default", created.Object.Metadata.Namespace)
	require.NotEmpty(t, *created.Object.Spec.Metric)
	p.Observed, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	r.Object.Spec.MaxReplicas = 6
	r.Object.Spec.MinReplicas = new(2)
	r.Object.Metadata.Labels = &map[string]string{"app": "updated"}
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.Object.Metadata.UID, updated.Object.Metadata.UID)
	require.Equal(t, 6, updated.Object.Spec.MaxReplicas)
	require.Equal(t, new(2), updated.Object.Spec.MinReplicas)
	live, err := api.AutoscalingV2().HorizontalPodAutoscalers("default").Get(t.Context(),
		created.Object.Metadata.Name, metav1.GetOptions{})
	require.NoError(t, err)
	live.Finalizers = []string{"example.com/hold"}
	_, err = api.AutoscalingV2().HorizontalPodAutoscalers("default").Update(t.Context(), live,
		metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	live, err = api.AutoscalingV2().HorizontalPodAutoscalers("default").Get(t.Context(),
		created.Object.Metadata.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, live.DeletionTimestamp)
}
