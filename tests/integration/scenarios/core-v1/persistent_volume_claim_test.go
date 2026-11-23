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
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestPersistentVolumeClaimLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	claims := api.CoreV1().PersistentVolumeClaims("default")
	r := &svc.PersistentVolumeClaimResource{Object: svc.PersistentVolumeClaimObject{
		Metadata: svc.ConfigMapMetadata{GenerateName: new("unobin-api-claim-"), Namespace: new("default"),
			Labels: &map[string]string{"value": "first"}}, Spec: svc.PersistentVolumeClaimSpec{
			AccessModes: []string{"ReadWriteOnce"}, Resources: svc.PersistentVolumeClaimResources{
				Requests: &map[string]string{"storage": "1Gi"}}}}, WaitUntilBound: new(false)}
	out, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.PersistentVolumeClaimResource, *svc.PersistentVolumeClaimResourceOutput,
		*config.Configuration]{Inputs: *r, Outputs: out}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The API-only harness has no protection controller to release this unused claim.
		if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
			_, err := claims.Patch(ctx, out.Object.Metadata.Name, types.MergePatchType,
				[]byte(`{"metadata":{"finalizers":null}}`), metav1.PatchOptions{})
			if !apierrors.IsNotFound(err) {
				require.NoError(t, err)
			}
		}
		require.NoError(t, r.Delete(ctx, nil, p))
		live, err := claims.Get(ctx, out.Object.Metadata.Name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			require.NoError(t, err)
			require.NotEqual(t, out.Object.Metadata.UID, string(live.UID))
		}
	})
	require.True(t, strings.HasPrefix(out.Object.Metadata.Name, "unobin-api-claim-"))
	require.Equal(t, map[string]string{"storage": "1Gi"}, out.Object.Spec.Resources.Requests)
	require.Equal(t, []string{"ReadWriteOnce"}, out.Object.Spec.AccessModes)
	p.Observed, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	r.Object.Metadata.Labels = &map[string]string{"value": "updated"}
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, out.Object.Metadata.UID, updated.Object.Metadata.UID)
	require.Equal(t, "updated", updated.Object.Metadata.Labels["value"])
}
