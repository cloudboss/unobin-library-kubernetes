package compile_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const endpointSliceFixture = "testdata/ub/endpoint-slice-evaluated"

type endpointSliceFactory struct {
	work   string
	binary string
	api    *endpointSliceMock
}

func compiledEndpointSlice(t *testing.T) endpointSliceFactory {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	source, err := filepath.Abs(filepath.Join(endpointSliceFixture, "factory.ub"))
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/discovery-v1-endpoint-slice")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "factory-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: source, OutDir: work,
		StackName: "endpoint-slice-test", Version: "v0.0.0", CLIVersion: "v0.13.0-a.10",
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())

	api := &endpointSliceMock{}
	server := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(work, "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "test",
		Clusters:       map[string]*clientcmdapi.Cluster{"test": {Server: server.URL}},
		Contexts: map[string]*clientcmdapi.Context{
			"test": {Cluster: "test", Namespace: "other"}, "alternate": {Cluster: "test"},
		},
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)
	return endpointSliceFactory{work: work, binary: result.BinaryPath, api: api}
}

func (f endpointSliceFactory) command(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir = f.work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f endpointSliceFactory) plan(t *testing.T, name string, flags ...string) (string, error) {
	t.Helper()
	const file = "config.ub"
	body, err := os.ReadFile(filepath.Join(endpointSliceFixture, "config-"+name+".ub"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.work, file), body, 0o600))
	out, err := f.command(t, "pin", "-c", file)
	require.NoError(t, err, "%s", out)
	args := append([]string{"plan", "-c", file, "-o", "plan.ubp"}, flags...)
	return f.command(t, args...)
}

