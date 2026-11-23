//go:build integration

package integration_test

import (
	"context"
	"os"
	"strings"
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

func TestNamespaceLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.NamespaceResource{Object: svc.NamespaceObject{Metadata: svc.NamespaceMetadata{
		GenerateName: new("unobin-namespace-"),
		Labels:       &map[string]string{"app": "namespace-test", "value": "first"},
		Annotations:  &map[string]string{"example.com/value": "first"},
	}}, WaitForDefaultServiceAccount: new(true), DeleteTimeout: new("30s")}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	created, err := r.Create(ctx, nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.NamespaceResource, *svc.NamespaceResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, r.Delete(ctx, nil, p))
	})
	name := created.Object.Metadata.Name
	require.True(t, strings.HasPrefix(name, "unobin-namespace-"))
	require.NotEmpty(t, created.Object.Metadata.UID)
	sa, err := api.CoreV1().ServiceAccounts(name).Get(ctx, "default", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "default", sa.Name)
	observed, err := r.Read(ctx, nil, p)
	require.NoError(t, err)
	require.Equal(t, created, observed)
	p.Observed = observed
	r.Object.Metadata.Labels = &map[string]string{"app": "namespace-test", "value": "updated"}
	r.Object.Metadata.Annotations = &map[string]string{"example.com/value": "updated"}
	updated, err := r.Update(ctx, nil, p)
	require.NoError(t, err)
	require.Equal(t, created.Object.Metadata.UID, updated.Object.Metadata.UID)
	live, err := api.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "updated", live.Labels["value"])
	require.Equal(t, "updated", live.Annotations["example.com/value"])
	require.NoError(t, r.Delete(ctx, nil, p))
	_, err = api.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "%v", err)
}
