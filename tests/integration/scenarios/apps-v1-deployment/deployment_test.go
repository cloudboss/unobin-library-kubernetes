//go:build integration

package deployment_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type deploymentPrior = runtime.Prior[
	svc.DeploymentResource, *svc.DeploymentResourceOutput, *config.Configuration,
]

func deploymentAPI(t *testing.T) typedappsv1.DeploymentInterface {
	t.Helper()
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	return api.AppsV1().Deployments("default")
}

func deploymentInput(t *testing.T) *svc.DeploymentResource {
	t.Helper()
	name := "unobin-deployment-" + strings.ToLower(rand.Text()[:10])
	return &svc.DeploymentResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.DeploymentSpec{
			Replicas: new("1"),
			Selector: &svc.Selector{MatchLabels: new(map[string]string{"app": name})},
			Template: svc.PodTemplate{
				Metadata: svc.Metadata{Labels: new(map[string]string{"app": name})},
				Spec: svc.PodSpec{Container: new([]svc.Container{{
					Name: "app", Image: new("registry.k8s.io/pause:3.9"),
				}})},
			},
		},
		CreateTimeout: new("3m"), UpdateTimeout: new("3m"), DeleteTimeout: new("30s"),
	}
}

func TestDeploymentAPILifecycle(t *testing.T) {
	api := deploymentAPI(t)
	r := deploymentInput(t)
	r.WaitForRollout = new(false)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := deploymentPrior{Inputs: *r, Outputs: created}
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
	p = deploymentPrior{Inputs: *r, Outputs: updated}
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, updated, read)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}

func TestDeploymentControllerRollout(t *testing.T) {
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		t.Skip("requires a cluster with working nodes")
	}
	api := deploymentAPI(t)
	r := deploymentInput(t)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := deploymentPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assertReady(t, live, 1)
	r.Spec.Replicas = new("2")
	r.Spec.Template.Spec.Container = new([]svc.Container{{
		Name: "app", Image: new("registry.k8s.io/pause:3.10"),
	}})
	r.Spec.Template.Metadata.Annotations = new(map[string]string{"rollout": "updated"})
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	live, err = api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assertReady(t, live, 2)
	require.Equal(t, "registry.k8s.io/pause:3.10", live.Spec.Template.Spec.Containers[0].Image)
	core, err := client.New(nil)
	require.NoError(t, err)
	pods, err := core.CoreV1().Pods("default").List(t.Context(), metav1.ListOptions{
		LabelSelector: "app=" + created.Name,
	})
	require.NoError(t, err)
	ready := 0
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" &&
				pod.DeletionTimestamp == nil {
				ready++
			}
		}
	}
	require.Equal(t, 2, ready)
	p = deploymentPrior{Inputs: *r, Outputs: updated}
	require.NoError(t, r.Delete(t.Context(), nil, p))
}

func assertReady(t *testing.T, obj *appsv1.Deployment, replicas int32) {
	t.Helper()
	require.Equal(t, obj.Generation, obj.Status.ObservedGeneration)
	require.Equal(t, replicas, obj.Status.UpdatedReplicas)
	require.Equal(t, replicas, obj.Status.ReadyReplicas)
	require.Equal(t, replicas, obj.Status.AvailableReplicas)
}
