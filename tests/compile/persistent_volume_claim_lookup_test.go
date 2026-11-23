package compile_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pvcInfoCompileOptions(t *testing.T, factory string) (compile.Options, *bytes.Buffer) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-persistent-volume-claim-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	factory, err = filepath.Abs(factory)
	require.NoError(t, err)
	log := new(bytes.Buffer)
	return compile.Options{
		FactoryPath: factory, OutDir: work, StackName: "pvc-info", Version: "v0.0.0",
		CLIVersion: "v0.13.0-a.11", GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Stdout: log, Stderr: log,
	}, log
}

func TestCompiledPVCInfoFixtures(t *testing.T) {
	structuralErrors := map[string][]string{
		"missing-metadata": {`missing required input "metadata"`},
		"list-metadata":    {"expected object", "got list"},
		"wrong-name":       {"expected optional(string), got integer"},
		"wrong-map-value":  {"expected string, got integer"},
		"null-map-value":   {"expected string, got null"},
		"generate-name":    {`unknown field "generate-name"`},
		"spec-input":       {`unknown field "spec"`},
		"resource-input":   {`unknown field "wait-until-bound"`},
		"unsupported-outputs": {
			`unknown field "status"`, `unknown field "id"`, `unknown field "data-source"`,
		},
	}
	fixtures, err := filepath.Glob("testdata/ub/persistent-volume-claim/data-source/*/factory.ub")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)
	for _, fixture := range fixtures {
		name := filepath.Base(filepath.Dir(fixture))
		t.Run(name, func(t *testing.T) {
			opts, log := pvcInfoCompileOptions(t, fixture)
			_, err := compile.RunResult(opts)
			switch name {
			case "valid", "omitted-name", "null-name":
				require.NoError(t, err, "%s", log.String())
			default:
				require.Error(t, err, "%s", log.String())
				if strings.HasPrefix(name, "invalid-") {
					require.Contains(t, err.Error(), "schema: data-source.claim: constraints[")
					return
				}
				diagnostics := structuralErrors[name]
				require.NotEmpty(t, diagnostics)
				for _, diagnostic := range diagnostics {
					require.Contains(t, err.Error(), diagnostic)
				}
			}
		})
	}
}

func TestCompiledPVCInfoAPIPreflight(t *testing.T) {
	opts, log := pvcInfoCompileOptions(t,
		"testdata/ub/persistent-volume-claim/data-source/invalid-partial-literal/factory.ub")
	opts.LibraryAPIDescriptor = &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	_, err := compile.RunResult(opts)
	require.Error(t, err, "%s", log.String())
	var apiErr *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &apiErr, "%s", log.String())
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, apiErr.Required)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 0}, apiErr.Implemented)
	require.NotContains(t, err.Error()+log.String(), "constraints[")
}

func TestCompiledPVCInfoInputs(t *testing.T) {
	fixture := "testdata/ub/persistent-volume-claim-inputs"
	opts, log := pvcInfoCompileOptions(t, filepath.Join(fixture, "factory.ub"))
	opts.Build = true
	result, err := compile.RunResult(opts)
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	var mu sync.Mutex
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		request := r.Method + " " + r.URL.RequestURI()
		var body any
		switch request {
		case "GET /api/v1/namespaces/default/persistentvolumeclaims/lookup":
			mu.Lock()
			calls = append(calls, request+" "+r.Header.Get("Authorization"))
			mu.Unlock()
			body = &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "lookup", Namespace: "default"},
			}
		case "POST /api/v1/namespaces/default/configmaps",
			"GET /api/v1/namespaces/default/configmaps/fixed":
			body = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "fixed", Namespace: "default", UID: "fixed-uid"},
			}
		default:
			t.Errorf("unexpected request %s", request)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	requests := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, calls...)
	}
	run := func(t *testing.T, args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = opts.OutDir
		return cmd.CombinedOutput()
	}
	update := func(t *testing.T, name string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(fixture, name+".ub"))
		require.NoError(t, err)
		body = bytes.ReplaceAll(body, []byte("PVC_INFO_SERVER"), []byte(server.URL))
		require.NoError(t, os.WriteFile(filepath.Join(opts.OutDir, "config.ub"), body, 0o600))
		out, err := run(t, "pin", "-c", "config.ub")
		require.NoError(t, err, "%s", out)
	}
	checkInvalid := func(t *testing.T) {
		t.Helper()
		for _, tc := range []struct{ name, rule string }{
			{"invalid-name", "constraints[0]"},
			{"invalid-label-key", "constraints[1]"},
			{"invalid-label-value", "constraints[2]"},
			{"invalid-annotation", "constraints[3]"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				update(t, tc.name)
				before := requests()
				out, err := run(t, "plan", "-c", "config.ub", "-o", "plan.ubp")
				require.Error(t, err, "%s", out)
				require.Contains(t, string(out), "data-source.claim: schema: "+tc.rule)
				require.Equal(t, before, requests())
			})
		}
	}
	t.Run("before apply", checkInvalid)

	update(t, "valid")
	const want = "GET /api/v1/namespaces/default/persistentvolumeclaims/lookup Bearer pvc-test-token"
	plan := func(anchor runtime.Decision) {
		t.Helper()
		out, err := run(t, "plan", "-c", "config.ub", "-o", "plan.ubp")
		require.NoError(t, err, "%s", out)
		require.NotContains(t, string(out), "pvc-test-token")
		body, err := os.ReadFile(filepath.Join(opts.OutDir, "plan.ubp"))
		require.NoError(t, err)
		saved, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
			return encrypters.Noop{}, nil
		})
		require.NoError(t, err)
		decisions := map[string]runtime.Decision{}
		for _, step := range saved.Steps {
			if step.Kind == runtime.NodeResource || step.Kind == runtime.NodeDataSource {
				decisions[step.Address] = step.Decision
			}
			if step.Address == "data-source.claim" {
				require.Empty(t, step.SensitiveInputs)
				require.Empty(t, step.SensitiveOutputs)
			}
		}
		require.Equal(t, map[string]runtime.Decision{
			"resource.anchor": anchor, "data-source.claim": runtime.DecisionRead,
		}, decisions)
	}
	plan(runtime.DecisionCreate)
	require.Equal(t, []string{want}, requests())
	out, err := run(t, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	require.NotContains(t, string(out), "pvc-test-token")
	require.Equal(t, []string{want, want}, requests())
	plan(runtime.DecisionNoOp)
	require.Equal(t, []string{want, want, want}, requests())
	t.Run("with unchanged resource", checkInvalid)
}
