//go:build integration

package nodes_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestNodesReadOnly(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	require.NoError(t, err)
	before, err := api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, before.Items)

	input := &svc.NodesDataSource{}
	all, err := input.Read(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, all.Nodes)
	want := make(map[string]string, len(before.Items))
	for _, node := range before.Items {
		want[node.Name] = string(node.UID)
	}
	ids := func(entries []svc.NodeLookupResult) map[string]string {
		result := make(map[string]string, len(entries))
		for _, node := range entries {
			require.NotContains(t, result, node.Metadata.Name)
			result[node.Metadata.Name] = node.Metadata.UID
		}
		return result
	}
	require.Equal(t, want, ids(*all.Nodes))

	hostname := before.Items[0].Labels[corev1.LabelHostname]
	require.NotEmpty(t, hostname)
	input.Metadata = &svc.NodesLookupMetadata{Labels: map[string]string{
		corev1.LabelHostname: hostname,
	}}
	filtered, err := input.Read(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, filtered.Nodes)
	selected := map[string]string{}
	for _, node := range before.Items {
		if node.Labels[corev1.LabelHostname] == hostname {
			selected[node.Name] = string(node.UID)
		}
	}
	require.Equal(t, selected, ids(*filtered.Nodes))

	input.Metadata.Labels = map[string]string{
		"unobin.cloudboss.com/nodes-integration-absent": "first",
	}
	empty, err := input.Read(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, new([]svc.NodeLookupResult{}), empty.Nodes)
}