func (f endpointSliceFactory) readPlan(t *testing.T) *runtime.PlanFile {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(f.work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(encoded, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
}

func (f endpointSliceFactory) step(t *testing.T) runtime.PlanStep {
	t.Helper()
	for _, step := range f.readPlan(t).Steps {
		if step.Address == "resource.slice" {
			return step
		}
	}
	t.Fatal("missing resource.slice in saved plan")
	return runtime.PlanStep{}
}

type endpointSliceMock struct {
	mu      sync.Mutex
	calls   []string
	patches []string
	object  *discoveryv1.EndpointSlice
	next    int
}

func (a *endpointSliceMock) serve(w http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, req.Method)
	w.Header().Set("Content-Type", "application/json")
	err := a.handle(w, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *endpointSliceMock) handle(w http.ResponseWriter, req *http.Request) error {
	const path = "/apis/discovery.k8s.io/v1/namespaces/default/endpointslices"
	if req.URL.Path != path && req.URL.Path != path+"/example" {
		return fmt.Errorf("unexpected path %s", req.URL.Path)
	}
	if req.Method != "POST" && a.object == nil {
		w.WriteHeader(http.StatusNotFound)
		return json.NewEncoder(w).Encode(metav1.Status{
			Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
		})
	}
	switch req.Method {
	case "POST":
		if a.object != nil {
			return fmt.Errorf("already exists")
		}
		obj := &discoveryv1.EndpointSlice{}
		if err := json.NewDecoder(req.Body).Decode(obj); err != nil {
			return err
		}
		a.next++
		obj.UID = types.UID(fmt.Sprintf("uid-%d", a.next))
		a.object = obj
	case "PATCH":
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return err
		}
		a.patches = append(a.patches, string(body))
		patch, err := jsonpatch.DecodePatch(body)
		if err != nil {
			return err
		}
		current, err := json.Marshal(a.object)
		if err != nil {
			return err
		}
		patched, err := patch.Apply(current)
		if err != nil {
			return err
		}
		obj := &discoveryv1.EndpointSlice{}
		if err := json.Unmarshal(patched, obj); err != nil {
			return err
		}
		a.object = obj
	case "DELETE":
		var options metav1.DeleteOptions
		if err := json.NewDecoder(req.Body).Decode(&options); err != nil {
			return err
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil ||
			*options.Preconditions.UID != a.object.UID {
			return fmt.Errorf("wrong delete preconditions: %v", options.Preconditions)
		}
		a.object = nil
		return json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
	case "GET":
	default:
		return fmt.Errorf("unexpected method %s", req.Method)
	}
	for i := range a.object.Ports {
		if a.object.Ports[i].Protocol == nil {
			a.object.Ports[i].Protocol = new(corev1.ProtocolTCP)
		}
	}
	return json.NewEncoder(w).Encode(a.object)
}

func (a *endpointSliceMock) takeCalls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func TestEndpointSliceEvaluatedInputs(t *testing.T) {
	f := compiledEndpointSlice(t)
	for _, name := range []string{"valid", "generated", "hostname-63"} {
		t.Run(name, func(t *testing.T) {
			out, err := f.plan(t, name)
			require.NoError(t, err, "%s", out)
			require.Equal(t, runtime.DecisionCreate, f.step(t).Decision)
			require.Empty(t, f.api.takeCalls())
		})
	}
	for _, tt := range []struct {
		name string
		want string
	}{
		{"invalid-enum", "address-type must be IPv4, IPv6 or FQDN"},
		{"bad-name", "constraints[1]"},
		{"long-name", "constraints[1]"},
		{"long-prefix", "constraints[2]"},
		{"empty-prefix", "constraints[2]"},
		{"empty-name", "constraints[1]"},
		{"both-names", "constraints[0]"},
		{"missing-name", "constraints[0]"},
		{"bad-label-key", "constraints[3]"},
		{"bad-label-value", "constraints[4]"},
		{"bad-annotation-key", "constraints[5]"},
		{"bad-ignore-regex", "ignore-annotations[0]"},
		{"port-text", "nonnegative int64 decimal string (ports[0])"},
		{"port-negative", "nonnegative int64 decimal string (ports[0])"},
		{"port-empty", "nonnegative int64 decimal string (ports[0])"},
		{"port-fractional", "nonnegative int64 decimal string (ports[0])"},
		{"port-overflow", "nonnegative int64 decimal string (ports[0])"},
		{"protocol", "constraints[13] (predicate): predicate requirement not satisfied (ports[0])"},
		{"hostname-64", "hostname must be a DNS label of at most 63 bytes (endpoints[0])"},
		{"hostname-dot", "hostname must be a DNS label of at most 63 bytes (endpoints[0])"},
		{"hostname-empty", "hostname must be a DNS label of at most 63 bytes (endpoints[0])"},
		{"partial-hostname", "hostname must be a DNS label of at most 63 bytes (endpoints[0])"},
		{"partial-ports", "ports must contain 1 to 100 items"},
		{"comprehension-ports", "ports must contain 1 to 100 items"},
		{"no-ports", "ports must contain 1 to 100 items"},
		{"no-endpoints", "endpoints must contain 1 to 1000 items"},
		{"too-many-endpoints", "endpoints must contain 1 to 1000 items"},
		{"missing-app", `"ports[0].app-protocol": required but not provided`},
		{"null-app", `"ports[0].app-protocol": required but is null`},
		{"missing-port", `"ports[0].port": required but not provided`},
		{"missing-ref-name", `"endpoints[0].target-ref.name": required but not provided`},
		{"null-ref-name", `"endpoints[0].target-ref.name": required but is null`},
		{"missing-addresses", `"endpoints[0].addresses": required but not provided`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := f.plan(t, tt.name)
			require.Error(t, err, "%s", out)
			require.Contains(t, out, "resource.slice")
			require.Contains(t, out, tt.want)
			require.Empty(t, f.api.takeCalls())
		})
	}
}

func (f endpointSliceFactory) readyPlan(
	t *testing.T, name string, decision runtime.Decision,
) runtime.PlanStep {
	t.Helper()
	out, err := f.plan(t, name)
	require.NoError(t, err, "%s", out)
	step := f.step(t)
	require.Equal(t, decision, step.Decision, "%s", out)
	return step
}

func (f endpointSliceFactory) apply(t *testing.T, calls ...string) {
	t.Helper()
	out, err := f.command(t, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	require.Equal(t, calls, f.api.takeCalls())
}

func (f endpointSliceFactory) state(t *testing.T) *local.Store {
	t.Helper()
	plan := f.readPlan(t)
	store, err := local.NewStore(filepath.Join(f.work, ".unobin/state"),
		plan.Factory.Name, plan.Stack, encrypters.Noop{})
	require.NoError(t, err)
	return store
}

func savedEndpointSlice(t *testing.T, store *local.Store) *state.Entry {
	t.Helper()
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.slice")
	require.NotNil(t, entry)
	return entry
}

func TestEndpointSliceSavedPlans(t *testing.T) {
	f := compiledEndpointSlice(t)

	step := f.readyPlan(t, "deferred-invalid-enum", runtime.DecisionCreate)
	require.Equal(t, map[string][]string{"address-type": {"action.seed.token"}},
		step.UnresolvedInputs)
	out, err := f.command(t, "apply", "plan.ubp")
	require.Error(t, err, "%s", out)
	require.Contains(t, out, "resource.slice: schema: constraints[8] (predicate): address-type")
	require.Empty(t, f.api.takeCalls())

	f.readyPlan(t, "valid", runtime.DecisionCreate)
	require.Empty(t, f.api.takeCalls())
	f.apply(t, "POST", "GET")
	store := f.state(t)
	first := savedEndpointSlice(t, store)
	require.Equal(t, "uid-1", first.Outputs["uid"])
	require.Equal(t, "IPv4", first.Outputs["address-type"])

	f.readyPlan(t, "valid", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t)
	require.Equal(t, first, savedEndpointSlice(t, store))

	t.Run("invalid saved NoOp", func(t *testing.T) {
		f.readyPlan(t, "valid", runtime.DecisionNoOp)
		require.Equal(t, []string{"GET"}, f.api.takeCalls())
		plan := f.readPlan(t)
		plan.Inputs["address-type"] = "bad"
		for _, step := range plan.Steps {
			if step.Address == "resource.slice" {
				step.Inputs["address-type"] = "bad"
			}
		}
		body, err := json.Marshal(plan)
		require.NoError(t, err)
		body, err = state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(f.work, "legacy.ubp"), body, 0o600))
		out, err := f.command(t, "apply", "legacy.ubp")
		require.Error(t, err, "%s", out)
		require.Contains(t, out, "resource.slice: schema: constraints[8] (predicate): address-type")
		require.Empty(t, f.api.takeCalls())
		require.Equal(t, first, savedEndpointSlice(t, store))
	})

	for _, tt := range []struct {
		name, field, diagnostic string
		decision                runtime.Decision
	}{
		{"deferred-invalid-update", "endpoints", "hostname must be a DNS label",
			runtime.DecisionUpdate},
		{"deferred-invalid-enum", "address-type", "constraints[8] (predicate): address-type",
			runtime.DecisionReplace},
		{"deferred-missing-app", "ports", `"ports[0].app-protocol": required but not provided`,
			runtime.DecisionReplace},
		{"deferred-null-ref-name", "endpoints", `"endpoints[0].target-ref.name": required but is null`,
			runtime.DecisionReplace},
	} {
		t.Run(tt.name, func(t *testing.T) {
			step := f.readyPlan(t, tt.name, tt.decision)
			require.Equal(t, map[string][]string{tt.field: {"action.seed.token"}},
				step.UnresolvedInputs)
			if tt.field == "address-type" {
				require.Equal(t, []string{"address-type"}, step.PendingReplacementReasons)
				require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
			}
			require.Equal(t, []string{"GET"}, f.api.takeCalls())
			out, err := f.command(t, "apply", "plan.ubp")
			require.Error(t, err, "%s", out)
			require.Contains(t, out, "resource.slice:")
			require.Contains(t, out, tt.diagnostic)
			require.Empty(t, f.api.takeCalls())
			require.Equal(t, first, savedEndpointSlice(t, store))
			f.api.mu.Lock()
			uid := f.api.object.UID
			f.api.mu.Unlock()
			require.Equal(t, types.UID("uid-1"), uid)
		})
	}

	step = f.readyPlan(t, "deferred-valid-replace", runtime.DecisionReplace)
	require.Equal(t, []string{"address-type"}, step.PendingReplacementReasons)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t, "GET", "DELETE", "POST", "GET")
	replaced := savedEndpointSlice(t, store)
	require.Equal(t, "uid-2", replaced.Outputs["uid"])
	require.Equal(t, "IPv6", replaced.Outputs["address-type"])

	snapshot, err := store.Current()
	require.NoError(t, err)
	snapshot.Find("resource.slice").Inputs["address-type"] = "ipv4"
	rev, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(rev))
	out, err = f.plan(t, "invalid-enum")
	require.Error(t, err, "%s", out)
	require.Contains(t, out, "resource.slice: schema: constraints[8] (predicate): address-type")
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	out, err = f.plan(t, "invalid-enum", "--destroy")
	require.NoError(t, err, "%s", out)
	require.Equal(t, runtime.DecisionDestroy, f.step(t).Decision)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t, "GET", "DELETE")
	f.api.mu.Lock()
	require.Nil(t, f.api.object)
	f.api.mu.Unlock()
}

