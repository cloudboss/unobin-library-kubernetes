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

func TestClusterRoleStableID(t *testing.T) {
	reg := Library().Resources["cluster-role"]
	id, err := reg.StableID(nil, map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
	_, err = reg.StableID(nil, map[string]any{})
	require.Error(t, err)
}

func clusterRoleReasons(
	t *testing.T, before, after svc.ClusterRoleResource,
	saved, observed *svc.ClusterRoleResourceOutput, current *config.Configuration,
) []string {
	t.Helper()
	reg := Library().Resources["cluster-role"]
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

func TestClusterRoleReplacementSelectors(t *testing.T) {
	before := svc.ClusterRoleResource{Metadata: svc.ClusterRoleMetadata{Name: new("system:reader")}}
	saved := &svc.ClusterRoleResourceOutput{Name: "system:reader"}
	for _, field := range []string{"name", "generate-name", "server", "kubeconfig",
		"current-context"} {
		t.Run(field, func(t *testing.T) {
			after := before
			current := &config.Configuration{}
			want := "metadata"
			switch field {
			case "name":
				after.Metadata.Name = new("other")
			case "generate-name":
				after.Metadata.Name = nil
				after.Metadata.GenerateName = new("other-")
			default:
				require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
				want = field
			}
			require.Equal(t, []string{want}, clusterRoleReasons(t, before, after, saved, saved, current))
		})
	}
	require.Empty(t, clusterRoleReasons(t, before, before, saved, saved,
		&config.Configuration{Namespace: new("irrelevant"), Token: new("current")}))
	after := before
	after.Metadata.Name = nil
	require.Empty(t, clusterRoleReasons(t, after, before, saved, saved, &config.Configuration{}))
}
