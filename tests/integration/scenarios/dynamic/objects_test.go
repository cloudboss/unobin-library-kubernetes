//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestCompiledObjectsOnKubernetes(t *testing.T) {
	if os.Getenv("UNOBIN_KUBERNETES_INTEGRATION") != "1" || os.Getenv("KUBECONFIG") == "" {
		t.Skip("requires an explicit Kubernetes integration cluster")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	namespace := "dynamic-objects-" + strings.ToLower(rand.Text()[:10])
	group := namespace + ".unobin.cloudboss.io"
	core, err := client.New(nil)
	require.NoError(t, err)
	_, err = core.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		require.NoError(t, core.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}))
	})
	for name, tier := range map[string]string{
		"edge-first": "edge", "edge-second": "edge", "unrelated": "storage",
	} {
		_, err := core.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"tier": tier}},
			Data:       map[string]string{"value": name},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	mice := createMouseCRD(t, ctx, namespace, group)
	spec := map[string]any{
		"targets": []any{int64(8080), "https", false, nil},
		"serial":  int64(9007199254740993),
	}
	mouse, err := mice.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v2", "kind": "Mouse",
		"metadata": map[string]any{"name": "example", "namespace": namespace},
		"spec":     spec,
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Equal(t, spec, mouse.Object["spec"])

	api, err := client.NewDynamicContext(ctx, nil)
	require.NoError(t, err)
	configMaps, _, err := api.Resolve(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"},
		namespace)
	require.NoError(t, err)
	first, err := configMaps.Get(ctx, "edge-first", metav1.GetOptions{})
	require.NoError(t, err)
	second, err := configMaps.Get(ctx, "edge-second", metav1.GetOptions{})
	require.NoError(t, err)
	page, err := configMaps.List(ctx, metav1.ListOptions{LabelSelector: "tier=edge", Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, page.GetContinue())
	require.Equal(t, []unstructured.Unstructured{*first}, page.Items)

	run := compileObjectsFactory(t, namespace, group)
	run("pin", "-c", "config.ub")
	run("plan", "-c", "config.ub", "-o", "objects.ubp")
	run("apply", "objects.ubp")
	snapshot, err := state.DecodeSnapshot(run("state", "pull", "-c", "config.ub"))
	require.NoError(t, err)
	for name, items := range map[string][]any{
		"selected": {second.Object},
		"limited":  {first.Object},
		"empty":    {},
		"custom":   {mouse.Object},
	} {
		entry := snapshot.Find("data-source." + name)
		require.NotNil(t, entry)
		require.Equal(t, map[string]any{"objects": items}, entry.Outputs)
	}
	run("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
	run("apply", "destroy.ubp")
	snapshot, err = state.DecodeSnapshot(run("state", "pull", "-c", "config.ub"))
	require.NoError(t, err)
	for _, name := range []string{"selected", "limited", "empty", "custom"} {
		require.Nil(t, snapshot.Find("data-source."+name))
	}
}

func createMouseCRD(
	t *testing.T, ctx context.Context, namespace, group string,
) dynamic.ResourceInterface {
	t.Helper()
	source, err := os.ReadFile("testdata/mouse-crd.yaml")
	require.NoError(t, err)
	crd := &unstructured.Unstructured{}
	err = yaml.Unmarshal([]byte(strings.ReplaceAll(string(source), "GROUP", group)), &crd.Object)
	require.NoError(t, err)
	api, err := client.NewDynamicContext(ctx, nil)
	require.NoError(t, err)
	crds, _, err := api.Resolve(schema.GroupVersionKind{
		Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition",
	}, "")
	require.NoError(t, err)
	_, err = crds.Create(ctx, crd, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		require.NoError(t, crds.Delete(cleanup, crd.GetName(), metav1.DeleteOptions{}))
	})

	gvk := schema.GroupVersionKind{Group: group, Version: "v2", Kind: "Mouse"}
	var mice dynamic.ResourceInterface
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			api, err := client.NewDynamicContext(ctx, nil)
			if err != nil {
				return false, err
			}
			mice, _, err = api.Resolve(gvk, namespace)
			if meta.IsNoMatchError(err) {
				return false, nil
			}
			return err == nil, err
		})
	require.NoError(t, err)
	return mice
}

func compileObjectsFactory(t *testing.T, namespace, group string) func(...string) []byte {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	factory, err := filepath.Abs("testdata/ub/objects/factory.ub")
	require.NoError(t, err)
	dir := filepath.Join(root, "_output", "integration")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	work, err := os.MkdirTemp(dir, "dynamic-objects-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })

	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work, StackName: "dynamic-objects",
		Version: "v0.0.0", CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	config, err := os.ReadFile("testdata/ub/objects/config.ub")
	require.NoError(t, err)
	configText := strings.NewReplacer("LOOKUP_NAMESPACE", namespace, "LOOKUP_GROUP", group).
		Replace(string(config))
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), []byte(configText), 0o600))
	return func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%v\n%s", args, out)
		return out
	}
}
