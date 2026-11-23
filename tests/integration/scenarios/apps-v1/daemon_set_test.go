//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type daemonSetPrior = runtime.Prior[
	svc.DaemonSetResource, *svc.DaemonSetResourceOutput, *config.Configuration,
]

func daemonSetAPI(t *testing.T) typedappsv1.DaemonSetInterface {
	t.Helper()
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	return api.AppsV1().DaemonSets("default")
}

func daemonSetInput(t *testing.T) *svc.DaemonSetResource {
	t.Helper()
	name := "unobin-daemon-set-" + strings.ToLower(rand.Text()[:10])
	return &svc.DaemonSetResource{
		Metadata: svc.Metadata{Name: &name},
		Spec: svc.DaemonSetSpec{
			Selector: &svc.Selector{MatchLabels: new(map[string]string{"app": name})},
			Template: svc.DaemonSetPodTemplate{
				Metadata: svc.Metadata{Labels: new(map[string]string{"app": name})},
				Spec: &svc.PodSpec{Container: new([]svc.Container{{
					Name: "app", Image: new("registry.k8s.io/pause:3.9"),
				}})},
			},
		},
		CreateTimeout: new("3m"), UpdateTimeout: new("3m"), DeleteTimeout: new("30s"),
	}
}

func TestDaemonSetAPILifecycle(t *testing.T) {
	api := daemonSetAPI(t)
	r := daemonSetInput(t)
	r.WaitForRollout = new(false)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := daemonSetPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	r.Spec.Template.Spec.Container = new([]svc.Container{{
		Name: "app", Image: new("registry.k8s.io/pause:3.10"),
	}})
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "registry.k8s.io/pause:3.10", live.Spec.Template.Spec.Containers[0].Image)
	p = daemonSetPrior{Inputs: *r, Outputs: updated}
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, updated, read)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}

func TestDaemonSetControllerRollout(t *testing.T) {
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		t.Skip("requires a cluster with working nodes")
	}
	api := daemonSetAPI(t)
	r := daemonSetInput(t)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := daemonSetPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assertReadyDaemonSet(t, live)
	r.Spec.Template.Spec.Container = new([]svc.Container{{
		Name: "app", Image: new("registry.k8s.io/pause:3.10"),
	}})
	r.Spec.Template.Metadata.Annotations = new(map[string]string{"rollout": "updated"})
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	live, err = api.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assertReadyDaemonSet(t, live)
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
				require.Equal(t, "registry.k8s.io/pause:3.10", pod.Spec.Containers[0].Image)
				require.Equal(t, "updated", pod.Annotations["rollout"])
				ready++
			}
		}
	}
	require.Equal(t, int(live.Status.DesiredNumberScheduled), ready)
	p = daemonSetPrior{Inputs: *r, Outputs: updated}
	require.NoError(t, r.Delete(t.Context(), nil, p))
}

func assertReadyDaemonSet(t *testing.T, obj *appsv1.DaemonSet) {
	t.Helper()
	require.Equal(t, obj.Generation, obj.Status.ObservedGeneration)
	require.Positive(t, obj.Status.DesiredNumberScheduled)
	require.Equal(t, obj.Status.DesiredNumberScheduled, obj.Status.NumberReady)
}

func TestDaemonSetAPIValidation(t *testing.T) {
	api := daemonSetAPI(t)
	for _, value := range []string{"OnFailure", "Never", "both zero"} {
		t.Run(value, func(t *testing.T) {
			r := daemonSetInput(t)
			r.WaitForRollout = new(false)
			switch value {
			case "OnFailure", "Never":
				r.Spec.Template.Spec.RestartPolicy = &value
			default:
				r.Spec.Strategy = &svc.DaemonSetStrategy{RollingUpdate: &svc.DaemonSetRollingUpdate{
					MaxSurge: new("0"), MaxUnavailable: new("0"),
				}}
			}
			out, err := r.Create(t.Context(), nil)
			if out != nil {
				require.NoError(t, r.Delete(t.Context(), nil, daemonSetPrior{Inputs: *r, Outputs: out}))
			}
			require.True(t, apierrors.IsInvalid(err), "%v", err)
		})
	}
	t.Run("OnDelete rolling", func(t *testing.T) {
		r := daemonSetInput(t)
		r.WaitForRollout = new(false)
		r.Spec.Strategy = &svc.DaemonSetStrategy{
			Type: new("OnDelete"),
			RollingUpdate: &svc.DaemonSetRollingUpdate{
				MaxSurge: new("0"), MaxUnavailable: new("0"),
			},
		}
		created, err := r.Create(t.Context(), nil)
		if created != nil {
			p := daemonSetPrior{Inputs: *r, Outputs: created}
			t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
		}
		require.NoError(t, err)
		require.NotNil(t, created)
		require.Equal(t, *r.Metadata.Name, created.Name)
		require.Equal(t, "default", created.Namespace)
		require.NotEmpty(t, created.UID)
		live, err := api.Get(t.Context(), created.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, live.Name, created.Name)
		require.Equal(t, live.Namespace, created.Namespace)
		require.Equal(t, string(live.UID), created.UID)
		require.Equal(t, appsv1.OnDeleteDaemonSetStrategyType, live.Spec.UpdateStrategy.Type)
		strategy := &svc.DaemonSetStrategy{Type: new(string(live.Spec.UpdateStrategy.Type))}
		if rolling := live.Spec.UpdateStrategy.RollingUpdate; rolling != nil {
			strategy.RollingUpdate = &svc.DaemonSetRollingUpdate{
				MaxSurge:       new(rolling.MaxSurge.String()),
				MaxUnavailable: new(rolling.MaxUnavailable.String()),
			}
		}
		require.Equal(t, strategy, created.Spec.Strategy)
		read, err := r.Read(t.Context(), nil, daemonSetPrior{Inputs: *r, Outputs: created})
		require.NoError(t, err)
		require.Equal(t, created, read)
	})
}