func TestEndpointSlicePlanAndPatchDrift(t *testing.T) {
	f := compiledEndpointSlice(t)
	f.readyPlan(t, "valid", runtime.DecisionCreate)
	f.apply(t, "POST", "GET")
	store := f.state(t)

	f.readyPlan(t, "change-protocol", runtime.DecisionUpdate)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t, "PATCH", "GET")
	f.api.mu.Lock()
	require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},`+
		`{"op":"replace","path":"/ports","value":[{"name":"","port":80,"appProtocol":""}]}]`,
		f.api.patches[0])
	f.api.object.ResourceVersion, f.api.object.Generation = "new-version", 2
	f.api.mu.Unlock()

	f.readyPlan(t, "change-protocol", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t)
	entry := savedEndpointSlice(t, store)
	require.Equal(t, "uid-1", entry.Outputs["uid"])
	require.Equal(t, []any{map[string]any{
		"port": "80", "protocol": "TCP", "app-protocol": "", "name": nil,
	}}, entry.Outputs["ports"])

	f.api.mu.Lock()
	f.api.object.Endpoints[0].Addresses = []string{"192.0.2.2"}
	f.api.object.Ports = nil
	f.api.mu.Unlock()
	step := f.readyPlan(t, "change-protocol", runtime.DecisionUpdate)
	require.True(t, step.Drift())
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t, "PATCH", "GET")
	f.api.mu.Lock()
	require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},`+
		`{"op":"replace","path":"/endpoints","value":[{"addresses":["192.0.2.1"],"conditions":{}}]},`+
		`{"op":"add","path":"/ports","value":[{"name":"","port":80,"appProtocol":""}]}]`,
		f.api.patches[1])
	require.Equal(t, []string{"192.0.2.1"}, f.api.object.Endpoints[0].Addresses)
	f.api.object.Ports[0].Protocol = new(corev1.ProtocolUDP)
	f.api.mu.Unlock()

	f.readyPlan(t, "change-protocol", runtime.DecisionUpdate)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t, "PATCH", "GET")
	require.Equal(t, entry.Outputs, savedEndpointSlice(t, store).Outputs)
	f.api.mu.Lock()
	require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},`+
		`{"op":"replace","path":"/ports","value":[{"name":"","port":80,"appProtocol":""}]}]`,
		f.api.patches[2])
	f.api.object = nil
	f.api.mu.Unlock()

	f.readyPlan(t, "change-protocol", runtime.DecisionCreate)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
}

func TestEndpointSliceChangedMetadataFilters(t *testing.T) {
	for _, tt := range []struct {
		name, initial, desired string
		calls                  []string
		wantExternal           bool
	}{
		{"add-filter", "valid", "ignore-external", []string{"GET"}, true},
		{"remove-filter", "ignore-external", "valid", []string{"GET", "PATCH", "GET"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := compiledEndpointSlice(t)
			f.readyPlan(t, tt.initial, runtime.DecisionCreate)
			f.apply(t, "POST", "GET")
			store := f.state(t)

			f.api.mu.Lock()
			f.api.object.Labels["external"] = "keep"
			f.api.object.Annotations["external"] = "keep"
			f.api.mu.Unlock()

			f.readyPlan(t, tt.desired, runtime.DecisionUpdate)
			require.Equal(t, []string{"GET"}, f.api.takeCalls())
			f.apply(t, tt.calls...)
			f.api.mu.Lock()
			obj, patches := f.api.object.DeepCopy(), f.api.patches
			f.api.mu.Unlock()

			labels := map[string]string{"test": "valid"}
			annotations := map[string]string{"test": "test"}
			if tt.wantExternal {
				labels["external"], annotations["external"] = "keep", "keep"
				require.Empty(t, patches)
			} else {
				require.Len(t, patches, 1)
				require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},`+
					`{"op":"remove","path":"/metadata/annotations/external"},`+
					`{"op":"remove","path":"/metadata/labels/external"}]`, patches[0])
			}
			require.Equal(t, types.UID("uid-1"), obj.UID)
			require.Equal(t, labels, obj.Labels)
			require.Equal(t, annotations, obj.Annotations)
			metadata := savedEndpointSlice(t, store).Outputs["metadata"].(map[string]any)
			require.Equal(t, map[string]any{"test": "valid"}, metadata["labels"])
			require.Equal(t, map[string]any{"test": "test"}, metadata["annotations"])
		})
	}
}

