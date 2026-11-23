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

func TestRoleBindingLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.RoleBindingResource{
		Metadata: svc.RoleMetadata{GenerateName: new("unobin-binding-api-")},
		RoleRef:  svc.RoleBindingRoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
		Subject: []svc.RoleBindingSubject{
			{Kind: "User", Name: "alice", Namespace: new("")},
			{Kind: "Group", Name: "readers", APIGroup: new(""), Namespace: new("")},
			{Kind: "ServiceAccount", Name: "reader"},
		},
	}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.RoleBindingResource, *svc.RoleBindingResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	require.Equal(t, "default", created.Namespace)
	require.Equal(t, rbacv1.GroupName, created.Subject[0].APIGroup)
	require.Empty(t, created.Subject[1].APIGroup)
	require.Empty(t, created.Subject[2].APIGroup)
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	p.Observed = read
	r.Metadata.Annotations = &map[string]string{"test.unobin.cloudboss.io/managed": "first"}
	r.Subject = []svc.RoleBindingSubject{
		{Kind: "ServiceAccount", Name: "second"},
		{Kind: "ServiceAccount", Name: "second"},
	}
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	live, err := api.RbacV1().RoleBindings("default").Get(
		t.Context(), created.Name, metav1.GetOptions{},
	)
	require.NoError(t, err)
	require.Equal(t, []rbacv1.Subject{
		{Kind: "ServiceAccount", Name: "second", Namespace: "default"},
		{Kind: "ServiceAccount", Name: "second", Namespace: "default"},
	}, live.Subjects)
	require.Equal(t, rbacv1.RoleRef(r.RoleRef), live.RoleRef)
	require.Equal(t, "first", live.Annotations["test.unobin.cloudboss.io/managed"])
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
