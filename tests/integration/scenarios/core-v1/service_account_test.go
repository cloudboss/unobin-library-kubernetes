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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestServiceAccountLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit"}[explicit], func(t *testing.T) {
			input := &svc.ServiceAccountResource{Object: svc.ServiceAccountObject{
				Metadata: svc.ServiceAccountMetadata{GenerateName: new("unobin-service-account-"),
					Labels: &map[string]string{"value": "first"}},
				ImagePullSecrets: &[]svc.ServiceAccountReference{{Name: new("first")}},
				Secrets:          &[]svc.ServiceAccountReference{{Name: new("first")}},
			}}
			if explicit {
				input.Object.AutomountServiceAccountToken = new(false)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			created, err := input.Create(ctx, nil)
			require.NoError(t, err)
			prior := runtime.Prior[svc.ServiceAccountResource,
				*svc.ServiceAccountResourceOutput,
				*config.Configuration]{
				Inputs: *input, Outputs: created,
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				require.NoError(t, input.Delete(ctx, nil, prior))
			})
			require.Equal(t, !explicit, created.Object.AutomountServiceAccountToken)
			require.Empty(t, created.DefaultSecretName)
			observed, err := input.Read(ctx, nil, prior)
			require.NoError(t, err)
			require.Equal(t, created, observed)
			prior.Observed = observed
			input.Object.Metadata.Labels = &map[string]string{"value": "updated"}
			input.Object.ImagePullSecrets = &[]svc.ServiceAccountReference{{Name: new("updated")}}
			input.Object.Secrets = &[]svc.ServiceAccountReference{{Name: new("updated")}}
			input.Object.AutomountServiceAccountToken = new(true)
			updated, err := input.Update(ctx, nil, prior)
			require.NoError(t, err)
			require.Equal(t, created.Object.Metadata.UID, updated.Object.Metadata.UID)
			live,
				err := api.CoreV1().ServiceAccounts("default").Get(ctx,
				created.Object.Metadata.Name,
				metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, "updated", live.Labels["value"])
			require.Equal(t, []corev1.LocalObjectReference{{Name: "updated"}}, live.ImagePullSecrets)
			require.Equal(t, []corev1.ObjectReference{{Name: "updated"}}, live.Secrets)
			require.True(t, *live.AutomountServiceAccountToken)
			require.NoError(t, input.Delete(ctx, nil, prior))
			require.NoError(t, wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 5*time.Second, true,
				func(ctx context.Context) (bool, error) {
					_,
						err := api.CoreV1().ServiceAccounts("default").Get(ctx,
						created.Object.Metadata.Name,
						metav1.GetOptions{})
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return false, err
				}))
		})
	}
}
