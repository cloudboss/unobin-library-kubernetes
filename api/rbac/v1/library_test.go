package v1

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
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

func TestCompiledRoleFixtures(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/rbac-v1-role")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, name := range []string{"valid", "missing-metadata", "list-metadata", "missing-rule",
		"missing-members", "missing-api-groups", "missing-resources", "missing-verbs",
		"null-member", "null-map", "unsupported-rule", "server-metadata"} {
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			fixture, err := filepath.Abs(filepath.Join("testdata/ub", name))
			require.NoError(t, err)
			var log bytes.Buffer
			sdk, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
				"github.com/cloudboss/unobin").Output()
			require.NoError(t, err)
			result, err := compile.RunResult(compile.Options{
				ReplaceUnobin: strings.TrimSpace(string(sdk)),
				FactoryPath:   filepath.Join(fixture, "factory.ub"), OutDir: work,
				StackName: "role-test", Version: "v0.0.0", CLIVersion: "dev",
				GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: name == "valid", Stdout: &log, Stderr: &log,
			})
			if name != "valid" {
				require.Error(t, err, "%s", log.String())
				field := map[string]string{
					"missing-metadata": "metadata", "list-metadata": "metadata",
					"missing-rule": "rule", "missing-members": "api-groups",
					"missing-api-groups": "api-groups", "missing-resources": "resources",
					"missing-verbs":    "verbs",
					"null-member":      "expected string, got null",
					"null-map":         "expected string, got null",
					"unsupported-rule": "non-resource-urls", "server-metadata": "uid",
				}[name]
				require.Contains(t, err.Error(), field)
				return
			}
			require.NoError(t, err, "%s", log.String())
			require.True(t, result.Built)
			body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
			for _, args := range [][]string{
				{"pin", "-c", "config.ub"}, {"validate", "-c", "config.ub"},
			} {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
				cmd.Dir = work
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
		})
	}
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
