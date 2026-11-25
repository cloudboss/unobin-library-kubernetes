package compile_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestCompiledPersistentVolumeLiterals(t *testing.T) {
	cases, err := os.ReadDir("testdata/ub/persistent-volume")
	require.NoError(t, err)
	for _, entry := range cases {
		if entry.Name() == "configured" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			_, _, logs, err := compilePersistentVolumeFixture(t,
				filepath.Join("testdata/ub/persistent-volume", entry.Name(), "factory.ub"), false)
			switch entry.Name() {
			case "valid", "managed-valid", "unrelated-valid":
				require.NoError(t, err, "%s", logs)
			case "blob-managed":
				require.ErrorContains(t, err, "resource.volume")
				require.ErrorContains(t, err, "constraints[")
				require.ErrorContains(t, err, "azure-disk.kind cannot be Managed for a blob URI")
			case "missing-capacity":
				require.ErrorContains(t, err, "type:", "%s", logs)
				require.ErrorContains(t, err, `missing required field "capacity"`)
			case "missing-csi-driver":
				require.ErrorContains(t, err, "type:", "%s", logs)
				require.ErrorContains(t, err, `missing required field "driver"`)
			default:
				require.ErrorContains(t, err, "constraints[", "%s", logs)
			}
		})
	}
}

func TestCompiledPersistentVolumeEvaluatedConstraint(t *testing.T) {
	fixture := "testdata/ub/persistent-volume/configured"
	result, dir, logs, err := compilePersistentVolumeFixture(t,
		filepath.Join(fixture, "factory.ub"), true)
	require.NoError(t, err, "%s", logs)
	for _, tc := range []struct {
		name, diagnostic string
	}{
		{name: "managed"},
		{name: "blob"},
		{name: "null-kind", diagnostic: "azure-disk.kind must be Managed for a managed disk URI"},
		{name: "blob-managed", diagnostic: "azure-disk.kind cannot be Managed for a blob URI"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := "config-" + tc.name + ".ub"
			body, err := os.ReadFile(filepath.Join(fixture, file))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), body, 0600))
			call := func(args ...string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
				cmd.Dir = dir
				return cmd.CombinedOutput()
			}
			out, err := call("pin", "-c", file)
			require.NoError(t, err, "%s", out)
			out, err = call("plan", "-c", file, "-o", tc.name+".ubp")
			if tc.diagnostic == "" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err)
				message := string(out)
				require.Contains(t, message, "resource.volume")
				require.Contains(t, message, "constraints[")
				require.Contains(t, message, "object.spec.persistent-volume-source."+tc.diagnostic)
			}
		})
	}
}

func compilePersistentVolumeFixture(t *testing.T, path string, build bool,
) (*compile.Result, string, string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-persistent-volume")
	require.NoError(t, os.MkdirAll(base, 0700))
	dir, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	fixture, err := filepath.Abs(path)
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: fixture, OutDir: dir, StackName: "volume", Version: "v0",
		CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            build, Stdout: &log, Stderr: &log,
	})
	return result, dir, strings.TrimSpace(log.String()), err
}

func TestCompiledPersistentVolumeScenario(t *testing.T) {
	scenario := "testdata/ub/compiled/persistent-volume"
	result, dir, logs, err := compilePersistentVolumeFixture(t,
		filepath.Join(scenario, "factory.ub"), true)
	require.NoError(t, err, "%s", logs)
	for _, name := range []string{"config.ub", "config-update.ub"} {
		body, err := os.ReadFile(filepath.Join(scenario, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0600))
		for _, args := range [][]string{{"pin", "-c", name}, {"plan", "-c", name, "-o", name + ".ubp"}} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
		}
	}
}

func TestCompiledPersistentVolumeRequiredInputs(t *testing.T) {
	fixture := "testdata/ub/persistent-volume/configured"
	result, dir, logs, err := compilePersistentVolumeFixture(t,
		filepath.Join(fixture, "factory.ub"), true)
	require.NoError(t, err, "%s", logs)
	requests := make(chan string, 16)
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- r.Method + " " + r.URL.Path:
		default:
		}
		http.Error(w, "unexpected Kubernetes request", http.StatusInternalServerError)
	}))
	defer api.Close()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "absent"))
	call := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}
	for _, tc := range []struct {
		name, diagnostic string
	}{
		{"missing-capacity", `field "object.spec.capacity": required but not provided`},
		{"null-capacity", `field "object.spec.capacity": required but is null`},
		{"missing-csi-driver", `field "object.spec.persistent-volume-source.csi.driver": ` +
			"required but not provided"},
		{"empty-capacity", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := "config-" + tc.name + ".ub"
			body, err := os.ReadFile(filepath.Join(fixture, file))
			require.NoError(t, err)
			body = bytes.ReplaceAll(body, []byte("https://api-placeholder.invalid"), []byte(api.URL))
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), body, 0600))
			out, err := call("pin", "-c", file)
			require.NoError(t, err, "%s", out)
			out, err = call("plan", "-c", file, "-o", tc.name+".ubp")
			if tc.diagnostic == "" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err, "%s", out)
				require.Contains(t, string(out), "resource.volume")
				require.Contains(t, string(out), tc.diagnostic)
			}
			select {
			case request := <-requests:
				t.Fatalf("unexpected Kubernetes request: %s", request)
			default:
			}
		})
	}
}
