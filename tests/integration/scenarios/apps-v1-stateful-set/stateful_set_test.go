//go:build integration

package statefulset_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type statefulSetPrior = runtime.Prior[
	svc.StatefulSetResource, *svc.StatefulSetResourceOutput, *config.Configuration,
]

func statefulSetAPI(t *testing.T) typedappsv1.StatefulSetInterface {
	t.Helper()
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	return api.AppsV1().StatefulSets("default")
}

func statefulSetInput(t *testing.T) *svc.StatefulSetResource {
	t.Helper()
	name := "unobin-stateful-set-" + strings.ToLower(rand.Text()[:10])
	return &svc.StatefulSetResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.StatefulSetSpec{
			ServiceName: name,
			Replicas:    new("1"),
			Selector:    svc.Selector{MatchLabels: new(map[string]string{"app": name})},
			Template: svc.StatefulSetPodTemplate{
				Metadata: svc.Metadata{Labels: new(map[string]string{"app": name})},
				Spec: &svc.StatefulSetPodSpec{Container: new([]svc.Container{{
					Name: "app", Image: new("registry.k8s.io/pause:3.9"),
				}})},
			},
		},
		CreateTimeout: new("3m"), UpdateTimeout: new("3m"), DeleteTimeout: new("30s"),
	}
}

func TestStatefulSetAPILifecycle(t *testing.T) {
	api := statefulSetAPI(t)
	r := statefulSetInput(t)
	r.WaitForRollout = new(false)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := statefulSetPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	r.Spec.Replicas = new("2")
	r.Spec.Template.Spec.Container = new([]svc.Container{{
		Name: "app", Image: new("registry.k8s.io/pause:3.10"),
	}})
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, "2", *updated.Spec.Replicas)
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "registry.k8s.io/pause:3.10", live.Spec.Template.Spec.Containers[0].Image)
	p = statefulSetPrior{Inputs: *r, Outputs: updated}
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, updated, read)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
