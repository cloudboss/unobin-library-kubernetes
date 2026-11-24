package v1_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const namespacesList = "GET /api/v1/namespaces"

func TestNamespacesSavedPlan(t *testing.T) {
	const namespaceRead = namespacesList + "/managed"
	const namespaceCreate = "POST /api/v1/namespaces"
	var mu sync.Mutex
	list := `{
		"kind":"NamespaceList","apiVersion":"v1",
		"items":[{"metadata":{"name":"zulu"}},{"metadata":{"name":"managed"}}]
	}`
	status := http.StatusOK
	setResponse := func(code int, body string) {
		mu.Lock()
		defer mu.Unlock()
		list, status = body, code
	}
	namespace := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "managed", UID: "uid"}}
	c, requests := namespacesAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.RequestURI() {
		case namespaceCreate:
			var requested corev1.Namespace
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&requested))
			assert.Equal(t, "managed", requested.Name)
			w.WriteHeader(http.StatusCreated)
			assert.NoError(t, json.NewEncoder(w).Encode(namespace))
		case namespaceRead:
			assert.NoError(t, json.NewEncoder(w).Encode(namespace))
		case namespacesList:
			mu.Lock()
			defer mu.Unlock()
			w.WriteHeader(status)
			_, err := w.Write([]byte(list))
			assert.NoError(t, err)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	t.Setenv("KUBECONFIG", *c.Kubeconfig)
	cmd := compileNamespacesFactory(t, "namespaces-plan")

	readRequests := 0
	checkRequests := func(want ...string) {
		t.Helper()
		got := requests()
		require.True(t, slices.Equal(want, got[readRequests:]),
			"requests = %q, want %q", got[readRequests:], want)
		readRequests = len(got)
	}
	want := []any{"zulu", "managed"}
	wantOutputs := map[string]any{"names": want, "joined": "zulu,managed"}
	plan := runNamespacesPlan(t, cmd)
	require.Equal(t, runtime.DecisionCreate, namespacesStep(t, plan, "resource.stable").Decision)
	deferred := namespacesStep(t, plan, "data-source.cluster")
	require.Equal(t, runtime.DecisionRead, deferred.Decision)
	require.Nil(t, deferred.ObservedOutputs)
	checkRequests()
	runNamespacesCommand(t, cmd, "apply", "plan.ubp")
	checkRequests(namespaceCreate, namespacesList)
	require.Equal(t, wantOutputs, namespacesOutputs(t, cmd))
	checkRequests()

	plan = runNamespacesPlan(t, cmd)
	require.Equal(t, runtime.DecisionNoOp, namespacesStep(t, plan, "resource.stable").Decision)
	observed := namespacesStep(t, plan, "data-source.cluster")
	require.Equal(t, runtime.DecisionRead, observed.Decision)
	require.Equal(t, map[string]any{"namespaces": want}, observed.ObservedOutputs)
	checkRequests(namespaceRead, namespacesList)
	runNamespacesCommand(t, cmd, "apply", "plan.ubp")
	checkRequests(namespacesList)
	require.Equal(t, wantOutputs, namespacesOutputs(t, cmd))
	checkRequests()

	runNamespacesPlan(t, cmd)
	checkRequests(namespaceRead, namespacesList)
	setResponse(http.StatusOK, `{"items":[{"metadata":{"name":"managed"}}]}`)
	body, err := cmd.run(t, "apply", "plan.ubp")
	require.Error(t, err)
	require.Contains(t, string(body),
		"data source data-source.cluster changed since the plan was computed; plan again")
	checkRequests(namespacesList)

	for _, body := range []string{`{"items":[]}`, `{}`} {
		setResponse(http.StatusOK, body)
		plan := runNamespacesPlan(t, cmd)
		step := namespacesStep(t, plan, "data-source.cluster")
		require.Equal(t, map[string]any{"namespaces": []any{}}, step.ObservedOutputs)
		checkRequests(namespaceRead, namespacesList)
		runNamespacesCommand(t, cmd, "apply", "plan.ubp")
		checkRequests(namespacesList)
		require.Equal(t, map[string]any{"names": []any{}, "joined": ""}, namespacesOutputs(t, cmd))
		checkRequests()
	}

	setResponse(http.StatusForbidden, `{
		"kind":"Status","apiVersion":"v1","code":403,"reason":"Forbidden",
		"status":"Failure","message":"namespaces are forbidden"
	}`)
	body, err = cmd.run(t, "plan", "-c", "config.ub", "-o", "plan.ubp")
	require.Error(t, err)
	require.Contains(t, string(body), "list Namespaces: namespaces are forbidden")
	checkRequests(namespaceRead, namespacesList)
}

func TestNamespacesQuotedArgumentInPinnedRuntime(t *testing.T) {
	c, requests := namespacesAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"items":[{"metadata":{"name":"observed"}}]}`))
		assert.NoError(t, err)
	})
	t.Setenv("KUBECONFIG", *c.Kubeconfig)
	cmd := compileNamespacesFactory(t, "namespaces-quoted")

	// The pinned compiler and runtime skip quoted construct argument keys.
	plan := runNamespacesPlan(t, cmd)
	step := namespacesStep(t, plan, "data-source.cluster")
	require.Empty(t, step.Inputs)
	require.Equal(t, map[string]any{"namespaces": []any{"observed"}}, step.ObservedOutputs)
	require.Equal(t, []string{namespacesList}, requests())
	runNamespacesCommand(t, cmd, "apply", "plan.ubp")
	require.Equal(t, map[string]any{"names": []any{"observed"}}, namespacesOutputs(t, cmd))
	require.Equal(t, []string{namespacesList, namespacesList}, requests())
}

type namespacesCommand struct {
	dir    string
	binary string
}

func (c namespacesCommand) run(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), c.binary, args...)
	cmd.Dir = c.dir
	return cmd.CombinedOutput()
}

func compileNamespacesFactory(t *testing.T, fixture string) namespacesCommand {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	factory, err := filepath.Abs(filepath.Join("testdata/ub", fixture, "factory.ub"))
	require.NoError(t, err)
	work := t.TempDir()
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work, StackName: fixture, Version: "v0.0.0",
		CLIVersion: "v0.13.0-a.11", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	config, err := os.ReadFile("testdata/ub/namespaces-plan/config.ub")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), config, 0o600))
	cmd := namespacesCommand{dir: work, binary: result.BinaryPath}
	runNamespacesCommand(t, cmd, "pin", "-c", "config.ub")
	return cmd
}

func runNamespacesCommand(t *testing.T, cmd namespacesCommand, args ...string) []byte {
	t.Helper()
	body, err := cmd.run(t, args...)
	require.NoError(t, err, "%v: %s", args, body)
	return body
}

func runNamespacesPlan(t *testing.T, cmd namespacesCommand) *runtime.PlanFile {
	t.Helper()
	runNamespacesCommand(t, cmd, "plan", "-c", "config.ub", "-o", "plan.ubp")
	body, err := os.ReadFile(filepath.Join(cmd.dir, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
}

func namespacesStep(t *testing.T, plan *runtime.PlanFile, address string) runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == address {
			return step
		}
	}
	t.Fatalf("plan lacks %s", address)
	return runtime.PlanStep{}
}

func namespacesOutputs(t *testing.T, cmd namespacesCommand) map[string]any {
	t.Helper()
	var result struct {
		Outputs map[string]any `json:"outputs"`
	}
	body := runNamespacesCommand(t, cmd, "output", "--format", "json", "-c", "config.ub")
	require.NoError(t, json.Unmarshal(body, &result))
	return result.Outputs
}
