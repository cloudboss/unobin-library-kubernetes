//go:build integration

package cronjob_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/batch/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestCronJobAPILifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	name := "unobin-cron-job-" + strings.ToLower(rand.Text()[:10])
	input := &svc.CronJobResource{
		Metadata: svc.CronJobMetadata{Name: &name},
		Spec: svc.CronJobSpec{Schedule: "@hourly", Suspend: new(true),
			JobTemplate: svc.CronJobTemplate{Spec: svc.JobSpec{
				TTLSecondsAfterFinished: new("30"), Template: svc.PodTemplate{
					Spec: &svc.PodSpec{Container: new([]svc.Container{{Name: "app", Image: new("busybox:1.37"),
						Command: new([]string{"true"})}})},
				},
			}},
		}, DeleteTimeout: new("30s"),
	}
	created, err := input.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.CronJobResource, *svc.CronJobResourceOutput, *config.Configuration]{
		Inputs: *input, Outputs: created,
	}
	t.Cleanup(func() { require.NoError(t, input.Delete(context.Background(), nil, p)) })
	input.Metadata.Labels = new(map[string]string{"stage": "updated"})
	input.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = new("60")
	updated, err := input.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, new("60"), updated.Spec.JobTemplate.Spec.TTLSecondsAfterFinished)
	api, err := client.New(nil)
	require.NoError(t, err)
	live, err := api.BatchV1().CronJobs("default").Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, new(int32(60)), live.Spec.JobTemplate.Spec.TTLSecondsAfterFinished)
	p.Inputs, p.Outputs = *input, updated
	input.Metadata.Labels = nil
	updated, err = input.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Nil(t, updated.Metadata.Labels)
	p.Inputs, p.Outputs = *input, updated
	require.NoError(t, input.Delete(t.Context(), nil, p))
	_, err = input.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
