//go:build integration

package podinfo_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestCompiledPodInfo(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	pods := api.CoreV1().Pods("default")
	const image = "registry.k8s.io/pause:3.10"
	pod, err := pods.Create(t.Context(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-pod-info-",
			Annotations:  map[string]string{"deployment.kubernetes.io/revision": "1"},
			Labels:       map[string]string{"pod-template-hash": "lookup-fixture"},
		},
		Spec: corev1.PodSpec{
			SchedulerName:                "unobin-unscheduled",
			AutomountServiceAccountToken: new(false),
			Containers:                   []corev1.Container{{Name: "test", Image: image}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{
			Preconditions:      &metav1.Preconditions{UID: &pod.UID},
			GracePeriodSeconds: new(int64(0)),
		})
		require.NoError(t, err)
		require.NoError(t, wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second,
			true, func(ctx context.Context) (bool, error) {
				_, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				return false, err
			}))
	})
	require.Equal(t, corev1.PodPending, pod.Status.Phase)

	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/integration")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "pod-info-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(root, "_output/go/bin/unobin")
	}
	fixture, err := filepath.Abs("testdata/ub/lookup")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), cli, "compile", "-p", fixture, "-o", work,
		"--name", "pod-info", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
	require.NoError(t, err)
	config := strings.ReplaceAll(string(body), "POD_INFO_NAME", pod.Name)
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), []byte(config), 0o600))
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(work, "pod-info"), args...)
		cmd.Dir = work
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return output
	}
	run("pin", "-c", "config.ub")
	run("plan", "-c", "config.ub", "-o", "plan.ubp")
	run("apply", "plan.ubp")
	var outputs struct {
		Outputs map[string]any `json:"outputs"`
	}
	require.NoError(t, json.Unmarshal(run("output", "--format", "json", "-c", "config.ub"), &outputs))
	require.Equal(t, map[string]any{
		"name": pod.Name, "namespace": "default", "prefix": pod.GenerateName, "uid": string(pod.UID),
		"annotations": map[string]any{"deployment.kubernetes.io/revision": "1"},
		"labels":      map[string]any{"pod-template-hash": "lookup-fixture"},
		"phase":       "Pending", "node": "", "scheduler": "unobin-unscheduled",
		"images": []any{image}, "grace": float64(30), "volumes": []any{},
		"missing-metadata": map[string]any{
			"name": pod.Name + "-absent", "generate-name": "missing-", "namespace": "default",
			"annotations": map[string]any{"EXAMPLE.COM/Lookup": "missing"},
			"labels":      map[string]any{"app": "missing"},
			"generation":  float64(0), "resource-version": "", "uid": "",
		},
		"missing-phase": nil, "missing-spec": nil,
	}, outputs.Outputs)
	run("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
	run("apply", "destroy.ubp")
	after, err := pods.Get(t.Context(), pod.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, pod.UID, after.UID)
	require.Equal(t, pod.ResourceVersion, after.ResourceVersion)
	require.Equal(t, pod.Spec, after.Spec)
	require.Equal(t, pod.Status, after.Status)
}
