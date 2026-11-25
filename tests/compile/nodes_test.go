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
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
)

const (
	nodesFixtures   = "testdata/ub/nodes"
	nodesAddress    = "data-source.lookup"
	nodesGet        = "GET /api/v1/nodes"
	nodesSeedPath   = "/api/v1/namespaces/default/configmaps"
	nodesConstraint = nodesAddress + ": schema: constraints["
)

func TestCompiledNodesLiterals(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "valid"},
		{"list-metadata", "metadata"},
		{"missing-labels", "labels"},
		{"null-labels", "labels"},
		{"wrong-value-type", "type mismatch: expected string, got integer"},
		{"null-label-value", "type mismatch: expected string, got null"},
		{"invalid-key", "constraints[0]"},
		{"invalid-value", "constraints[1]"},
		{"long-name", "constraints[0]"},
		{"long-prefix", "constraints[0]"},
		{"long-value", "constraints[1]"},
		{"aggregate-id", "id"},
		{"extra-metadata", "generate-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileNodesFactory(t, filepath.Join(nodesFixtures, tc.name), false, nil)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
			if strings.HasPrefix(tc.want, "constraints[") {
				require.ErrorContains(t, err, nodesAddress)
			}
		})
	}
}

func TestCompiledNodesLibraryAPI(t *testing.T) {
	_, err := compileNodesFactory(t, filepath.Join(nodesFixtures, "valid"), false,
		&libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		})
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	require.ErrorContains(t, err, "1.1")
}

func TestCompiledNodesEvaluatedPresence(t *testing.T) {
	fix := filepath.Join(nodesFixtures, "schema")
	binary, err := compileNodesFactory(t, fix, true, nil)
	require.NoError(t, err)
	api := newCompiledNodesAPI(t)
	run := newNodesRun(t, binary)
	for _, tc := range []struct {
		mode string
		want string
	}{
		{mode: "empty"},
		{mode: "absent"},
		{"missing", `"metadata.labels": required but not provided`},
		{"null-labels", `"metadata.labels": required but is null`},
		{"null-label-value", `"metadata.labels[\"app\"]": required but is null`},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			run := run
			run.t = t
			run.configure(fix, "config-"+tc.mode+".ub", "KUBE_SERVER", api.url)
			if tc.want == "" {
				run.ok("plan", "-c", "config.ub", "-o", "read.ubp")
				require.Equal(t, []string{nodesGet}, api.takeCalls())
				return
			}
			out := run.fail("plan", "-c", "config.ub", "-o", "read.ubp")
			require.Contains(t, out, nodesAddress)
			require.Contains(t, out, tc.want)
			require.Empty(t, api.takeCalls())
		})
	}
}

func TestCompiledNodesEvaluatedConstraints(t *testing.T) {
	fix := filepath.Join(nodesFixtures, "evaluated")
	binary, err := compileNodesFactory(t, fix, true, nil)
	require.NoError(t, err)
	api := newCompiledNodesAPI(t)
	run := newNodesRun(t, binary)
	configure := func(key, value string) {
		run.configure(fix, "config.ub", "KUBE_SERVER", api.url,
			"SELECTOR_KEY", key, "SELECTOR_VALUE", value)
	}
	read := nodesGet + "?labelSelector=app%3Dready"
	configure("app", "ready")
	run.ok("plan", "-c", "config.ub", "-o", "ready.ubp")
	run.ok("apply", "ready.ubp")
	require.Equal(t, []string{read, read}, api.takeCalls())

	for _, tc := range []struct {
		key, value, rule string
	}{
		{"Upper.example/Tier", "", "0]"},
		{"app", "-invalid", "1]"},
		{strings.Repeat("x", 64), "valid", "0]"},
		{strings.Repeat("x", 254) + "/app", "", "0]"},
		{"app", strings.Repeat("x", 64), "1]"},
	} {
		configure(tc.key, tc.value)
		out := run.fail("plan", "-c", "config.ub", "-o", "invalid.ubp")
		require.Contains(t, out, nodesConstraint+tc.rule)
		require.Contains(t, out, "metadata.labels")
		require.Empty(t, api.takeCalls())
	}

	configure("app", "ready")
	run.ok("plan", "-c", "config.ub", "-o", "fresh.ubp")
	fresh := run.plan("fresh.ubp")
	step := nodesReadStep(t, fresh)
	require.Equal(t, runtime.DecisionRead, step.Decision)
	require.Equal(t, map[string]any{"nodes": []any{}}, step.ObservedOutputs)
	require.Equal(t, []string{read}, api.takeCalls())

	fresh.Inputs["selector-value"] = "-invalid"
	body, err := json.Marshal(fresh)
	require.NoError(t, err)
	sealed, err := state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(run.work, "invalid-read.ubp"), sealed, 0o600))
	out := run.fail("apply", "invalid-read.ubp")
	require.Contains(t, out, nodesConstraint+"1]")
	require.Empty(t, api.takeCalls())
}

