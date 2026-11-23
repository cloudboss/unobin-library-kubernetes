//go:build integration

package integration_test

import (
	"context"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestSecretLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	namespace, err := client.Namespace(nil)
	require.NoError(t, err)
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.SecretResource{Object: svc.SecretObject{
		Metadata:   svc.SecretMetadata{GenerateName: new("unobin-secret-api-"), Namespace: &namespace},
		Data:       &map[string]string{"value": "first", "overlap": "text"},
		BinaryData: &map[string]string{"bytes": "AP8=", "overlap": "AAE=", "empty": ""},
	}}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.SecretResource, *svc.SecretResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	require.Equal(t, "dGV4dA==", created.Object.BinaryData["overlap"])
	require.Equal(t, "AP8=", created.Object.BinaryData["bytes"])
	_, err = api.CoreV1().Secrets(namespace).Patch(t.Context(), created.Object.Metadata.Name,
		types.MergePatchType, []byte(`{"data":{"external":"ZXh0cmE="}}`), metav1.PatchOptions{})
	require.NoError(t, err)
	p.Observed, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	r.Object.Data = &map[string]string{"value": "updated"}
	r.Object.BinaryData = &map[string]string{"bytes": "AgM="}
	r.Object.Immutable = new(true)
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.Object.Metadata.UID, updated.Object.Metadata.UID)
	require.True(t, updated.Object.Immutable)
	require.Equal(t, *r.Object.Data, updated.Object.Data)
	require.Equal(t, *r.Object.BinaryData, updated.Object.BinaryData)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	replacement, err := api.CoreV1().Secrets(namespace).Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: created.Object.Metadata.Name, Namespace: namespace},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, api.CoreV1().Secrets(namespace).Delete(context.Background(), replacement.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &replacement.UID}}))
	})
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorContains(t, err, "UID changed")
	err = r.Delete(t.Context(), nil, p)
	require.True(t, apierrors.IsConflict(err))
}
