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
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/typecheck"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
)

func TestRoleRegistration(t *testing.T) {
	lib := Library()
	require.Equal(t, "kubernetes-rbac-v1", lib.Name)
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	require.ElementsMatch(t, []string{"role", "role-binding"}, mapKeys(lib.Resources))
	require.Empty(t, lib.Actions)
	require.Empty(t, lib.DataSources)
	reg := lib.Resources["role"]
	require.IsType(t, &svc.RoleResource{}, reg.NewReceiver())
	require.Equal(t, reflect.TypeFor[*svc.RoleResourceOutput](), reg.OutputType())
	require.Equal(t, 1, reg.SchemaVersion())
	require.True(t, reg.HasStableID())
	id, err := reg.StableID(map[string]any{},
		map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
}

func TestRoleSchema(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	resource := schema.Resources["role"]
	require.ElementsMatch(t, []string{"metadata", "rule", "ignore-annotations", "ignore-labels"},
		mapKeys(resource.Inputs))
	require.Equal(t, typecheck.TOptional(typecheck.TList(typecheck.TString())),
		resource.Inputs["ignore-annotations"])
	require.Equal(t, resource.Inputs["ignore-annotations"], resource.Inputs["ignore-labels"])
	require.Equal(t, typecheck.Object, resource.Inputs["metadata"].Kind)
	metadata := map[string]typecheck.Type{
		"name":          typecheck.TOptional(typecheck.TString()),
		"namespace":     typecheck.TOptional(typecheck.TString()),
		"generate-name": typecheck.TOptional(typecheck.TString()),
		"labels":        typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		"annotations":   typecheck.TOptional(typecheck.TMap(typecheck.TString())),
	}
	require.Equal(t, metadata, objectFields(resource.Inputs["metadata"]))
	rules := resource.Inputs["rule"]
	require.Equal(t, typecheck.List, rules.Kind)
	require.Equal(t, map[string]typecheck.Type{
		"api-groups":     typecheck.TList(typecheck.TString()),
		"resources":      typecheck.TList(typecheck.TString()),
		"verbs":          typecheck.TList(typecheck.TString()),
		"resource-names": typecheck.TOptional(typecheck.TList(typecheck.TString())),
	}, objectFields(*rules.Elem))
	require.ElementsMatch(t, []string{"server", "api-version", "kind", "name", "namespace", "uid",
		"metadata", "rule"}, mapKeys(resource.Outputs))
	for _, name := range []string{"server", "api-version", "kind", "name", "namespace", "uid"} {
		require.Equal(t, typecheck.TString(), resource.Outputs[name])
	}
	for name := range metadata {
		metadata[name] = typecheck.TString()
	}
	metadata["labels"], metadata["annotations"] = typecheck.TMap(typecheck.TString()),
		typecheck.TMap(typecheck.TString())
	require.Equal(t, metadata, objectFields(resource.Outputs["metadata"]))
	require.Equal(t, map[string]typecheck.Type{
		"api-groups":     typecheck.TList(typecheck.TString()),
		"resources":      typecheck.TList(typecheck.TString()),
		"verbs":          typecheck.TList(typecheck.TString()),
		"resource-names": typecheck.TList(typecheck.TString()),
	}, objectFields(*resource.Outputs["rule"].Elem))
	require.Empty(t, resource.SensitiveInputs)
	require.Empty(t, resource.SensitiveOutputs)
}

func objectFields(object typecheck.Type) map[string]typecheck.Type {
	fields := map[string]typecheck.Type{}
	for _, field := range object.Fields {
		fields[field.Name] = field.Type
		if field.Optional {
			fields[field.Name] = typecheck.TOptional(field.Type)
		}
	}
	return fields
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
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
					"null-map":         "expected optional(map(string))",
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
