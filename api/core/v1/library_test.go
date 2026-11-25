package v1

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestConfigMapStableID(t *testing.T) {
	reg := Library().Resources["config-map"]
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://cluster", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
}

func replacementReasons(
	t *testing.T, reg runtime.ResourceRegistration, desired map[string]any,
	before, outputs, observed, configuration map[string]any, current *config.Configuration,
) []string {
	t.Helper()
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
		"Inputs": before, "Outputs": outputs, "Observed": observed, "Configuration": configuration,
	} {
		prior.FieldByName(key).Set(reflect.ValueOf(value))
	}
	result := method.Call([]reflect.Value{reflect.ValueOf(receiver), reflect.ValueOf(current), prior})
	require.True(t, result[1].IsNil(), "%v", result[1])
	return result[0].Interface().([]string)
}

func TestConfigMapReplacementMatrix(t *testing.T) {
	reg := Library().Resources["config-map"]
	for _, immutable := range []bool{false, true} {
		for _, change := range []string{"name", "namespace", "generate-name", "data", "binary-data",
			"immutable", "omit-immutable", "annotations", "labels", "metadata-and-content"} {
			t.Run(map[bool]string{false: "mutable", true: "immutable"}[immutable]+"/"+change,
				func(t *testing.T) {
					before := map[string]any{"object": map[string]any{
						"metadata": map[string]any{}, "immutable": immutable,
					}}
					object := map[string]any{"metadata": map[string]any{}, "immutable": immutable}
					desired := map[string]any{"object": object}
					metadata := object["metadata"].(map[string]any)
					want := false
					switch change {
					case "name", "namespace", "generate-name":
						metadata[change] = "changed"
						want = true
					case "annotations", "labels":
						metadata[change] = map[string]any{"key": "value"}
					case "data", "binary-data":
						object[change] = map[string]any{"key": "YQ=="}
						want = immutable
					case "immutable":
						object[change] = !immutable
						want = immutable
					case "omit-immutable":
						delete(object, "immutable")
						want = immutable
					case "metadata-and-content":
						metadata["labels"] = map[string]any{"key": "value"}
						object["data"] = map[string]any{"key": "new"}
						object["immutable"] = true
						want = immutable
					}
					reasons := replacementReasons(t, reg, desired, before, nil, nil, nil, nil)
					if want {
						require.Equal(t, []string{"object"}, reasons)
					} else {
						require.Empty(t, reasons)
					}
				})
		}
	}
}

func TestConfigMapNormalizedPlanning(t *testing.T) {
	reg := Library().Resources["config-map"]
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{}}}
	desired := map[string]any{"object": map[string]any{
		"metadata": map[string]any{
			"namespace": "default", "annotations": map[string]any{}, "labels": map[string]any{},
		},
		"data": map[string]any{}, "binary-data": map[string]any{}, "immutable": false,
	}, "ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, desired))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	require.Empty(t, replacementReasons(t, reg, desired, before, nil, nil, nil, nil))
	generated := map[string]any{"object": map[string]any{
		"metadata": map[string]any{"generate-name": "prefix-"},
	}}
	outputs := map[string]any{"server": "https://saved", "object": map[string]any{
		"metadata":  map[string]any{"name": "prefix-actual", "uid": "uid", "namespace": "default"},
		"immutable": false,
	}}
	require.Empty(t, replacementReasons(t, reg, generated, generated, outputs, outputs, nil, nil))
	observed := map[string]any{"server": "https://saved", "object": map[string]any{
		"metadata":  map[string]any{"name": "prefix-actual", "uid": "uid", "namespace": "default"},
		"immutable": true,
	}}
	require.Equal(t, []string{"object.immutable"},
		replacementReasons(t, reg, generated, generated, outputs, observed, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, nil, nil,
			map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, reg, before, before, nil, nil,
		map[string]any{"namespace": "old"}, &config.Configuration{Namespace: new("new")}))
}

func unobinSource(t *testing.T) string {
	t.Helper()
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(module))
}

func coreCompileDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	return work
}
