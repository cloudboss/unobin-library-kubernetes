//go:build integration

package integration_test

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestCompiledSecretLookupReadOnly(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	secrets := api.CoreV1().Secrets("default")
	mixed := []byte(" \xff\xfe雪\xff\uFFFD e\u0301\t\n")
	created, err := secrets.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-it-secret-lookup-",
			Labels: map[string]string{
				"app.kubernetes.io/name": "synthetic", "kubernetes.io/metadata.name": "kept",
			},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "9"},
		},
		Type: "example.test/custom", Immutable: new(true),
		Data: map[string][]byte{
			"text": mixed, "bytes": mixed, "unicode": []byte("日本語"),
			"empty-text": {}, "empty-bytes": {},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := secrets.Delete(ctx, created.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &created.UID},
		})
		require.NoError(t, err)
	})

	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/integration")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "secret-lookup-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(root, "_output/go/bin/unobin")
	}
	fixture, err := filepath.Abs("testdata/ub/secret-lookup")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), cli, "compile", "-p", fixture, "-o", work,
		"--name", "secret-lookup", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
	require.NoError(t, err)
	config := strings.ReplaceAll(string(body), "SECRET_LOOKUP_NAME", created.Name)
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), []byte(config), 0o600))

	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(work, "secret-lookup"), args...)
		cmd.Dir = work
		body, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", body)
	}
	run("pin", "-c", "config.ub")
	run("plan", "-c", "config.ub", "-o", "plan.ubp")
	run("apply", "plan.ubp")
	body, err = os.ReadFile(filepath.Join(work, "plan.ubp"))
	require.NoError(t, err)
	saved, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	store, err := local.NewStore(
		filepath.Join(work, "state"), saved.Factory.Name, saved.Stack, encrypters.Noop{},
	)
	require.NoError(t, err)
	snapshot, err := store.Current()
	require.NoError(t, err)
	existing := snapshot.Find("data-source.existing")
	require.NotNil(t, existing)
	require.Equal(t, map[string]any{
		"metadata": map[string]any{
			"name": created.Name, "namespace": created.Namespace, "generate-name": created.GenerateName,
			"generation": created.Generation, "resource-version": created.ResourceVersion,
			"uid": string(created.UID),
			"labels": map[string]any{
				"app.kubernetes.io/name": "synthetic", "kubernetes.io/metadata.name": "kept",
			},
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "9"},
		},
		"data": map[string]any{
			"text": " \uFFFD\uFFFD雪\uFFFD\uFFFD e\u0301\t\n", "empty-text": "", "unicode": "日本語",
		},
		"binary-data": map[string]any{
			"bytes": "IP/+6Zuq/++/vSBlzIEJCg==", "empty-bytes": "", "absent": "",
		},
		"type": "example.test/custom", "immutable": true,
	}, existing.Outputs)
	decoded, err := base64.StdEncoding.DecodeString(
		existing.Outputs["binary-data"].(map[string]any)["bytes"].(string),
	)
	require.NoError(t, err)
	require.Equal(t, mixed, decoded)

	absent := snapshot.Find("data-source.absent")
	require.NotNil(t, absent)
	require.Equal(t, map[string]any{
		"metadata": map[string]any{
			"name": created.Name + "-absent", "namespace": "default", "generate-name": "missing-",
			"generation": int64(0), "resource-version": "", "uid": "",
			"annotations": map[string]any{"EXAMPLE.COM/Lookup": "missing"},
			"labels":      map[string]any{"app": "missing"},
		},
		"data": map[string]any{}, "binary-data": map[string]any{"missing": "synthetic placeholder"},
		"type": "", "immutable": false,
	}, absent.Outputs)

	run("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
	run("apply", "destroy.ubp")
	snapshot, err = store.Current()
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
	after, err := secrets.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, created, after)
}
