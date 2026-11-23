//go:build integration

package integration_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestRoleLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	r := &svc.RoleResource{
		Metadata: svc.RoleMetadata{GenerateName: new("unobin-role-api-")},
		Rule: []svc.RoleRule{{APIGroups: []string{""}, Resources: []string{"pods"},
			Verbs: []string{"list", "get", "get"}, ResourceNames: &[]string{"one"}}},
	}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[svc.RoleResource, *svc.RoleResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	require.True(t, strings.HasPrefix(created.Name, *r.Metadata.GenerateName))
	require.Equal(t, "default", created.Namespace)
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	p.Observed = read
	r.Rule = []svc.RoleRule{{APIGroups: []string{""}, Resources: []string{"pods"},
		Verbs: []string{"watch", "get", "list"}}}
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.Name, updated.Name)
	require.Equal(t, created.UID, updated.UID)
	live, err := api.RbacV1().Roles("default").Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"get", "list", "watch"}, live.Rules[0].Verbs)
	require.Empty(t, live.Rules[0].ResourceNames)
	require.Equal(t, created.UID, string(live.UID))
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	_, err = api.RbacV1().Roles("default").Get(t.Context(), created.Name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "%v", err)
}
