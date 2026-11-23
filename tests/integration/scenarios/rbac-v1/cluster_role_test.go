//go:build integration

package integration_test

import (
	"context"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestClusterRoleLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.ClusterRoleResource{
		Metadata: svc.ClusterRoleMetadata{GenerateName: new("unobin-cluster-api-")},
		Rule: &[]svc.ClusterRoleRule{{Verbs: []string{"get", "list", "get"},
			APIGroups: &[]string{""}, Resources: &[]string{"pods"}, ResourceNames: &[]string{"one"}}}}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.ClusterRoleResource, *svc.ClusterRoleResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	require.Equal(t, []string{"get", "list", "get"}, created.Rule[0].Verbs)
	observed, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	p.Observed = observed
	r.Rule = &[]svc.ClusterRoleRule{{Verbs: []string{"watch", "get", "watch"},
		APIGroups: &[]string{""}, Resources: &[]string{"pods"}}}
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.Name, updated.Name)
	require.Equal(t, created.UID, updated.UID)
	live, err := api.RbacV1().ClusterRoles().Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, []rbacv1.PolicyRule{{Verbs: []string{"watch", "get", "watch"},
		APIGroups: []string{""}, Resources: []string{"pods"}}}, live.Rules)
	p.Outputs = updated
	p.Observed = nil
	p.Inputs = *r
	r.Rule = nil
	relinquished, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, updated.Rule, relinquished.Rule)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
