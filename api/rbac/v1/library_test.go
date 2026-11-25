package v1

import (
	"reflect"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestRoleStableID(t *testing.T) {
	reg := Library().Resources["role"]
	id, err := reg.StableID(map[string]any{},
		map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
}

func replacementReasons(
	t *testing.T, desired, before, outputs, configuration map[string]any,
	current *config.Configuration,
) []string {
	t.Helper()
	reg := Library().Resources["role"]
	if current == nil {
		current = &config.Configuration{}
	}
	if configuration == nil {
		configuration = map[string]any{}
	}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, desired))
	method := reflect.ValueOf(reg).MethodByName("ReplacementReasons")
	prior := reflect.New(method.Type().In(2)).Elem()
	for key, value := range map[string]map[string]any{
		"Inputs": before, "Outputs": outputs, "Observed": outputs, "Configuration": configuration,
	} {
		prior.FieldByName(key).Set(reflect.ValueOf(value))
	}
	result := method.Call([]reflect.Value{
		reflect.ValueOf(receiver), reflect.ValueOf(current), prior,
	})
	require.True(t, result[1].IsNil(), "%v", result[1])
	return result[0].Interface().([]string)
}

func TestRoleNormalizedPlanning(t *testing.T) {
	reg := Library().Resources["role"]
	before := map[string]any{"metadata": map[string]any{"generate-name": "system:reader-"},
		"rule": []any{map[string]any{"api-groups": []any{""}, "resources": []any{"pods"},
			"verbs": []any{"list", "get", "get"}}}}
	desired := map[string]any{"metadata": map[string]any{"generate-name": "system:reader-",
		"namespace": "default", "annotations": map[string]any{}, "labels": map[string]any{}},
		"rule": []any{map[string]any{"api-groups": []any{""}, "resources": []any{"pods"},
			"verbs": []any{"get", "list"}, "resource-names": []any{}}},
		"ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, desired))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	outputs := map[string]any{"server": "https://saved", "name": "system:reader-actual",
		"namespace": "default", "uid": "uid"}
	require.Empty(t, replacementReasons(t, desired, before, outputs, nil, nil))
	for _, field := range []string{"name", "namespace", "generate-name"} {
		changed := map[string]any{"metadata": map[string]any{field: "changed"}}
		require.Equal(t, []string{"metadata"},
			replacementReasons(t, changed, before, outputs, nil, nil))
	}
	restored := map[string]any{"metadata": map[string]any{"name": "system:reader-actual"}}
	omitted := map[string]any{"metadata": map[string]any{}}
	require.Empty(t, replacementReasons(t, restored, omitted, outputs, nil, nil))
	require.NoError(t, runtime.Decode(receiver, restored))
	equal, err = reg.InputsEqual(receiver, omitted)
	require.NoError(t, err)
	require.False(t, equal)
	named := map[string]any{"metadata": map[string]any{"name": "system:reader"}}
	require.Empty(t, replacementReasons(t, map[string]any{"metadata": map[string]any{}},
		named, outputs, nil, nil))
	require.Equal(t, []string{"metadata"}, replacementReasons(t,
		map[string]any{"metadata": map[string]any{}},
		map[string]any{"metadata": map[string]any{"namespace": "team"}}, outputs, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
		require.Equal(t, []string{field}, replacementReasons(t, before, before, outputs,
			map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, before, before, outputs,
		map[string]any{"namespace": "team", "token": "old"},
		&config.Configuration{Namespace: new("other"), Token: new("new")}))
}
