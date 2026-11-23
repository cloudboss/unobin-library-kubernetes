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
	"sync/atomic"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubejson "k8s.io/apimachinery/pkg/util/json"
)

const (
	objectInfoFixtures   = "testdata/ub/object-info"
	objectInfoAddress    = "data-source.endpoint"
	objectInfoConstraint = objectInfoAddress + ": schema: constraints["
	secretObjectPath     = "/api/v1/namespaces/default/secrets/example"
	widgetObjectPath     = "/apis/example.io/v2/namespaces/default/widgets/example"
	objectInfoSecret     = "cHJpdmF0ZS1wYXlsb2Fk"
)

func TestCompiledObjectInfoLiterals(t *testing.T) {
	for _, tc := range []struct {
		fixture, diagnostic string
	}{
		{"valid", ""},
		{"valid-generic", ""},
		{"missing-api-version", `missing required input "api-version"`},
		{"null-api-version", "expected string, got null"},
		{"missing-kind", `missing required input "kind"`},
		{"null-kind", "expected string, got null"},
		{"missing-metadata", `missing required input "metadata"`},
		{"null-metadata", "got null"},
		{"missing-name", `missing required field "name"`},
		{"null-name", "expected string, got null"},
		{"list-metadata", "got list(object"},
		{"invalid-empty-name", "constraints["},
		{"invalid-dot-name", "constraints["},
		{"invalid-dot-dot-name", "constraints["},
		{"invalid-slash-name", "constraints["},
		{"invalid-percent-name", "constraints["},
		{"invalid-version", "constraints["},
		{"object-input", `unknown field "object" on dynamic.object-info`},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			_, err := compileObjectInfo(t, filepath.Join("literals", tc.fixture), false)
			if tc.diagnostic == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.diagnostic)
			if tc.diagnostic == "constraints[" {
				require.Contains(t, err.Error(), objectInfoAddress)
			}
		})
	}
}

func TestCompiledObjectInfoRejectsOlderLibraryAPI(t *testing.T) {
	descriptor := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	_, err := compileObjectInfoForAPI(t, "literals/valid", false, descriptor)
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Hint, "The library recommends Unobin v0.13.0-a.10")
}

func TestCompiledObjectInfoObservedPlan(t *testing.T) {
	binary, err := compileObjectInfo(t, "known", true)
	require.NoError(t, err)
	for _, tc := range []struct {
		kind, apiVersion, path string
	}{
		{"Secret", "v1", secretObjectPath},
		{"Widget", "example.io/v2", widgetObjectPath},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var response atomic.Int32
			server, takeCalls := newObjectInfoHTTP(t, func(req *http.Request) (int, any) {
				switch response.Load() {
				case 1:
					return http.StatusOK, observedObjectInfo(tc.apiVersion, tc.kind, "Updated")
				case 2:
					err := apierrors.NewNotFound(schema.GroupResource{Resource: "objects"}, "example")
					return http.StatusNotFound, err.ErrStatus
				default:
					return http.StatusOK, observedObjectInfo(tc.apiVersion, tc.kind, "Ready")
				}
			})
			run := newObjectInfoRun(t, binary, "known", server, tc.apiVersion, tc.kind, "example")
			review := run.ok("plan", "-c", "config.ub", "-o", "read.ubp")
			require.NotContains(t, string(review), objectInfoSecret)
			want := observedObjectInfo(tc.apiVersion, tc.kind, "Ready")
			steps := run.plan("read.ubp").Steps
			require.Equal(t, objectInfoKnownSteps(), objectInfoStepAddresses(steps))
			first := steps[1]
			require.Equal(t, runtime.DecisionRead, first.Decision)
			require.Equal(t, map[string]any{"object": want}, first.ObservedOutputs)
			require.Equal(t, []string{"object"}, first.SensitiveOutputs)
			require.Equal(t, objectInfoReadCalls("GET "+tc.path), takeCalls())

			run.ok("apply", "read.ubp")
			require.Equal(t, objectInfoReadCalls("GET "+tc.path), takeCalls())
			snapshot := run.state()
			entry := snapshot.Find(objectInfoAddress)
			require.NotNil(t, entry)
			require.Equal(t, map[string]any{"object": want}, entry.Outputs)
			require.Equal(t, []string{"object"}, entry.SensitiveOutputs)
			output, ok := snapshot.Outputs["observed"].(string)
			require.True(t, ok)
			var document map[string]any
			require.NoError(t, kubejson.Unmarshal([]byte(output), &document))
			require.Equal(t, want, document)

			review = run.ok("plan", "-c", "config.ub", "-o", "repeat.ubp")
			require.Contains(t, string(review), "No changes.")
			repeat := run.plan("repeat.ubp")
			require.Equal(t, objectInfoKnownSteps(), objectInfoStepAddresses(repeat.Steps))
			require.Equal(t, runtime.DecisionRead, repeat.Steps[1].Decision)
			require.Equal(t, first.ObservedOutputs, repeat.Steps[1].ObservedOutputs)
			require.Equal(t, objectInfoReadCalls("GET "+tc.path), takeCalls())

			for _, tc := range []struct {
				name string
				mode int32
			}{
				{"changed status", 1}, {"disappearance", 2},
			} {
				t.Run(tc.name, func(t *testing.T) {
					response.Store(tc.mode)
					out := run.fail("apply", "repeat.ubp")
					require.Contains(t, out, objectInfoAddress+" changed since the plan")
					require.Contains(t, out, "plan again")
					require.Contains(t, out, "object: (sensitive)")
					require.NotContains(t, out, objectInfoSecret)
					require.NotContains(t, out, "currentPhase")
				})
			}
			require.Equal(t, append(objectInfoReadCalls("GET "+tc.path),
				objectInfoReadCalls("GET "+tc.path)...), takeCalls())

			run.ok("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
			require.True(t, run.plan("destroy.ubp").Destroy)
			run.ok("apply", "destroy.ubp")
			require.Nil(t, run.state().Find(objectInfoAddress))
			require.Empty(t, takeCalls())
		})
	}
}