func TestCompiledNodesPendingConstraints(t *testing.T) {
	fix := filepath.Join(nodesFixtures, "pending")
	binary, err := compileNodesFactory(t, fix, true, nil)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, mode, key, value, uid string
		planRule, applyRule, query  string
	}{
		{
			name: "known literal key", mode: "literal-key", value: "good", planRule: "0]",
		},
		{
			name: "known sibling value", mode: "literal-value", value: "-invalid", planRule: "1]",
		},
		{
			name: "known comprehension key", mode: "comprehension", key: "Upper.example/Tier",
			planRule: "0]",
		},
		{
			name: "deferred invalid value", mode: "literal-value", value: "good", uid: "bad value",
			applyRule: "1]",
		},
		{
			name: "deferred valid value", mode: "literal-value", value: "good", uid: "blue",
			query: "?labelSelector=app%3Dblue%2Csibling%3Dgood",
		},
		{
			name: "deferred invalid comprehension key", mode: "pending-key", uid: "Upper.example/Tier",
			applyRule: "0]",
		},
		{
			name: "deferred valid comprehension key", mode: "pending-key", uid: "example.com/Tier",
			query: "?labelSelector=example.com%2FTier%3Dinclude",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newCompiledNodesAPI(t)
			api.setSeedUID(tc.uid)
			run := newNodesRun(t, binary)
			run.configure(fix, "config.ub", "KUBE_SERVER", api.url, "FIXTURE_MODE", tc.mode,
				"SELECTOR_KEY", tc.key, "SELECTOR_VALUE", tc.value)
			if tc.planRule != "" {
				out := run.fail("plan", "-c", "config.ub", "-o", "deferred.ubp")
				require.Contains(t, out, nodesConstraint+tc.planRule)
				require.Contains(t, out, "metadata.labels")
				require.Empty(t, api.takeCalls())
				return
			}

			run.ok("plan", "-c", "config.ub", "-o", "deferred.ubp")
			step := nodesReadStep(t, run.plan("deferred.ubp"))
			require.Equal(t, runtime.DecisionRead, step.Decision)
			require.Nil(t, step.ObservedOutputs)
			require.Equal(t, map[string][]string{
				"metadata": {"resource.seed.object.metadata.uid"},
			}, step.UnresolvedInputs)
			require.Empty(t, api.takeCalls())

			want := []string{
				"POST " + nodesSeedPath, "GET " + nodesSeedPath + "/node-selector-seed",
			}
			if tc.applyRule != "" {
				out := run.fail("apply", "deferred.ubp")
				require.Contains(t, out, nodesConstraint+tc.applyRule)
				require.Contains(t, out, "metadata.labels")
			} else {
				run.ok("apply", "deferred.ubp")
				want = append(want, nodesGet+tc.query)
			}
			require.Equal(t, want, api.takeCalls())
		})
	}
}

