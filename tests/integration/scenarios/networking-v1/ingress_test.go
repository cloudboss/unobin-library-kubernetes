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

func TestIngressLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.IngressResource{Metadata: svc.IngressMetadata{GenerateName: new("unobin-api-ingress-")},
		Spec: svc.IngressSpec{Rule: new([]svc.IngressRule{{Host: new("example.com"),
			HTTP: &svc.IngressHTTP{Path: []svc.IngressPath{{Path: new("/"),
				Backend: &svc.IngressBackend{Service: &svc.IngressServiceBackend{Name: "app",
					Port: svc.IngressServicePort{Number: new(int64(80))}}},
			}}},
		}})},
	}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.IngressResource, *svc.IngressResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	live, err := api.NetworkingV1().Ingresses("default").Get(
		t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, networkingv1.PathTypeImplementationSpecific,
		*live.Spec.Rules[0].HTTP.Paths[0].PathType)
	r.Spec = svc.IngressSpec{DefaultBackend: &svc.IngressBackend{Service: &svc.IngressServiceBackend{
		Name: "updated", Port: svc.IngressServicePort{Name: new("http")},
	}}}
	r.WaitForLoadBalancer = new(true)
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Nil(t, updated.Spec.Rule)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