func TestCompiledObjectInfoSensitivePlan(t *testing.T) {
	binary, err := compileObjectInfo(t, "sensitive-consumer", true)
	require.NoError(t, err)
	server, takeCalls := newObjectInfoHTTP(t, func(*http.Request) (int, any) {
		return http.StatusOK, observedObjectInfo("v1", "Secret", "Ready")
	})
	run := newObjectInfoRun(t, binary, "sensitive-consumer", server, "v1", "Secret", "example")
	out := run.ok("plan", "-c", "config.ub", "-o", "sensitive.ubp")
	require.Contains(t, string(out), "resource.consumer")
	require.Contains(t, string(out), "object: <sensitive>")
	require.NotContains(t, string(out), objectInfoSecret)
	require.NotContains(t, string(out), "currentPhase")
	require.Equal(t, objectInfoReadCalls("GET "+secretObjectPath), takeCalls())
}

func TestCompiledObjectInfoEvaluatedConstraints(t *testing.T) {
	binary, err := compileObjectInfo(t, "known", true)
	require.NoError(t, err)
	server, takeCalls := newObjectInfoHTTP(t, func(req *http.Request) (int, any) {
		return http.StatusOK, observedObjectInfo("v1", "Secret", "Ready")
	})
	run := newObjectInfoRun(t, binary, "known", server, "v1", "Secret", "example")
	run.ok("plan", "-c", "config.ub", "-o", "read.ubp")
	run.ok("apply", "read.ubp")
	require.Equal(t, append(objectInfoReadCalls("GET "+secretObjectPath),
		objectInfoReadCalls("GET "+secretObjectPath)...), takeCalls())

	for _, tc := range []struct {
		version, name, want string
	}{
		{"v1", "part/name", "constraints[1]"},
		{"example.io/v2/extra", "example", "constraints[0]"},
	} {
		run.configure("known", server, tc.version, "Secret", tc.name)
		out := run.fail("plan", "-c", "config.ub", "-o", "invalid.ubp")
		require.Contains(t, out, objectInfoConstraint)
		require.Contains(t, out, tc.want)
		require.Empty(t, takeCalls())
	}

	run.configure("known", server, "v1", "Secret", "example")
	run.ok("plan", "-c", "config.ub", "-o", "fresh.ubp")
	fresh := run.plan("fresh.ubp")
	require.Equal(t, objectInfoKnownSteps(), objectInfoStepAddresses(fresh.Steps))
	require.Equal(t, runtime.DecisionRead, fresh.Steps[1].Decision)
	require.Equal(t, objectInfoReadCalls("GET "+secretObjectPath), takeCalls())

	fresh.Inputs["lookup-name"] = ".."
	body, err := json.Marshal(fresh)
	require.NoError(t, err)
	sealed, err := state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(run.work, "invalid-read.ubp"), sealed, 0o600))
	out := run.fail("apply", "invalid-read.ubp")
	require.Contains(t, out, objectInfoConstraint+"1]")
	require.Empty(t, takeCalls())
}

