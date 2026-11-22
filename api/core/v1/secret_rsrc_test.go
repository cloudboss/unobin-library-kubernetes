package v1

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestSecretStableID(t *testing.T) {
	reg := Library().Resources["secret"]
	id, err := reg.StableID(map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
		map[string]any{"server": "https://saved", "object": map[string]any{
			"metadata": map[string]any{"uid": "uid"},
		}})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
}

func TestSecretReplacementMatrix(t *testing.T) {
	reg := Library().Resources["secret"]
	for _, immutable := range []bool{false, true} {
		for _, field := range []string{"name", "generate-name", "namespace", "type", "data",
			"binary-data", "immutable", "annotations", "labels"} {
			t.Run(field+"/immutable="+stringBool(immutable), func(t *testing.T) {
				before := map[string]any{"object": map[string]any{
					"metadata": map[string]any{}, "immutable": immutable,
				}}
				object := map[string]any{"metadata": map[string]any{}, "immutable": immutable}
				desired := map[string]any{"object": object}
				metadata := object["metadata"].(map[string]any)
				want := false
				switch field {
				case "name", "generate-name", "namespace":
					metadata[field] = "changed"
					want = true
				case "type":
					object[field] = "custom"
					want = true
				case "data", "binary-data":
					object[field] = map[string]any{"key": "YQ=="}
					want = immutable
				case "immutable":
					object[field] = !immutable
					want = immutable
				case "annotations", "labels":
					metadata[field] = map[string]any{"key": "value"}
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
	before := map[string]any{"object": map[string]any{"metadata": map[string]any{}}}
	desired := map[string]any{"object": map[string]any{"metadata": map[string]any{
		"namespace": "default", "annotations": map[string]any{}, "labels": map[string]any{},
	}, "binary-data": map[string]any{}, "immutable": false, "type": "Opaque"},
		"wait-for-service-account-token": true, "create-timeout": "60s",
		"ignore-annotations": []any{}, "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, desired))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	desired["object"].(map[string]any)["data"] = map[string]any{}
	require.NoError(t, runtime.Decode(receiver, desired))
	equal, err = reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.False(t, equal)
	outputs := map[string]any{"server": "https://saved", "object": map[string]any{
		"metadata":  map[string]any{"name": "generated", "namespace": "default", "uid": "uid"},
		"immutable": false,
	}}
	observed := map[string]any{"server": "https://saved", "object": map[string]any{
		"metadata":  map[string]any{"name": "generated", "namespace": "default", "uid": "uid"},
		"immutable": true,
	}}
	require.Equal(t, []string{"object.immutable"},
		replacementReasons(t, reg, before, before, outputs, observed, nil, nil))
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "new"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before, nil, nil,
			map[string]any{field: "old"}, current))
	}
}

func stringBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func compileSecretFixture(t *testing.T, factory string, build bool) (*compile.Result, string) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-secret")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "fixture-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work, StackName: "secret-test", Version: "v0.0.0",
		CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
		GoVersion: "1.26.2", Build: build,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	return result, work
}

func TestCompiledSecretMaskingAndPersistence(t *testing.T) {
	factory, err := filepath.Abs("testdata/ub/secret/valid/factory.ub")
	require.NoError(t, err)
	result, work := compileSecretFixture(t, factory, true)
	var stored corev1.Secret
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "POST" {
			posts++
			require.NoError(t, json.NewDecoder(req.Body).Decode(&stored))
			stored.Name, stored.Namespace, stored.UID = "example", "default", "uid"
			stored.Data = map[string][]byte{}
			for key, value := range stored.StringData {
				stored.Data[key] = []byte(value)
			}
			stored.StringData = nil
		}
		require.NoError(t, json.NewEncoder(w).Encode(&stored))
	}))
	t.Cleanup(server.Close)
	body, err := os.ReadFile("testdata/ub/secret/valid/config.ub")
	require.NoError(t, err)
	body = bytes.ReplaceAll(body, []byte("SECRET_TEST_SERVER"), []byte(server.URL))
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err)
		require.NotContains(t, string(out), "secret-payload-test-value")
		return out
	}
	run("pin", "-c", "config.ub")
	plan := run("plan", "-c", "config.ub", "-o", "plan.ub")
	require.Contains(t, string(plan), "sensitive")
	run("apply", "plan.ub")
	output := run("output", "-c", "config.ub")
	require.Contains(t, string(output), "sensitive")
	require.Equal(t, 1, posts)
	found, derived := false, false
	require.NoError(t, filepath.WalkDir(filepath.Join(work, ".unobin/state"),
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".json.enc") {
				return nil
			}
			encoded, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var envelope state.Envelope
			require.NoError(t, json.Unmarshal(encoded, &envelope))
			var snapshot state.Snapshot
			require.NoError(t, json.Unmarshal(envelope.Ciphertext, &snapshot))
			resource := snapshot.Find("resource.credentials")
			if resource == nil {
				return nil
			}
			found = true
			require.Equal(t, []string{"object"}, resource.SensitiveInputs)
			require.Equal(t, []string{"object"}, resource.SensitiveOutputs)
			input := resource.Inputs["object"].(map[string]any)
			observed := resource.Outputs["object"].(map[string]any)
			require.True(t, input["data"].(map[string]any)["value"] == "secret-payload-test-value")
			require.True(t, observed["data"].(map[string]any)["value"] == "secret-payload-test-value")
			derived = derived || snapshot.Outputs["derived"] == "secret-payload-test-value"
			return nil
		}))
	require.True(t, found)
	require.True(t, derived)
}

func TestCompiledSecretInvalidFields(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	for _, name := range []string{
		"missing-metadata", "write-only", "revision", "file-reference", "conflict",
	} {
		t.Run(name, func(t *testing.T) {
			factory, err := filepath.Abs("testdata/ub/secret/" + name + "/factory.ub")
			require.NoError(t, err)
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{
				FactoryPath: factory, OutDir: coreCompileDirectory(t), StackName: "secret-test", Version: "v0",
				CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
				GoVersion:        "1.26.2",
				ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
				Stdout:           &log, Stderr: &log,
			})
			require.Error(t, err)
		})
	}
}

func TestSecretScenarioFactory(t *testing.T) {
	factory, err := filepath.Abs("../../../tests/integration/scenarios/core-v1-secret/factory.ub")
	require.NoError(t, err)
	compileSecretFixture(t, factory, true)
}
