//go:build integration

package persistentvolumeclaiminfo_test

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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestCompiledPersistentVolumeClaimInfo(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	claims := api.CoreV1().PersistentVolumeClaims("default")
	claim, err := claims.Create(t.Context(), &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-pvc-lookup-",
			Annotations:  map[string]string{"lookup.example/purpose": "fixture"},
			Labels:       map[string]string{"lookup.example/role": "fixture"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: new(""),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"lookup.example/role": "fixture"},
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
			_, err := claims.Patch(ctx, claim.Name, types.MergePatchType,
				[]byte(`{"metadata":{"finalizers":null}}`), metav1.PatchOptions{})
			require.NoError(t, err)
		}
		require.NoError(t, claims.Delete(ctx, claim.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &claim.UID},
		}))
		require.NoError(t, wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second,
			true, func(ctx context.Context) (bool, error) {
				_, err := claims.Get(ctx, claim.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				return false, err
			}))
	})
	require.Equal(t, corev1.ClaimPending, claim.Status.Phase)
	require.Empty(t, claim.Spec.VolumeName)

	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	work, err := os.MkdirTemp(filepath.Join(root, "_output/integration"), "pvc-info-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(root, "_output/go/bin/unobin")
	}
	fixture, err := filepath.Abs("testdata/ub/lookup")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), cli, "compile", "-p", fixture, "-o", work,
		"--name", "pvc-info", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
	require.NoError(t, err)
	config := strings.ReplaceAll(string(body), "PVC_INFO_NAME", claim.Name)
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), []byte(config), 0o600))
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(work, "pvc-info"), args...)
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
		"actual-name": claim.Name, "namespace": "default", "uid": string(claim.UID),
		"generate-name": claim.GenerateName, "access-modes": []any{"ReadWriteOnce"},
		"requests": map[string]any{"storage": "1Gi"}, "volume-name": "", "class": "",
		"selector":     map[string]any{"lookup.example/role": "fixture"},
		"missing-name": claim.Name + "-absent", "missing-spec": nil,
	}, outputs.Outputs)
	run("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
	run("apply", "destroy.ubp")
	after, err := claims.Get(t.Context(), claim.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, claim.UID, after.UID)
	require.Equal(t, claim.Spec, after.Spec)
	require.Equal(t, corev1.ClaimPending, after.Status.Phase)
}
