package v1

import (
	"reflect"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
)

func TestClusterRoleBindingStableID(t *testing.T) {
	reg := Library().Resources["cluster-role-binding"]
	id, err := reg.StableID(nil, map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
	_, err = reg.StableID(nil, map[string]any{})
	require.Error(t, err)
}

func clusterBindingReasons(
	t *testing.T, before, after svc.ClusterRoleBindingResource,
	saved, observed *svc.ClusterRoleBindingResourceOutput, current *config.Configuration,
) []string {
	t.Helper()
	reg := Library().Resources["cluster-role-binding"]
	method := reflect.ValueOf(reg).MethodByName("ReplacementReasons")
	prior := reflect.New(method.Type().In(2)).Elem()
	encode := func(value any) map[string]any {
		body, err := ub.Marshal(value)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, ub.Unmarshal(body, &out))
		return out
	}
	for key, value := range map[string]map[string]any{
		"Inputs": encode(before), "Outputs": encode(saved), "Observed": encode(observed),
		"Configuration": {},
	} {
		prior.FieldByName(key).Set(reflect.ValueOf(value))
	}
	result := method.Call([]reflect.Value{
		reflect.ValueOf(&after), reflect.ValueOf(current), prior,
	})
	require.True(t, result[1].IsNil(), "%v", result[1])
	return result[0].Interface().([]string)
}

func TestClusterRoleBindingReplacementAndEquality(t *testing.T) {
	before := svc.ClusterRoleBindingResource{
		Metadata: svc.ClusterRoleMetadata{Name: new("system:reader")},
		RoleRef: svc.ClusterRoleBindingRoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader",
		},
		Subject: []svc.ClusterRoleBindingSubject{{Kind: "User", Name: "alice", APIGroup: new("known")}},
	}
	after := before
	after.Metadata.Annotations, after.Metadata.Labels = &map[string]string{}, &map[string]string{}
	after.Subject = []svc.ClusterRoleBindingSubject{
		{Kind: "User", Name: "alice", Namespace: new("default")},
	}
	reg := Library().Resources["cluster-role-binding"]
	body, err := ub.Marshal(before)
	require.NoError(t, err)
	var encoded map[string]any
	require.NoError(t, ub.Unmarshal(body, &encoded))
	equal, err := reg.InputsEqual(&after, encoded)
	require.NoError(t, err)
	require.True(t, equal)
	omitted := before
	omitted.Subject = []svc.ClusterRoleBindingSubject{{Kind: "User", Name: "alice"}}
	explicitEmpty := omitted
	explicitEmpty.Subject = []svc.ClusterRoleBindingSubject{
		{Kind: "User", Name: "alice", APIGroup: new("")},
	}
	body, err = ub.Marshal(omitted)
	require.NoError(t, err)
	encoded = nil
	require.NoError(t, ub.Unmarshal(body, &encoded))
	equal, err = reg.InputsEqual(&explicitEmpty, encoded)
	require.NoError(t, err)
	require.False(t, equal, "a new explicit empty group must replace a computed value")
	saved := &svc.ClusterRoleBindingResourceOutput{RoleRef: before.RoleRef}
	require.Empty(t, clusterBindingReasons(t, before, after, saved, saved, &config.Configuration{}))
	for _, field := range []string{
		"name", "generate-name", "api-group", "kind", "ref-name",
		"server", "kubeconfig", "current-context",
	} {
		t.Run(field, func(t *testing.T) {
			changed := before
			current := &config.Configuration{}
			want := "metadata"
			switch field {
			case "name":
				changed.Metadata.Name = new("other")
			case "generate-name":
				changed.Metadata.Name, changed.Metadata.GenerateName = nil, new("other-")
			case "api-group":
				changed.RoleRef.APIGroup, want = "other", "role-ref"
			case "kind":
				changed.RoleRef.Kind, want = "ClusterRole", "role-ref"
			case "ref-name":
				changed.RoleRef.Name, want = "other", "role-ref"
			default:
				require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
				want = field
			}
			require.Equal(t, []string{want},
				clusterBindingReasons(t, before, changed, saved, saved, current))
		})
	}
	observed := *saved
	observed.RoleRef.Name = "drift"
	require.Equal(t, []string{"role-ref"}, clusterBindingReasons(t, before, before, saved, &observed,
		&config.Configuration{}))
	require.Empty(t, clusterBindingReasons(t, before, before, saved, saved,
		&config.Configuration{Token: new("new"), Namespace: new("configured")}))
}

func TestClusterRoleBindingRestoredNameReplacement(t *testing.T) {
	before := svc.ClusterRoleBindingResource{
		Metadata: svc.ClusterRoleMetadata{},
		RoleRef: svc.ClusterRoleBindingRoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader",
		},
		Subject: []svc.ClusterRoleBindingSubject{{Kind: "User", Name: "alice"}},
	}
	after := before
	after.Metadata.Name = new("system:reader")
	saved := &svc.ClusterRoleBindingResourceOutput{Name: "system:reader", RoleRef: before.RoleRef}
	require.Empty(t, clusterBindingReasons(t, before, after, saved, saved, &config.Configuration{}))
	observed := *saved
	observed.RoleRef.Name = "drift"
	require.Equal(t, []string{"role-ref"}, clusterBindingReasons(t, before, after, saved, &observed,
		&config.Configuration{}))
	require.Equal(t, []string{"server"}, clusterBindingReasons(t, before, after, saved, saved,
		&config.Configuration{Server: new("https://other")}))
	after.RoleRef.Name = "changed"
	require.Equal(t, []string{"role-ref"}, clusterBindingReasons(t, before, after, saved, saved,
		&config.Configuration{}))
	after.RoleRef = before.RoleRef
	after.Metadata.Name = new("system:renamed")
	require.Equal(t, []string{"metadata"}, clusterBindingReasons(t, before, after, saved, saved,
		&config.Configuration{}))
}
