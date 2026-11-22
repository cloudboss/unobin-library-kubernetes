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

func TestServiceAccountSchemaAndRegistration(t *testing.T) {
	lib := Library()
	reg := lib.Resources["service-account"]
	require.Equal(t, reflect.TypeFor[*svc.ServiceAccountResourceOutput](), reg.OutputType())
	require.IsType(t, &svc.ServiceAccountResource{}, reg.NewReceiver())
	require.Equal(t, 1, reg.SchemaVersion())
	require.NotContains(t, lib.Actions, "service-account")
	require.NotContains(t, lib.DataSources, "service-account")
	require.True(t, reg.HasStableID())
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://cluster", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
	_, err = reg.StableID(map[string]any{}, map[string]any{})
	require.Error(t, err)
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	resource := schema.Resources["service-account"]
	require.ElementsMatch(t, []string{
		"object", "create-timeout", "ignore-annotations", "ignore-labels",
	}, mapKeys(resource.Inputs))
	require.Equal(t, typecheck.TOptional(typecheck.TString()), resource.Inputs["create-timeout"])
	require.Equal(t, typecheck.TOptional(typecheck.TList(typecheck.TString())),
		resource.Inputs["ignore-annotations"])
	require.Equal(t, resource.Inputs["ignore-annotations"], resource.Inputs["ignore-labels"])
	object := resource.Inputs["object"]
	require.Equal(t, typecheck.Object, object.Kind)
	fields := objectFields(object)
	require.ElementsMatch(t, []string{
		"metadata", "image-pull-secrets", "secrets", "automount-service-account-token",
	}, mapKeys(fields))
	require.Equal(t,
		typecheck.TOptional(typecheck.TBoolean()),
		fields["automount-service-account-token"])
	require.Equal(t, map[string]typecheck.Type{
		"name":          typecheck.TOptional(typecheck.TString()),
		"generate-name": typecheck.TOptional(typecheck.TString()),
		"namespace":     typecheck.TOptional(typecheck.TString()),
		"annotations":   typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		"labels":        typecheck.TOptional(typecheck.TMap(typecheck.TString())),
	}, objectFields(fields["metadata"]))
	for _, name := range []string{"image-pull-secrets", "secrets"} {
		require.Equal(t, typecheck.Optional, fields[name].Kind)
		require.Equal(t, typecheck.List, fields[name].Elem.Kind)
		require.Equal(t, map[string]typecheck.Type{"name": typecheck.TOptional(typecheck.TString())},
			objectFields(*fields[name].Elem.Elem))
	}
	require.ElementsMatch(t,
		[]string{"server",
			"api-version",
			"kind",
			"default-secret-name",
			"object"},
		mapKeys(resource.Outputs))
	metadata := objectFields(objectFields(resource.Outputs["object"])["metadata"])
	require.ElementsMatch(t, []string{
		"name", "generate-name", "namespace", "uid", "annotations", "labels",
	}, mapKeys(metadata))
	require.Empty(t, resource.SensitiveInputs)
	require.Empty(t, resource.SensitiveOutputs)
}

func TestServiceAccountPlanning(t *testing.T) {
	reg := Library().Resources["service-account"]
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{}}}
	normalized := map[string]any{"object": map[string]any{
		"metadata": map[string]any{"namespace": "default", "annotations": map[string]any{},
			"labels": map[string]any{}}, "automount-service-account-token": true,
		"secrets": []any{}, "image-pull-secrets": []any{},
	}, "create-timeout": "30000ms", "ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, normalized))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	for _, field := range []string{"secrets", "image-pull-secrets"} {
		input := map[string]any{"object": map[string]any{"metadata": map[string]any{},
			field: []any{map[string]any{"name": "z"}, map[string]any{}, map[string]any{"name": "a"}}}}
		receiver := reg.NewReceiver()
		require.NoError(t, runtime.Decode(receiver, input))
		prior := map[string]any{"object": map[string]any{"metadata": map[string]any{},
			field: []any{map[string]any{"name": "a"}, map[string]any{"name": "z"},
				map[string]any{"name": "a"}, map[string]any{"name": ""}}}}
		equal, err := reg.InputsEqual(receiver, prior)
		require.NoError(t, err)
		require.True(t, equal)
	}
	for _, field := range []string{"name", "generate-name", "namespace"} {
		desired := map[string]any{"object": map[string]any{"metadata": map[string]any{field: "changed"}}}
		require.Equal(t, []string{"object"}, replacementReasons(t, reg, desired, before,
			nil, nil, nil, nil))
	}
	require.Empty(t, replacementReasons(t, reg, normalized, before, nil, nil, nil, nil))
	generated := map[string]any{"object": map[string]any{
		"metadata": map[string]any{"generate-name": "prefix-"},
	}}
	outputs := map[string]any{"server": "https://saved", "api-version": "v1", "kind": "ServiceAccount",
		"object": map[string]any{"metadata": map[string]any{"name": "prefix-actual", "uid": "uid"}}}
	require.Empty(t, replacementReasons(t, reg, generated, generated, outputs, outputs, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "new"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, nil, nil,
			map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, reg, before, before, nil, nil,
		map[string]any{"token": "old", "namespace": "old"},
		&config.Configuration{Token: new("new"), Namespace: new("new")}))
}

func TestCompiledServiceAccountFixtures(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-service-account")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, name := range []string{
		"valid", "missing-metadata", "list-metadata", "unknown", "camel-case", "snake-case",
		"reference-list", "metadata-uid", "default-secret-name", "status", "scenario",
	} {
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			fixture := filepath.Join(root, "api/core/v1/testdata/ub/service-account", name)
			if name == "scenario" {
				fixture = filepath.Join(root, "tests/integration/scenarios/core-v1-service-account")
			}
			build := name == "valid" || name == "scenario"
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
				StackName: "service-account-test", Version: "v0.0.0",
				CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
				GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: build, Stdout: &log, Stderr: &log,
			})
			if !build {
				require.Error(t, err)
				field := map[string]string{
					"missing-metadata": "metadata", "list-metadata": "metadata", "unknown": "unknown",
					"camel-case": "imagePullSecrets", "snake-case": "no match",
					"reference-list": "type mismatch", "metadata-uid": "uid",
					"default-secret-name": "default-secret-name", "status": "status",
				}[name]
				require.Contains(t, err.Error(), field)
				return
			}
			require.NoError(t, err, "%s", log.String())
			require.True(t, result.Built)
			configs := []string{"config.ub"}
			if name == "scenario" {
				configs = append(configs, "config-update.ub")
			}
			for _, file := range configs {
				body, err := os.ReadFile(filepath.Join(fixture, file))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(work, file), body, 0o600))
				for _, action := range []string{"pin", "validate"} {
					cmd := exec.CommandContext(t.Context(), result.BinaryPath, action, "-c", file)
					cmd.Dir = work
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
				}
			}
		})
	}
}