func TestCompiledObjectInfoDeferredConstraints(t *testing.T) {
	t.Run("known bad name with pending namespace", func(t *testing.T) {
		binary, err := compileObjectInfo(t, "pending-namespace", true)
		require.NoError(t, err)
		server, takeCalls := newObjectInfoHTTP(t, func(req *http.Request) (int, any) {
			return http.StatusNotFound, nil
		})
		run := newObjectInfoRun(t, binary, "pending-namespace", server, "", "", "part/name")
		out := run.fail("plan", "-c", "config.ub", "-o", "deferred.ubp")
		require.Contains(t, out, objectInfoConstraint+"1]")
		require.Empty(t, takeCalls())
	})
	const seedCollection = "/api/v1/namespaces/default/configmaps"
	binary, err := compileObjectInfo(t, "pending-name", true)
	require.NoError(t, err)
	for _, name := range []string{"part/name", "example"} {
		t.Run(name, func(t *testing.T) {
			server, takeCalls := newObjectInfoHTTP(t, func(req *http.Request) (int, any) {
				if req.URL.Path == seedCollection || req.URL.Path == seedCollection+"/seed" {
					return http.StatusOK, map[string]any{
						"kind": "ConfigMap", "apiVersion": "v1",
						"metadata": map[string]any{
							"name": "seed", "namespace": "default", "uid": name,
						},
						"data": map[string]any{"value": name},
					}
				}
				return http.StatusOK, observedObjectInfo("v1", "Secret", "Ready")
			})
			run := newObjectInfoRun(t, binary, "pending-name", server, "", "", name)
			run.ok("plan", "-c", "config.ub", "-o", "deferred.ubp")
			steps := run.plan("deferred.ubp").Steps
			require.Equal(t, []string{
				"library-config.core", "library-config.dynamic", "resource.seed",
				objectInfoAddress, "output.observed",
			}, objectInfoStepAddresses(steps))
			require.Equal(t, runtime.DecisionRead, steps[3].Decision)
			require.Empty(t, steps[3].ObservedOutputs)
			require.Equal(t, map[string][]string{
				"metadata": {"resource.seed.object.metadata.uid"},
			}, steps[3].UnresolvedInputs)
			require.Empty(t, takeCalls())
			want := []string{"POST " + seedCollection, "GET " + seedCollection + "/seed"}
			if name == "example" {
				run.ok("apply", "deferred.ubp")
				want = append(want, objectInfoReadCalls("GET "+secretObjectPath)...)
			} else {
				out := run.fail("apply", "deferred.ubp")
				require.Contains(t, out, objectInfoConstraint+"1]")
			}
			require.Equal(t, want, takeCalls())
		})
	}
}

func observedObjectInfo(version, kind, phase string) map[string]any {
	obj := map[string]any{
		"apiVersion": version, "kind": kind,
		"metadata": map[string]any{"name": "example", "namespace": "default", "uid": "live-uid"},
		"status":   map[string]any{"currentPhase": phase},
	}
	if kind == "Secret" {
		obj["data"] = map[string]any{"value": objectInfoSecret}
	} else {
		obj["spec"] = map[string]any{
			"ports": []any{int64(8080), "https"},
			"crd": map[string]any{
				"integer": int64(9007199254740993), "decimal": 0.25, "on": true,
				"null": nil, "emptyMap": map[string]any{}, "emptyList": []any{},
			},
		}
	}
	return obj
}

func objectInfoKnownSteps() []string {
	return []string{"library-config.dynamic", objectInfoAddress, "output.observed"}
}

func objectInfoStepAddresses(steps []runtime.PlanStep) []string {
	result := make([]string, 0, len(steps))
	for _, step := range steps {
		result = append(result, step.Address)
	}
	return result
}

func objectInfoReadCalls(objectCall string) []string {
	return []string{
		"GET /api?timeout=32s",
		"GET /apis?timeout=32s",
		"GET /api/v1?timeout=32s",
		"GET /apis/example.io/v2?timeout=32s",
		objectCall,
	}
}

