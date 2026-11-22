//go:build integration

package job_test

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

func TestJobAPILifecycle(t *testing.T) { testJobLifecycle(t, false) }
func TestJobControllerCompletion(t *testing.T) {
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		t.Skip("API-only backend has no Job controller")
	}
	testJobLifecycle(t, true)
}

func testJobLifecycle(t *testing.T, waiting bool) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	name := "unobin-job-" + strings.ToLower(rand.Text()[:10])
	r := &svc.JobResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.JobSpec{Template: svc.PodTemplate{
			Metadata: svc.TemplateMetadata{},
			Spec: &svc.PodSpec{Container: new([]svc.Container{{
				Name: "app", Image: new("busybox:1.37"), Command: new([]string{"true"}),
			}})},
		}},
		WaitForCompletion: &waiting, DeleteTimeout: new("30s"),
	}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.JobResource, *svc.JobResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	r.Spec.Parallelism = new(int64(2))
	r.Spec.BackoffLimit = new(int64(4))
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, int64(2), *updated.Spec.Parallelism)
	api, err := client.New(nil)
	require.NoError(t, err)
	live, err := api.BatchV1().Jobs("default").Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, int32(4), *live.Spec.BackoffLimit)
	p.Inputs = *r
	p.Outputs = updated
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