func TestEndpointSliceDestroyWithHistoricalMetadataFilters(t *testing.T) {
	f := compiledEndpointSlice(t)
	f.readyPlan(t, "valid", runtime.DecisionCreate)
	f.apply(t, "POST", "GET")
	store := f.state(t)
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.slice")
	entry.Inputs["ignore-labels"] = []any{"["}
	entry.Inputs["ignore-annotations"] = []any{"["}
	rev, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(rev))

	out, err := f.plan(t, "valid", "--destroy")
	require.NoError(t, err, "%s", out)
	require.Equal(t, runtime.DecisionDestroy, f.step(t).Decision)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
	f.apply(t, "GET", "DELETE")
	f.api.mu.Lock()
	obj := f.api.object
	f.api.mu.Unlock()
	require.Nil(t, obj)
	snapshot, err = store.Current()
	require.NoError(t, err)
	require.Nil(t, snapshot.Find("resource.slice"))
}

func TestEndpointSliceReplacementFields(t *testing.T) {
	f := compiledEndpointSlice(t)
	f.readyPlan(t, "valid", runtime.DecisionCreate)
	f.apply(t, "POST", "GET")
	for _, tt := range []struct{ name, reason string }{
		{"replacement-name", "metadata"},
		{"replacement-prefix", "metadata"},
		{"replacement-namespace", "metadata"},
		{"resolver-server", "server"},
		{"resolver-kubeconfig", "kubeconfig"},
		{"resolver-context", "current-context"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			step := f.readyPlan(t, tt.name, runtime.DecisionReplace)
			require.Equal(t, []string{tt.reason}, step.ReplacementReasons)
			require.Equal(t, []string{"GET"}, f.api.takeCalls())
		})
	}
	f.readyPlan(t, "credential", runtime.DecisionNoOp)
	require.Equal(t, []string{"GET"}, f.api.takeCalls())
}
