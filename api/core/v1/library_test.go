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
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func TestConfigMapRegistration(t *testing.T) {
	lib := Library()
	require.Equal(t, "kubernetes-core-v1", lib.Name)
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	reg := lib.Resources["config-map"]
	require.Equal(t, reflect.TypeFor[*svc.ConfigMapResourceOutput](), reg.OutputType())
	require.IsType(t, &svc.ConfigMapResource{}, reg.NewReceiver())
	require.Equal(t, 1, reg.SchemaVersion())
	require.True(t, reg.HasStableID())
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://cluster", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
}

func TestConfigMapSchema(t *testing.T) {
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
	resource := schema.Resources["config-map"]
	require.ElementsMatch(t, []string{"object", "ignore-annotations", "ignore-labels"},
		mapKeys(resource.Inputs))
	require.Equal(t, typecheck.TOptional(typecheck.TList(typecheck.TString())),
		resource.Inputs["ignore-annotations"])
	require.Equal(t, resource.Inputs["ignore-annotations"], resource.Inputs["ignore-labels"])
	object := resource.Inputs["object"]
	require.Equal(t, typecheck.Object, object.Kind)
	fields := objectFields(object)
	require.ElementsMatch(t, []string{"metadata", "data", "binary-data", "immutable"}, mapKeys(fields))
	require.Equal(t, typecheck.TOptional(typecheck.TMap(typecheck.TString())), fields["data"])
	require.Equal(t, fields["data"], fields["binary-data"])
	require.Equal(t, typecheck.TOptional(typecheck.TBoolean()), fields["immutable"])
	metadata := fields["metadata"]
	require.Equal(t, typecheck.Object, metadata.Kind)
	require.Equal(t, map[string]typecheck.Type{
		"name":          typecheck.TOptional(typecheck.TString()),
		"generate-name": typecheck.TOptional(typecheck.TString()),
		"namespace":     typecheck.TOptional(typecheck.TString()),
		"annotations":   typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		"labels":        typecheck.TOptional(typecheck.TMap(typecheck.TString())),
	}, objectFields(metadata))
	require.ElementsMatch(t, []string{"server", "api-version", "kind", "object"},
		mapKeys(resource.Outputs))
	require.Equal(t, typecheck.TString(), resource.Outputs["api-version"])
	output := objectFields(resource.Outputs["object"])
	require.Equal(t, typecheck.TMap(typecheck.TString()), output["data"])
	require.Equal(t, output["data"], output["binary-data"])
	require.Equal(t, typecheck.TBoolean(), output["immutable"])
	require.ElementsMatch(t, []string{
		"name", "generate-name", "namespace", "uid", "annotations", "labels",
	},
		mapKeys(objectFields(output["metadata"])))
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

func TestCompiledConfigMapFixtures(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-config-map")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, name := range []string{"valid", "missing-metadata", "list-metadata"} {
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			fixture, err := filepath.Abs(filepath.Join("testdata/ub", name))
			require.NoError(t, err)
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
				StackName: "config-map-test", Version: "v0.0.0",
				CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
				GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: name == "valid", Stdout: &log, Stderr: &log,
			})
			if name != "valid" {
				require.Error(t, err)
				require.Contains(t, err.Error(), "metadata")
				return
			}
			require.NoError(t, err, "%s", log.String())
			require.True(t, result.Built)
			body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
			for _, args := range [][]string{{"pin", "-c", "config.ub"}, {"validate", "-c", "config.ub"}} {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
				cmd.Dir = work
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
		})
	}
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