func newObjectInfoHTTP(
	t *testing.T, observe func(*http.Request) (int, any),
) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		mu.Unlock()
		status, payload := http.StatusOK, any(nil)
		switch req.URL.Path {
		case "/api":
			payload = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			version := metav1.GroupVersionForDiscovery{GroupVersion: "example.io/v2", Version: "v2"}
			payload = metav1.APIGroupList{Groups: []metav1.APIGroup{
				{Name: "example.io", Versions: []metav1.GroupVersionForDiscovery{version},
					PreferredVersion: version},
			}}
		case "/api/v1":
			payload = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
				{Name: "secrets", Kind: "Secret", Namespaced: true},
			}}
		case "/apis/example.io/v2":
			payload = metav1.APIResourceList{
				GroupVersion: "example.io/v2", APIResources: []metav1.APIResource{
					{Name: "widgets", Kind: "Widget", Namespaced: true},
				}}
		default:
			status, payload = observe(req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("write Kubernetes response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		got := calls
		calls = nil
		for i := 2; i < len(got); i++ {
			if got[i] == "GET /api/v1?timeout=32s" &&
				got[i-1] == "GET /apis/example.io/v2?timeout=32s" {
				got[i-1], got[i] = got[i], got[i-1]
			}
		}
		return got
	}
}

type objectInfoRun struct {
	t            *testing.T
	binary, work string
}

func newObjectInfoRun(
	t *testing.T, binary, fixture, server, version, kind, name string,
) objectInfoRun {
	t.Helper()
	run := objectInfoRun{t: t, binary: binary, work: objectInfoWork(t)}
	t.Setenv("KUBECONFIG", filepath.Join(run.work, "no-kubeconfig"))
	run.configure(fixture, server, version, kind, name)
	return run
}

func (r objectInfoRun) configure(fixture, server, version, kind, name string) {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(objectInfoFixtures, fixture, "config.ub"))
	require.NoError(r.t, err)
	config := strings.NewReplacer(
		"KUBE_SERVER", server, "LOOKUP_VERSION", version, "LOOKUP_KIND", kind, "LOOKUP_NAME", name,
	).Replace(string(body))
	require.NoError(r.t, os.WriteFile(filepath.Join(r.work, "config.ub"), []byte(config), 0o600))
	r.ok("pin", "-c", "config.ub")
}

func (r objectInfoRun) command(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(r.t.Context(), r.binary, args...)
	cmd.Dir = r.work
	return cmd.CombinedOutput()
}

func (r objectInfoRun) ok(args ...string) []byte {
	r.t.Helper()
	out, err := r.command(args...)
	require.NoError(r.t, err, "%s %v\n%s", r.binary, args, out)
	return out
}

func (r objectInfoRun) fail(args ...string) string {
	r.t.Helper()
	out, err := r.command(args...)
	require.Error(r.t, err, "%s %v\n%s", r.binary, args, out)
	return string(out)
}

func (r objectInfoRun) plan(path string) *runtime.PlanFile {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(r.work, path))
	require.NoError(r.t, err)
	plan, err := runtime.OpenPlan(body, func(ref *runtime.StateRef) (encrypt.Encrypter, error) {
		require.NotNil(r.t, ref)
		require.Equal(r.t, "noop", ref.Name)
		return encrypters.Noop{}, nil
	})
	require.NoError(r.t, err)
	return plan
}

func (r objectInfoRun) state() *state.Snapshot {
	r.t.Helper()
	snapshot, err := state.DecodeSnapshot(r.ok("state", "pull", "-c", "config.ub"))
	require.NoError(r.t, err)
	return snapshot
}

func objectInfoWork(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "_output", "tests", "dynamic-object-info")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path, err := os.MkdirTemp(dir, "object-info-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(path)) })
	absolute, err := filepath.Abs(path)
	require.NoError(t, err)
	return absolute
}

func compileObjectInfo(t *testing.T, fixture string, build bool) (string, error) {
	t.Helper()
	return compileObjectInfoForAPI(t, fixture, build, nil)
}

func compileObjectInfoForAPI(
	t *testing.T, fixture string, build bool, descriptor *libraryapi.Descriptor,
) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	path, err := filepath.Abs(filepath.Join(objectInfoFixtures, fixture, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: path, OutDir: objectInfoWork(t),
		StackName: "dynamic-object-info", Version: "v0.0.0", CLIVersion: "v0.13.0-a.10",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		LibraryAPIDescriptor: descriptor, Build: build, Stdout: &log, Stderr: &log,
	})
	if err != nil {
		t.Log(log.String())
		return "", err
	}
	if build {
		require.True(t, result.Built)
	}
	return result.BinaryPath, nil
}
