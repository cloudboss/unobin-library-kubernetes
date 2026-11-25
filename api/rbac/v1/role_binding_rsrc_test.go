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

func TestRoleBindingStableID(t *testing.T) {
	reg := Library().Resources["role-binding"]
	id, err := reg.StableID(nil, map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
	_, err = reg.StableID(nil, map[string]any{})
	require.Error(t, err)
}

func bindingReasons(
	t *testing.T, before, after svc.RoleBindingResource,
	saved, observed *svc.RoleBindingResourceOutput, current *config.Configuration,
) []string {
	t.Helper()
	reg := Library().Resources["role-binding"]
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

func TestRoleBindingReplacementAndEquality(t *testing.T) {
	before := svc.RoleBindingResource{
		Metadata: svc.RoleMetadata{Name: new("system:reader")},
		RoleRef: svc.RoleBindingRoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader",
		},
		Subject: []svc.RoleBindingSubject{{Kind: "User", Name: "alice", APIGroup: new("known")}},
	}
	after := before
	after.Metadata.Name, after.Metadata.Namespace = nil, new("default")
	after.Metadata.Annotations, after.Metadata.Labels = &map[string]string{}, &map[string]string{}
	after.Subject = []svc.RoleBindingSubject{{Kind: "User", Name: "alice", Namespace: new("default")}}
	reg := Library().Resources["role-binding"]
	body, err := ub.Marshal(before)
	require.NoError(t, err)
	var encoded map[string]any
	require.NoError(t, ub.Unmarshal(body, &encoded))
	equal, err := reg.InputsEqual(&after, encoded)
	require.NoError(t, err)
	require.True(t, equal)
	omitted := before
	omitted.Subject = []svc.RoleBindingSubject{{Kind: "User", Name: "alice"}}
	explicitEmpty := omitted
	explicitEmpty.Subject = []svc.RoleBindingSubject{{Kind: "User", Name: "alice", APIGroup: new("")}}
	body, err = ub.Marshal(omitted)
	require.NoError(t, err)
	encoded = nil
	require.NoError(t, ub.Unmarshal(body, &encoded))
	equal, err = reg.InputsEqual(&explicitEmpty, encoded)
	require.NoError(t, err)
	require.False(t, equal, "a new explicit empty group must replace a computed value")
	saved := &svc.RoleBindingResourceOutput{RoleRef: before.RoleRef}
	require.Empty(t, bindingReasons(t, before, after, saved, saved, &config.Configuration{}))
	for _, field := range []string{
		"name", "namespace", "generate-name", "api-group", "kind", "ref-name",
		"server", "kubeconfig", "current-context",
	} {
		t.Run(field, func(t *testing.T) {
			changed := before
			current := &config.Configuration{}
			want := "metadata"
			switch field {
			case "name":
				changed.Metadata.Name = new("other")
			case "namespace":
				changed.Metadata.Namespace = new("team")
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
			require.Equal(t, []string{want}, bindingReasons(t, before, changed, saved, saved, current))
		})
	}
	observed := *saved
	observed.RoleRef.Name = "drift"
	require.Equal(t, []string{"role-ref"}, bindingReasons(t, before, before, saved, &observed,
		&config.Configuration{}))
	require.Empty(t, bindingReasons(t, before, before, saved, saved,
		&config.Configuration{Token: new("new"), Namespace: new("configured")}))
}

func TestRoleBindingRestoredNameReplacement(t *testing.T) {
	before := svc.RoleBindingResource{
		Metadata: svc.RoleMetadata{},
		RoleRef: svc.RoleBindingRoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader",
		},
		Subject: []svc.RoleBindingSubject{{Kind: "User", Name: "alice"}},
	}
	after := before
	after.Metadata.Name = new("system:reader")
	saved := &svc.RoleBindingResourceOutput{Name: "system:reader", RoleRef: before.RoleRef}
	require.Empty(t, bindingReasons(t, before, after, saved, saved, &config.Configuration{}))
	observed := *saved
	observed.RoleRef.Name = "drift"
	require.Equal(t, []string{"role-ref"}, bindingReasons(t, before, after, saved, &observed,
		&config.Configuration{}))
	require.Equal(t, []string{"server"}, bindingReasons(t, before, after, saved, saved,
		&config.Configuration{Server: new("https://other")}))
	after.RoleRef.Name = "changed"
	require.Equal(t, []string{"role-ref"}, bindingReasons(t, before, after, saved, saved,
		&config.Configuration{}))
	after.RoleRef = before.RoleRef
	after.Metadata.Name = new("system:renamed")
	require.Equal(t, []string{"metadata"}, bindingReasons(t, before, after, saved, saved,
		&config.Configuration{}))
}
