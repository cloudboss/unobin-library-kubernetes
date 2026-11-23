//go:build integration

package object_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func TestCustomObjectInfo(t *testing.T) {
	if os.Getenv("UNOBIN_KUBERNETES_INTEGRATION") != "1" || os.Getenv("KUBECONFIG") == "" {
		t.Skip("requires an explicit Kubernetes integration cluster")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	group := "object-info-" + strings.ToLower(rand.Text()[:10]) + ".unobin.cloudboss.io"
	source, err := os.ReadFile("testdata/widget-crd.yaml")
	require.NoError(t, err)
	createLookupObject(t, ctx, strings.ReplaceAll(string(source), "GROUP", group))

	version := group + "/v2"
	gvk := schema.GroupVersionKind{Group: group, Version: "v2", Kind: "Widget"}
	var widgets dynamic.ResourceInterface
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			api, err := client.NewDynamicContext(ctx, nil)
			if err != nil {
				return false, err
			}
			widgets, _, err = api.Resolve(gvk, "default")
			if meta.IsNoMatchError(err) {
				return false, nil
			}
			return err == nil, err
		})
	require.NoError(t, err)
	spec := map[string]any{
		"nested": map[string]any{
			"targets": []any{int64(8080), "https"},
			"serial":  int64(9007199254740993),
		},
	}
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": version, "kind": "Widget", "spec": spec,
		"metadata": map[string]any{"name": "example", "namespace": "default"},
	})
	require.NoError(t, err)
	createLookupObject(t, ctx, string(manifest))

	live, err := widgets.Get(ctx, "example", metav1.GetOptions{})
	require.NoError(t, err)
	lookup := svc.ObjectDataSource{
		APIVersion: version, Kind: "Widget", Metadata: svc.ObjectLookupMetadata{Name: "example"},
	}
	output, err := lookup.Read(ctx, &config.Configuration{Namespace: new("other")})
	require.NoError(t, err)
	require.Equal(t, &svc.ObjectDataSourceOutput{Object: &live.Object}, output)
	require.Equal(t, spec, (*output.Object)["spec"])
}

func createLookupObject(t *testing.T, ctx context.Context, manifest string) {
	t.Helper()
	resource := svc.ObjectResource{Manifest: manifest, DeleteTimeout: new("20s")}
	out, err := resource.Create(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resource.Delete(context.Background(), nil,
			runtime.Prior[svc.ObjectResource, *svc.ObjectResourceOutput, *config.Configuration]{
				Inputs: resource, Outputs: out,
			}))
	})
}