func TestCompiledNodesSavedReads(t *testing.T) {
	fix := filepath.Join(nodesFixtures, "evaluated")
	binary, err := compileNodesFactory(t, fix, true, nil)
	require.NoError(t, err)
	api := newCompiledNodesAPI(t)
	run := newNodesRun(t, binary)
	run.configure(fix, "config.ub", "KUBE_SERVER", api.url,
		"SELECTOR_KEY", "app", "SELECTOR_VALUE", "stable")
	read := nodesGet + "?labelSelector=app%3Dstable"

	run.ok("plan", "-c", "config.ub", "-o", "empty.ubp")
	empty := nodesReadStep(t, run.plan("empty.ubp"))
	require.Equal(t, map[string]any{"nodes": []any{}}, empty.ObservedOutputs)
	run.ok("apply", "empty.ubp")
	require.Equal(t, []string{read, read}, api.takeCalls())

	run.ok("plan", "-c", "config.ub", "-o", "steady.ubp")
	steady := nodesReadStep(t, run.plan("steady.ubp"))
	require.Equal(t, runtime.DecisionRead, steady.Decision)
	require.Equal(t, empty.ObservedOutputs, steady.ObservedOutputs)
	run.ok("apply", "steady.ubp")
	require.Equal(t, []string{read, read}, api.takeCalls())

	run.ok("plan", "-c", "config.ub", "-o", "changed-presence.ubp")
	api.setNodes(http.StatusNotFound, `{"kind":"Status","reason":"NotFound","code":404}`)
	out := run.fail("apply", "changed-presence.ubp")
	require.Contains(t, out, "data source "+nodesAddress+" changed since the plan was computed")
	require.Equal(t, []string{read, read}, api.takeCalls())

	run.ok("plan", "-c", "config.ub", "-o", "absent.ubp")
	absent := nodesReadStep(t, run.plan("absent.ubp"))
	require.Equal(t, map[string]any{"nodes": nil}, absent.ObservedOutputs)
	run.ok("apply", "absent.ubp")
	require.Equal(t, []string{read, read}, api.takeCalls())

	api.setNodes(http.StatusOK, `{"items":[{"metadata":{
		"name":"node","resourceVersion":"1","namespace":"ignored","generateName":"ignored-"
	}}]}`)
	run.ok("plan", "-c", "config.ub", "-o", "changed-node.ubp")
	observed := nodesReadStep(t, run.plan("changed-node.ubp"))
	require.Equal(t, map[string]any{"nodes": []any{map[string]any{
		"metadata": map[string]any{
			"name": "node", "resource-version": "1", "uid": "", "generation": int64(0),
			"annotations": map[string]any{}, "labels": map[string]any{},
		},
		"spec": map[string]any{
			"pod-cidr": "", "pod-cidrs": []any{}, "provider-id": "", "unschedulable": false,
			"taints": []any{},
		},
		"status": map[string]any{
			"allocatable": map[string]any{}, "capacity": map[string]any{},
			"addresses": []any{}, "conditions": []any{},
			"node-info": map[string]any{
				"machine-id": "", "system-uuid": "", "boot-id": "", "kernel-version": "",
				"os-image": "", "container-runtime-version": "", "kubelet-version": "",
				"kube-proxy-version": "", "operating-system": "", "architecture": "",
			},
		},
	}}}, observed.ObservedOutputs)
	api.setNodes(http.StatusOK, `{"items":[{"metadata":{"name":"node","resourceVersion":"2"}}]}`)
	out = run.fail("apply", "changed-node.ubp")
	require.Contains(t, out, "data source "+nodesAddress+" changed since the plan was computed")
	require.Equal(t, []string{read, read}, api.takeCalls())

	run.ok("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
	run.ok("apply", "destroy.ubp")
	require.Empty(t, api.takeCalls())
}

type compiledNodesAPI struct {
	mu     sync.Mutex
	url    string
	calls  []string
	uid    string
	status int
	body   string
}

func newCompiledNodesAPI(t *testing.T) *compiledNodesAPI {
	t.Helper()
	api := &compiledNodesAPI{status: http.StatusOK, body: `{"items":[]}`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api/v1/nodes":
			w.WriteHeader(api.status)
			if _, err := w.Write([]byte(api.body)); err != nil {
				t.Error(err)
			}
		case nodesSeedPath, nodesSeedPath + "/node-selector-seed":
			body := map[string]any{
				"kind": "ConfigMap", "apiVersion": "v1",
				"metadata": map[string]any{
					"name": "node-selector-seed", "namespace": "default", "uid": api.uid,
				},
			}
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func (api *compiledNodesAPI) setNodes(status int, body string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.status, api.body = status, body
}

func (api *compiledNodesAPI) setSeedUID(uid string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.uid = uid
}

func (api *compiledNodesAPI) takeCalls() []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	calls := api.calls
	api.calls = nil
	return calls
}

type nodesRun struct {
	t            *testing.T
	binary, work string
}

func newNodesRun(t *testing.T, binary string) nodesRun {
	t.Helper()
	run := nodesRun{t: t, binary: binary, work: nodesWork(t)}
	t.Setenv("KUBECONFIG", filepath.Join(run.work, "no-kubeconfig"))
	return run
}

func (r nodesRun) configure(fix, name string, replacements ...string) {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(fix, name))
	require.NoError(r.t, err)
	config := strings.NewReplacer(replacements...).Replace(string(body))
	require.NoError(r.t, os.WriteFile(filepath.Join(r.work, "config.ub"), []byte(config), 0o600))
	r.ok("pin", "-c", "config.ub")
}

func (r nodesRun) command(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(r.t.Context(), r.binary, args...)
	cmd.Dir = r.work
	return cmd.CombinedOutput()
}

func (r nodesRun) ok(args ...string) {
	r.t.Helper()
	out, err := r.command(args...)
	require.NoError(r.t, err, "%v\n%s", args, out)
}

func (r nodesRun) fail(args ...string) string {
	r.t.Helper()
	out, err := r.command(args...)
	require.Error(r.t, err, "%v\n%s", args, out)
	return string(out)
}

func (r nodesRun) plan(name string) *runtime.PlanFile {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(r.work, name))
	require.NoError(r.t, err)
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(r.t, err)
	return plan
}

func nodesReadStep(t *testing.T, plan *runtime.PlanFile) runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == nodesAddress {
			return step
		}
	}
	t.Fatal("saved plan is missing the Node read")
	return runtime.PlanStep{}
}

func nodesWork(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../_output/tests/core-v1-nodes")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	work, err := os.MkdirTemp(dir, "nodes-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	return work
}

func compileNodesFactory(
	t *testing.T, fix string, build bool, descriptor *libraryapi.Descriptor,
) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	factory, err := filepath.Abs(filepath.Join(fix, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: nodesWork(t), StackName: "nodes-test", Version: "v0.0.0",
		CLIVersion: "v0.13.1", GoVersion: "1.26.2", LibraryAPIDescriptor: descriptor,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            build, Stdout: &log, Stderr: &log,
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
