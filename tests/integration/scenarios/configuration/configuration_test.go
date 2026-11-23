//go:build integration

package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestConfiguration(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"),
		"use tests/integration/run.sh to select the test environment")
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	version, err := api.Discovery().ServerVersion()
	require.NoError(t, err)
	require.NotEmpty(t, version.GitVersion)
	namespace, err := client.Namespace(nil)
	require.NoError(t, err)
	maps := api.CoreV1().ConfigMaps(namespace)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	created, err := maps.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "unobin-config-"},
		Data:       map[string]string{"value": "created"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := maps.Delete(ctx, created.Name, metav1.DeleteOptions{})
		if !apierrors.IsNotFound(err) {
			require.NoError(t, err)
		}
	})
	read, err := maps.Get(ctx, created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, created.UID, read.UID)
	require.Equal(t, map[string]string{"value": "created"}, read.Data)
	dynamic, err := client.NewDynamic(nil)
	require.NoError(t, err)
	resource, resolvedNamespace, err := dynamic.Resolve(
		schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "")
	require.NoError(t, err)
	require.Equal(t, namespace, resolvedNamespace)
	object, err := resource.Get(ctx, created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, created.UID, object.GetUID())
	read.Data["value"] = "updated"
	_, err = maps.Update(ctx, read, metav1.UpdateOptions{})
	require.NoError(t, err)
	read, err = maps.Get(ctx, created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"value": "updated"}, read.Data)
	require.NoError(t, maps.Delete(ctx, created.Name, metav1.DeleteOptions{}))
	_, err = maps.Get(ctx, created.Name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "expected deletion, got %v", err)
}
