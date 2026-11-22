package v1

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestServiceAccountStableID(t *testing.T) {
	reg := Library().Resources["service-account"]
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://cluster", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
	_, err = reg.StableID(map[string]any{}, map[string]any{})
	require.Error(t, err)
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
