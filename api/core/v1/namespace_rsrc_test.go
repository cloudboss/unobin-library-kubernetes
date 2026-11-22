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

func TestNamespaceSchemaAndRegistration(t *testing.T) {
	lib := Library()
	reg := lib.Resources["namespace"]
	require.Equal(t, "kubernetes-core-v1", lib.Name)
	require.Equal(t, reflect.TypeFor[*svc.NamespaceResourceOutput](), reg.OutputType())
	require.IsType(t, &svc.NamespaceResource{}, reg.NewReceiver())
	require.Equal(t, 1, reg.SchemaVersion())
	require.True(t, reg.HasStableID())
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://cluster", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
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
	resource := schema.Resources["namespace"]
	require.ElementsMatch(t, []string{
		"object", "wait-for-default-service-account", "delete-timeout",
		"ignore-annotations", "ignore-labels",
	}, mapKeys(resource.Inputs))
	require.Equal(t, typecheck.TOptional(typecheck.TBoolean()),
		resource.Inputs["wait-for-default-service-account"])
	require.Equal(t, typecheck.TOptional(typecheck.TString()), resource.Inputs["delete-timeout"])
	require.Equal(t, typecheck.TOptional(typecheck.TList(typecheck.TString())),
		resource.Inputs["ignore-annotations"])
	require.Equal(t, resource.Inputs["ignore-annotations"], resource.Inputs["ignore-labels"])
	object := resource.Inputs["object"]
	require.Equal(t, typecheck.Object, object.Kind)
	require.ElementsMatch(t, []string{"metadata"}, mapKeys(objectFields(object)))
	metadata := objectFields(object)["metadata"]
	require.Equal(t, typecheck.Object, metadata.Kind)
	require.Equal(t, map[string]typecheck.Type{
		"name":          typecheck.TOptional(typecheck.TString()),
		"generate-name": typecheck.TOptional(typecheck.TString()),
		"annotations":   typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		"labels":        typecheck.TOptional(typecheck.TMap(typecheck.TString())),
	}, objectFields(metadata))
	require.ElementsMatch(t, []string{"server", "api-version", "kind", "object"},
		mapKeys(resource.Outputs))
	require.ElementsMatch(t, []string{"metadata"},
		mapKeys(objectFields(resource.Outputs["object"])))
	outputs := objectFields(objectFields(resource.Outputs["object"])["metadata"])
	require.Equal(t, map[string]typecheck.Type{
		"name": typecheck.TString(), "generate-name": typecheck.TString(), "uid": typecheck.TString(),
		"annotations": typecheck.TMap(typecheck.TString()), "labels": typecheck.TMap(typecheck.TString()),
	}, outputs)
	require.Empty(t, resource.SensitiveInputs)
	require.Empty(t, resource.SensitiveOutputs)
}

func TestNamespacePlanning(t *testing.T) {
	reg := Library().Resources["namespace"]
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{}}}
	normalized := map[string]any{"object": map[string]any{"metadata": map[string]any{
		"annotations": map[string]any{}, "labels": map[string]any{},
	}}, "wait-for-default-service-account": false, "delete-timeout": "5m",
		"ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, normalized))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	for _, change := range []string{
		"name", "generate-name", "annotations", "labels",
		"wait-for-default-service-account", "delete-timeout",
	} {
		metadata := map[string]any{}
		desired := map[string]any{"object": map[string]any{"metadata": metadata}}
		switch change {
		case "name", "generate-name":
			metadata[change] = "changed"
		case "annotations", "labels":
			metadata[change] = map[string]any{"key": "value"}
		case "wait-for-default-service-account":
			desired[change] = true
		case "delete-timeout":
			desired[change] = "1s"
		}
		reasons := replacementReasons(t, reg, desired, before, nil, nil, nil, nil)
		if change == "name" || change == "generate-name" {
			require.Equal(t, []string{"object"}, reasons)
		} else {
			require.Empty(t, reasons)
		}
	}
	generated := map[string]any{"object": map[string]any{
		"metadata": map[string]any{"generate-name": "prefix-"},
	}}
	outputs := map[string]any{"server": "https://saved", "api-version": "v1", "kind": "Namespace",
		"object": map[string]any{"metadata": map[string]any{"name": "prefix-actual", "uid": "uid"}}}
	require.Empty(t, replacementReasons(t, reg, generated, generated, outputs, outputs, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, nil, nil,
			map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, reg, before, before, nil, nil,
		map[string]any{"namespace": "old"}, &config.Configuration{Namespace: new("new")}))
}

func TestCompiledNamespaceFixtures(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-namespace")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, name := range []string{
		"valid", "missing-metadata", "list-metadata", "namespace", "spec", "status",
		"metadata-finalizers", "spec-finalizers", "null-annotation", "null-label", "adoption",
	} {
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			fixture, err := filepath.Abs(filepath.Join("testdata/ub/namespace", name))
			require.NoError(t, err)
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
				StackName: "namespace-test", Version: "v0.0.0",
				CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
				GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: name == "valid", Stdout: &log, Stderr: &log,
			})
			if name != "valid" {
				require.Error(t, err)
				field := map[string]string{
					"missing-metadata": "metadata", "list-metadata": "metadata",
					"namespace": "namespace", "spec": "spec", "status": "status",
					"metadata-finalizers": "finalizers", "spec-finalizers": "spec",
					"null-annotation": "string", "null-label": "string", "adoption": "adopt",
				}[name]
				require.Contains(t, err.Error(), field)
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
