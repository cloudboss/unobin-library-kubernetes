//go:build integration

package configmapinfo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestConfigMapInfoReadOnly(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	require.NoError(t, err)
	obj, err := api.CoreV1().ConfigMaps("default").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "unobin-it-config-map-info-read-"},
		Data:       map[string]string{"empty": "", "unicode": "雪"},
		BinaryData: map[string][]byte{"bytes": {0, 255}}, Immutable: new(true),
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, api.CoreV1().ConfigMaps("default").Delete(
			context.Background(), obj.Name, metav1.DeleteOptions{}))
	})
	r := &svc.ConfigMapDataSource{Metadata: svc.ConfigMapLookupMetadata{Name: &obj.Name,
		Annotations: &map[string]string{"EXAMPLE.COM/Lookup": "not-a-selector"},
		Labels:      &map[string]string{"lookup": "not-a-selector"}}, Immutable: new(false)}
	out, err := r.Read(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, string(obj.UID), out.Metadata.UID)
	require.Equal(t, obj.Data, out.Data)
	require.Equal(t, map[string]string{"bytes": "AP8="}, out.BinaryData)
	require.True(t, out.Immutable)
	r.Metadata.Name = new("unobin-it-config-map-info-absent")
	missing, err := r.Read(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "unobin-it-config-map-info-absent", missing.Metadata.Name)
	require.Equal(t, "default", missing.Metadata.Namespace)
	require.Equal(t, map[string]string{}, missing.Data)
	require.Equal(t, map[string]string{}, missing.BinaryData)
	require.False(t, missing.Immutable)
	after, err := api.CoreV1().ConfigMaps("default").Get(ctx, obj.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, obj, after)
}
