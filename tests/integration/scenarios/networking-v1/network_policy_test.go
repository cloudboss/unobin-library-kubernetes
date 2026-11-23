//go:build integration

package integration_test

import (
	"context"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/networking/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestNetworkPolicyAPILifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.NetworkPolicyResource{
		Metadata: svc.NetworkPolicyMetadata{GenerateName: new("unobin-api-policy-")},
		Spec: svc.NetworkPolicySpec{PodSelector: svc.NetworkPolicySelector{},
			PolicyTypes: []string{"Ingress"}, Ingress: new([]svc.NetworkPolicyIngress{{}})},
	}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.NetworkPolicyResource, *svc.NetworkPolicyResourceOutput,
		*config.Configuration]{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	live, err := api.NetworkingV1().NetworkPolicies("default").Get(
		t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, []networkingv1.NetworkPolicyIngressRule{{}}, live.Spec.Ingress)
	r.Spec.Ingress = new([]svc.NetworkPolicyIngress{})
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Nil(t, updated.Spec.Ingress)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
