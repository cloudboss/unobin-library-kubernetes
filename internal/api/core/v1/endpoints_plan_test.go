package v1_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
)

//nolint:staticcheck // Manage objects from the core/v1 Endpoints API.
type testEndpoints = corev1.Endpoints

//nolint:staticcheck // Preserve core/v1 Endpoints subset membership.
type testEndpointSubset = corev1.EndpointSubset

const endpointsCollection = "/api/v1/namespaces/default/endpoints"
const endpointsSubject = endpointsCollection + "/subject"
const endpointsSeed = "/api/v1/namespaces/default/configmaps/endpoints-seed"

var endpointsCompile = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/core/v1")
	if err == nil && len(warnings) != 0 {
		err = fmt.Errorf("Core v1 schema: %v", warnings)
	}
	return schema, err
})

type endpointsPlanAPI struct {
	server      string
	mu          sync.Mutex
	objects     map[string]*testEndpoints
	seed        *corev1.ConfigMap
	calls       []string
	patches     []string
	auth        []string
	uid         int
	beforePatch func(*testEndpoints)
}

func startEndpointsPlanAPI(t *testing.T) *endpointsPlanAPI {
	t.Helper()
	api := &endpointsPlanAPI{objects: map[string]*testEndpoints{}}
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		api.calls = append(api.calls, r.Method+" "+p)
		if strings.Contains(p, "/endpoints") && p != endpointsSeed {
			api.auth = append(api.auth, r.Header.Get("Authorization"))
			api.serveEndpoints(t, w, r)
			return
		}
		switch r.Method {
		case http.MethodPost:
			require.NoError(t, json.NewDecoder(r.Body).Decode(&api.seed))
			api.seed.UID = "seed"
		case http.MethodPatch:
			applyEndpointsTestPatch(t, r, api.seed)
		case http.MethodGet:
			require.Equal(t, endpointsSeed, p)
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
		}
		endpointsTestReply(t, w, api.seed)
	}))
	t.Cleanup(server.Close)
	api.server = server.URL
	return api
}

func (a *endpointsPlanAPI) serveEndpoints(t *testing.T, w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	obj := a.objects[p]
	if r.Method == http.MethodPost {
		var created testEndpoints
		require.NoError(t, json.NewDecoder(r.Body).Decode(&created))
		a.uid++
		created.UID = types.UID(fmt.Sprintf("uid-%d", a.uid))
		if created.Name == "" {
			created.Name = created.GenerateName + "actual"
		}
		defaultTestProtocols(&created)
		a.objects[p+"/"+created.Name] = &created
		endpointsTestReply(t, w, &created)
		return
	}
	if obj == nil {
		w.WriteHeader(http.StatusNotFound)
		endpointsTestReply(t, w, metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound,
			Code: http.StatusNotFound})
		return
	}
	switch r.Method {
	case http.MethodPatch:
		require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		a.patches = append(a.patches, string(body))
		if a.beforePatch != nil {
			a.beforePatch(obj)
		}
		if err := patchEndpointsTestObject(body, obj); err != nil {
			w.WriteHeader(http.StatusConflict)
			endpointsTestReply(t, w, metav1.Status{Status: "Failure",
				Reason: metav1.StatusReasonConflict, Code: http.StatusConflict, Message: err.Error()})
			return
		}
		defaultTestProtocols(obj)
	case http.MethodDelete:
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(r.Body).Decode(&options))
		require.Equal(t, metav1.DeleteOptions{
			TypeMeta:      metav1.TypeMeta{APIVersion: "v1", Kind: "DeleteOptions"},
			Preconditions: &metav1.Preconditions{UID: &obj.UID},
		}, options)
		delete(a.objects, p)
		endpointsTestReply(t, w, metav1.Status{Status: "Success"})
		return
	case http.MethodGet:
	default:
		t.Errorf("unexpected request %s %s", r.Method, p)
	}
	endpointsTestReply(t, w, obj)
}

func defaultTestProtocols(obj *testEndpoints) {
	for i := range obj.Subsets {
		for j := range obj.Subsets[i].Ports {
			if obj.Subsets[i].Ports[j].Protocol == "" {
				obj.Subsets[i].Ports[j].Protocol = corev1.ProtocolTCP
			}
		}
	}
}

func applyEndpointsTestPatch(t *testing.T, r *http.Request, obj any) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.NoError(t, patchEndpointsTestObject(body, obj))
}

func patchEndpointsTestObject(body []byte, obj any) error {
	patch, err := jsonpatch.DecodePatch(body)
	if err != nil {
		return err
	}
	original, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	patched, err := patch.Apply(original)
	if err != nil {
		return err
	}
	switch target := obj.(type) {
	case *testEndpoints:
		*target = testEndpoints{}
	case *corev1.ConfigMap:
		*target = corev1.ConfigMap{}
	}
	return json.Unmarshal(patched, obj)
}

func endpointsTestReply(t *testing.T, w http.ResponseWriter, obj any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(obj))
}

func (a *endpointsPlanAPI) expectCalls(t *testing.T, want ...string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	var actual []string
	for _, call := range a.calls {
		if strings.Contains(call, "/endpoints") && !strings.Contains(call, "/configmaps/") {
			actual = append(actual, call)
		}
	}
	require.Equal(t, want, actual)
	a.calls = nil
}

func (a *endpointsPlanAPI) expectPatch(t *testing.T, want string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	require.NotEmpty(t, a.patches)
	require.JSONEq(t, want, a.patches[0])
	require.Empty(t, a.patches[1:])
	a.patches = nil
}

func (a *endpointsPlanAPI) edit(path string, change func(*testEndpoints)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	change(a.objects[path])
}

func (a *endpointsPlanAPI) get(path string) *testEndpoints {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.objects[path].DeepCopy()
}

func endpointsExecutor(t *testing.T, fixture string, api *endpointsPlanAPI) *runtime.Executor {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-endpoints")
	require.NoError(t, os.MkdirAll(base, 0o700))
	dir, err := os.MkdirTemp(base, "factory-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	factory := filepath.Join("testdata/ub", fixture, "factory.ub")
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: dir, StackName: "endpoints", Version: "v0.0.0",
		CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	data, err := os.ReadFile(factory)
	require.NoError(t, err)
	source, err := syntax.ParseSource(factory, data)
	require.NoError(t, err)
	library := core.Library()
	extracted, err := endpointsCompile()
	require.NoError(t, err)
	schema := *extracted
	schema.Resources = maps.Clone(extracted.Resources)
	library.Schema = &schema
	library.Constraints = map[string][]lang.ConstraintSpec{}
	library.Defaults = map[string][]lang.DefaultSpec{}
	for name, entry := range extracted.Resources {
		schema := *entry
		schema.EvaluatedInputs = entry.Inputs
		library.Schema.Resources[name] = &schema
		library.Constraints["resource."+name] = schema.Constraints
		library.Defaults["resource."+name] = schema.Defaults
	}
	libs := map[string]*runtime.Library{"core": library}
	store, err := local.NewStore(dir, "endpoints", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(source.Factory.Body, libs), SyntaxSource: &source.Factory.Body,
		Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: result.FactoryName, Version: result.Version,
			ContentRevision: result.ContentRevision},
		Inputs: map[string]any{"server": api.server},
	}
}

func endpointsSavedPlan(t *testing.T, executor *runtime.Executor) *runtime.PlanFile {
	t.Helper()
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	saved, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return saved
}

func endpointsStep(t *testing.T, saved *runtime.PlanFile) runtime.PlanStep {
	t.Helper()
	for _, step := range saved.Steps {
		if step.Address == "resource.subject" {
			return step
		}
	}
	t.Fatal("plan omitted resource.subject")
	return runtime.PlanStep{}
}

func applyEndpointsPlan(
	t *testing.T, executor *runtime.Executor, saved *runtime.PlanFile,
) error {
	t.Helper()
	inputs := executor.Inputs
	executor.Inputs = nil
	defer func() { executor.Inputs = inputs }()
	_, err := executor.ApplyPlan(t.Context(), saved)
	return err
}

func setEndpointsPlanInputs(executor *runtime.Executor) {
	maps.Copy(executor.Inputs, map[string]any{
		"token": "", "kubeconfig": nil, "current-context": nil,
		"metadata": map[string]any{
			"name":        "subject",
			"annotations": map[string]any{"example.com/annotation": "first"},
		},
		"ignore-annotations": nil, "ignore-labels": nil,
		"subset-key": "selected", "subset": nil,
	})
}

func setEndpointsSubset(executor *runtime.Executor, subset any) {
	executor.Inputs["subset"] = subset
}

func endpointsSubset(port any, protocol *string) []any {
	p := map[string]any{"port": port}
	if protocol != nil {
		p["protocol"] = *protocol
	}
	return []any{map[string]any{
		"address": []any{map[string]any{"ip": "192.0.2.1"}}, "port": []any{p},
	}}
}

func TestEndpointsEvaluatedInputs(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	for _, test := range []struct {
		name, diagnostic string
		subset           any
	}{
		{"missing-ip", `field "subset[0].address[0].ip": required but not provided`,
			[]any{map[string]any{"address": []any{map[string]any{}}}}},
		{"null-ip", `field "subset[0].not-ready-address[0].ip": required but is null`,
			[]any{map[string]any{"not-ready-address": []any{map[string]any{"ip": nil}}}}},
		{"null-port", `field "subset[0].port[0].port": required but is null`,
			endpointsSubset(nil, nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			setEndpointsSubset(executor, test.subset)
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.subject")
			require.ErrorContains(t, err, test.diagnostic)
			api.expectCalls(t)
		})
	}
	for _, test := range []struct {
		field      string
		value      any
		diagnostic string
	}{
		{"metadata", map[string]any{"name": "Bad_Name"}, "metadata.name"},
		{"metadata", map[string]any{"name": "subject", "labels": map[string]any{
			strings.Repeat("a", 254) + "/key": "",
		}}, "metadata.labels"},
		{"ignore-labels", []any{"["}, "ignore-labels"},
		{"ignore-annotations", []any{"["}, "ignore-annotations"},
	} {
		t.Run(test.diagnostic, func(t *testing.T) {
			setEndpointsSubset(executor, nil)
			setEndpointsPlanInputs(executor)
			executor.Inputs[test.field] = test.value
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.subject")
			require.ErrorContains(t, err, "schema: constraints")
			require.ErrorContains(t, err, test.diagnostic)
			api.expectCalls(t)
		})
	}
}

func TestEndpointsRejectsResolvedWrongTypes(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	for _, test := range []struct{ key, diagnostic string }{
		{"wrong-subset-kind", `field "subset": expected list`},
		{"wrong-port-kind", `field "subset[0].port[0].port": expected integer`},
	} {
		t.Run(test.key, func(t *testing.T) {
			executor.Inputs["subset-key"] = test.key
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, "resource.subject")
			require.ErrorContains(t, err, test.diagnostic)
			api.expectCalls(t)
		})
	}
}

func TestEndpointsSavedLifecycle(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	metadata := executor.Inputs["metadata"].(map[string]any)
	annotations := metadata["annotations"].(map[string]any)
	apply := func(decision runtime.Decision, calls ...string) {
		t.Helper()
		saved := endpointsSavedPlan(t, executor)
		require.Equal(t, decision, endpointsStep(t, saved).Decision)
		if decision == runtime.DecisionCreate {
			api.expectCalls(t)
		} else {
			api.expectCalls(t, "GET "+endpointsSubject)
		}
		require.NoError(t, applyEndpointsPlan(t, executor, saved))
		api.expectCalls(t, calls...)
	}
	apply(runtime.DecisionCreate, "POST "+endpointsCollection, "GET "+endpointsSubject)
	metadata["namespace"] = "default"
	apply(runtime.DecisionNoOp)

	address := func(ip string) map[string]any { return map[string]any{"ip": ip} }
	ready := []any{address("192.0.2.1"), address("192.0.2.2")}
	notReady := []any{address("192.0.2.3"), address("192.0.2.4")}
	ports := []any{map[string]any{"port": int64(80)}, map[string]any{"port": int64(81)}}
	subset := map[string]any{"address": ready, "not-ready-address": notReady, "port": ports}
	second := map[string]any{"address": []any{address("192.0.2.5")}}
	setEndpointsSubset(executor, []any{second, subset, second, map[string]any{}})
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	initialPatch := `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/subsets","value":[
			{"addresses":[{"ip":"192.0.2.1"},{"ip":"192.0.2.2"}],
			 "notReadyAddresses":[{"ip":"192.0.2.3"},{"ip":"192.0.2.4"}],
			 "ports":[{"port":80,"protocol":"TCP"},{"port":81,"protocol":"TCP"}]},
			{"addresses":[{"ip":"192.0.2.5"}]}
		]}]`
	api.expectPatch(t, initialPatch)
	expected := []any{map[string]any{
		"address": []any{
			map[string]any{"ip": "192.0.2.1", "hostname": "", "node-name": ""},
			map[string]any{"ip": "192.0.2.2", "hostname": "", "node-name": ""},
		},
		"not-ready-address": []any{
			map[string]any{"ip": "192.0.2.3", "hostname": "", "node-name": ""},
			map[string]any{"ip": "192.0.2.4", "hostname": "", "node-name": ""},
		},
		"port": []any{
			map[string]any{"port": int64(80), "name": "", "protocol": "TCP"},
			map[string]any{"port": int64(81), "name": "", "protocol": "TCP"},
		},
	}, map[string]any{
		"address":           []any{map[string]any{"ip": "192.0.2.5", "hostname": "", "node-name": ""}},
		"not-ready-address": []any{}, "port": []any{},
	}}
	reordered := map[string]any{
		"address":           []any{ready[1], ready[0], ready[1]},
		"not-ready-address": []any{notReady[1], notReady[0], notReady[0]},
		"port":              []any{ports[1], map[string]any{"port": 80, "protocol": "TCP"}, ports[0]},
	}
	setEndpointsSubset(executor, []any{reordered, second, reordered})
	api.edit(endpointsSubject, func(obj *testEndpoints) {
		obj.ResourceVersion, obj.Generation = "2", 17
		obj.Subsets[0].Addresses = append(obj.Subsets[0].Addresses, obj.Subsets[0].Addresses[0])
		obj.Subsets[0].Addresses[0].TargetRef = &corev1.ObjectReference{Kind: "Pod", Name: "test"}
		obj.Subsets[0].Ports[0].AppProtocol = new("example.com/app")
		slices.Reverse(obj.Subsets[0].NotReadyAddresses)
		slices.Reverse(obj.Subsets[0].Ports)
		slices.Reverse(obj.Subsets)
		obj.Subsets = append(obj.Subsets, obj.Subsets[0], testEndpointSubset{})
	})
	saved := endpointsSavedPlan(t, executor)
	step := endpointsStep(t, saved)
	require.Equal(t, runtime.DecisionNoOp, step.Decision)
	require.Equal(t, expected, step.ObservedOutputs["subset"])
	api.expectCalls(t, "GET "+endpointsSubject)
	require.NoError(t, applyEndpointsPlan(t, executor, saved))
	api.expectCalls(t)
	annotations["example.com/annotation"] = "second"
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/metadata/annotations/example.com~1annotation","value":"second"}]`)
	require.NotNil(t, api.get(endpointsSubject).Subsets[1].Addresses[0].TargetRef)

	api.edit(endpointsSubject, func(obj *testEndpoints) {
		obj.Subsets[1].NotReadyAddresses = obj.Subsets[1].Addresses
		obj.Subsets[1].Addresses = nil
	})
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, initialPatch)

	setEndpointsSubset(executor, endpointsSubset(80, new("TCP")))
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/subsets","value":[{"addresses":[{"ip":"192.0.2.1"}],
		"ports":[{"port":80,"protocol":"TCP"}]}]}]`)
	setEndpointsSubset(executor, endpointsSubset(80, new("")))
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/subsets","value":[{"addresses":[{"ip":"192.0.2.1"}],
		"ports":[{"port":80}]}]}]`)
	annotations["example.com/annotation"] = "third"
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/metadata/annotations/example.com~1annotation","value":"third"}]`)
	setEndpointsSubset(executor, []any{})
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/subsets","value":[]}]`)
	require.Equal(t, types.UID("uid-1"), api.get(endpointsSubject).UID)
	require.Empty(t, api.get(endpointsSubject).Subsets)
	api.edit(endpointsSubject, func(obj *testEndpoints) {
		obj.Subsets = []testEndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "192.0.2.9"}},
		}}
	})
	apply(runtime.DecisionUpdate, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/subsets","value":[]}]`)
	require.Empty(t, api.get(endpointsSubject).Subsets)
}

func TestEndpointsSavedConstraintsAndHistoricalDestroy(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	require.NoError(t, applyEndpointsPlan(t, executor, endpointsSavedPlan(t, executor)))
	api.expectCalls(t, "POST "+endpointsCollection, "GET "+endpointsSubject)
	valid := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionNoOp, endpointsStep(t, valid).Decision)
	api.expectCalls(t, "GET "+endpointsSubject)
	require.NoError(t, applyEndpointsPlan(t, executor, valid))
	api.expectCalls(t)

	saved := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionNoOp, endpointsStep(t, saved).Decision)
	api.expectCalls(t, "GET "+endpointsSubject)
	saved.Inputs["ignore-labels"] = []any{"["}
	err := applyEndpointsPlan(t, executor, saved)
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints")
	require.ErrorContains(t, err, "ignore-labels")
	api.expectCalls(t)

	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.subject")
	require.NotNil(t, entry)
	entry.Inputs["ignore-labels"] = []any{"["}
	revision, err := executor.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, executor.Store.SetCurrent(revision))
	executor.Inputs["ignore-labels"] = []any{"["}
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints")
	require.ErrorContains(t, err, "ignore-labels")
	api.expectCalls(t, "GET "+endpointsSubject)

	executor.Destroy = true
	destroy := endpointsSavedPlan(t, executor)
	step := endpointsStep(t, destroy)
	require.Equal(t, runtime.DecisionDestroy, step.Decision)
	require.Equal(t, new(api.server+"/uid-1"), step.ExpectedStableID)
	api.expectCalls(t, "GET "+endpointsSubject)
	require.NoError(t, applyEndpointsPlan(t, executor, destroy))
	api.expectCalls(t, "GET "+endpointsSubject, "DELETE "+endpointsSubject)
	require.Nil(t, api.get(endpointsSubject))
}

func TestEndpointsDeferredConstraints(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-deferred", api)
	maps.Copy(executor.Inputs, map[string]any{
		"name": "subject", "seed": "subject", "deferred-identity": true,
		"known-filter": "[", "resolve-port": false, "port-key": "selected",
	})
	_, err := executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints")
	require.ErrorContains(t, err, "ignore-labels")
	api.expectCalls(t)

	executor.Inputs["known-filter"] = "^external/"
	executor.Inputs["seed"] = "Invalid_Name"
	invalid := endpointsSavedPlan(t, executor)
	step := endpointsStep(t, invalid)
	require.Equal(t, runtime.DecisionCreate, step.Decision)
	require.Contains(t, step.UnresolvedInputs["metadata"], "data-source.live.data['value']")
	err = applyEndpointsPlan(t, executor, invalid)
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints")
	require.ErrorContains(t, err, "metadata.name")
	api.expectCalls(t)

	executor.Inputs["seed"] = "subject"
	valid := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionCreate, endpointsStep(t, valid).Decision)
	require.NoError(t, applyEndpointsPlan(t, executor, valid))
	api.expectCalls(t, "POST "+endpointsCollection, "GET "+endpointsSubject)
	require.Equal(t, map[string]string{"value": "subject"}, api.get(endpointsSubject).Annotations)

	executor.Inputs["deferred-identity"], executor.Inputs["seed"] = false, "second"
	update := endpointsSavedPlan(t, executor)
	step = endpointsStep(t, update)
	require.Contains(t, step.UnresolvedInputs["metadata"], "data-source.live.data['value']")
	api.expectCalls(t, "GET "+endpointsSubject)
	require.NoError(t, applyEndpointsPlan(t, executor, update))
	api.expectCalls(t, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/metadata/annotations/value","value":"second"}]`)

	executor.Inputs["deferred-identity"], executor.Inputs["seed"] = true, "Invalid_Name"
	invalid = endpointsSavedPlan(t, executor)
	step = endpointsStep(t, invalid)
	require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
	api.expectCalls(t, "GET "+endpointsSubject)
	err = applyEndpointsPlan(t, executor, invalid)
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, "schema: constraints")
	require.ErrorContains(t, err, "metadata.name")
	api.expectCalls(t)
	require.Equal(t, types.UID("uid-1"), api.get(endpointsSubject).UID)

	executor.Inputs["seed"] = "replacement"
	replacement := endpointsSavedPlan(t, executor)
	step = endpointsStep(t, replacement)
	require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
	api.expectCalls(t, "GET "+endpointsSubject)
	require.NoError(t, applyEndpointsPlan(t, executor, replacement))
	replacementPath := endpointsCollection + "/replacement"
	api.expectCalls(t, "GET "+endpointsSubject, "DELETE "+endpointsSubject,
		"POST "+endpointsCollection, "GET "+replacementPath)
	require.Nil(t, api.get(endpointsSubject))
	require.Equal(t, types.UID("uid-2"), api.get(replacementPath).UID)
	require.Equal(t, map[string]string{"value": "replacement"}, api.get(replacementPath).Annotations)
}

func TestEndpointsSavedMetadataFilters(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	metadata := executor.Inputs["metadata"].(map[string]any)
	delete(metadata, "annotations")
	require.NoError(t, applyEndpointsPlan(t, executor, endpointsSavedPlan(t, executor)))
	api.expectCalls(t, "POST "+endpointsCollection, "GET "+endpointsSubject)

	applyUpdate := func(calls ...string) map[string]any {
		t.Helper()
		saved := endpointsSavedPlan(t, executor)
		require.Equal(t, runtime.DecisionUpdate, endpointsStep(t, saved).Decision)
		api.expectCalls(t, "GET "+endpointsSubject)
		require.NoError(t, applyEndpointsPlan(t, executor, saved))
		api.expectCalls(t, calls...)
		state, err := executor.Store.Current()
		require.NoError(t, err)
		return state.Find("resource.subject").Outputs["metadata"].(map[string]any)
	}
	api.edit(endpointsSubject, func(obj *testEndpoints) {
		obj.Annotations = map[string]string{"external": "keep"}
		obj.Labels = map[string]string{"external": "keep"}
	})
	executor.Inputs["ignore-annotations"] = []any{"^external$"}
	executor.Inputs["ignore-labels"] = []any{"^external$"}
	output := applyUpdate("GET " + endpointsSubject)
	require.Equal(t, map[string]any{}, output["annotations"])
	require.Equal(t, map[string]any{}, output["labels"])
	require.Equal(t, map[string]string{"external": "keep"}, api.get(endpointsSubject).Labels)
	require.Equal(t, map[string]string{"external": "keep"}, api.get(endpointsSubject).Annotations)
	require.Empty(t, api.patches)

	executor.Inputs["ignore-annotations"] = nil
	executor.Inputs["ignore-labels"] = nil
	output = applyUpdate("GET "+endpointsSubject, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"remove","path":"/metadata/annotations/external"},
		{"op":"remove","path":"/metadata/labels/external"}]`)
	require.Equal(t, map[string]any{}, output["annotations"])
	require.Equal(t, map[string]any{}, output["labels"])

	executor.Inputs["ignore-labels"] = []any{"^external$"}
	output = applyUpdate("GET " + endpointsSubject)
	require.Equal(t, map[string]any{}, output["labels"])
	api.edit(endpointsSubject, func(obj *testEndpoints) {
		obj.Labels = map[string]string{"external": "replace on first managed value"}
	})
	metadata["labels"] = map[string]any{"example.com/key": "managed", "second": "value"}
	output = applyUpdate("PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"add","path":"/metadata/labels","value":{"example.com/key":"managed","second":"value"}}]`)
	require.Equal(t, map[string]any{"example.com/key": "managed", "second": "value"}, output["labels"])
	require.Equal(t, map[string]string{"example.com/key": "managed", "second": "value"},
		api.get(endpointsSubject).Labels)

	api.edit(endpointsSubject, func(obj *testEndpoints) { obj.Labels["external"] = "keep" })
	metadata["labels"] = map[string]any{"second": "updated"}
	output = applyUpdate("PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"remove","path":"/metadata/labels/example.com~1key"},
		{"op":"replace","path":"/metadata/labels/second","value":"updated"}]`)
	require.Equal(t, map[string]any{"second": "updated"}, output["labels"])
	require.Equal(t, map[string]string{"external": "keep", "second": "updated"},
		api.get(endpointsSubject).Labels)

	executor.Inputs["ignore-labels"] = []any{"^external$|^second$"}
	metadata["labels"] = map[string]any{"external": "desired"}
	output = applyUpdate("GET "+endpointsSubject, "PATCH "+endpointsSubject, "GET "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/metadata/labels/external","value":"desired"},
		{"op":"remove","path":"/metadata/labels/second"}]`)
	require.Equal(t, map[string]any{"external": "desired"}, output["labels"])
	require.Equal(t, map[string]string{"external": "desired"}, api.get(endpointsSubject).Labels)
}

func TestEndpointsSavedIdentityAndClusterReplacement(t *testing.T) {
	oldAPI := startEndpointsPlanAPI(t)
	newAPI := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", oldAPI)
	setEndpointsPlanInputs(executor)
	executor.Inputs["token"] = "original"
	require.NoError(t, applyEndpointsPlan(t, executor, endpointsSavedPlan(t, executor)))
	oldAPI.expectCalls(t, "POST "+endpointsCollection, "GET "+endpointsSubject)
	executor.Inputs["token"] = "rotated"
	saved := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionNoOp, endpointsStep(t, saved).Decision)
	oldAPI.expectCalls(t, "GET "+endpointsSubject)
	require.NoError(t, applyEndpointsPlan(t, executor, saved))
	oldAPI.expectCalls(t)

	metadata := executor.Inputs["metadata"].(map[string]any)
	metadata["name"] = "second"
	saved = endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionReplace, endpointsStep(t, saved).Decision)
	oldAPI.expectCalls(t, "GET "+endpointsSubject)
	require.NoError(t, applyEndpointsPlan(t, executor, saved))
	second := endpointsCollection + "/second"
	oldAPI.expectCalls(t, "GET "+endpointsSubject, "DELETE "+endpointsSubject,
		"POST "+endpointsCollection, "GET "+second)
	require.Nil(t, oldAPI.get(endpointsSubject))
	require.Equal(t, types.UID("uid-2"), oldAPI.get(second).UID)
	require.Equal(t, []string{
		"Bearer original", "Bearer original", "Bearer rotated", "Bearer rotated",
		"Bearer rotated", "Bearer rotated", "Bearer rotated", "Bearer rotated",
	}, oldAPI.auth)

	executor.Inputs["server"] = newAPI.server
	saved = endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionReplace, endpointsStep(t, saved).Decision)
	oldAPI.expectCalls(t, "GET "+second)
	newAPI.expectCalls(t)
	require.NoError(t, applyEndpointsPlan(t, executor, saved))
	oldAPI.expectCalls(t, "GET "+second, "DELETE "+second)
	newAPI.expectCalls(t, "POST "+endpointsCollection, "GET "+second)
	require.Nil(t, oldAPI.get(second))
	require.Equal(t, types.UID("uid-1"), newAPI.get(second).UID)
}

func TestEndpointsPatchProtectsRecreatedObject(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	require.NoError(t, applyEndpointsPlan(t, executor, endpointsSavedPlan(t, executor)))
	api.expectCalls(t, "POST "+endpointsCollection, "GET "+endpointsSubject)
	metadata := executor.Inputs["metadata"].(map[string]any)
	metadata["annotations"] = map[string]any{"value": "new"}
	saved := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionUpdate, endpointsStep(t, saved).Decision)
	api.expectCalls(t, "GET "+endpointsSubject)
	api.mu.Lock()
	api.beforePatch = func(obj *testEndpoints) {
		obj.UID = "unmanaged"
		obj.Annotations = map[string]string{"value": "untouched"}
	}
	api.mu.Unlock()
	err := applyEndpointsPlan(t, executor, saved)
	require.ErrorContains(t, err, "patch Endpoints")
	require.ErrorContains(t, err, "conflict")
	api.expectCalls(t, "PATCH "+endpointsSubject)
	api.expectPatch(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"remove","path":"/metadata/annotations/example.com~1annotation"},
		{"op":"add","path":"/metadata/annotations/value","value":"new"}]`)
	require.Equal(t, map[string]string{"value": "untouched"}, api.get(endpointsSubject).Annotations)
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "UID changed from uid-1 to unmanaged")
	api.expectCalls(t, "GET "+endpointsSubject)
}

func TestEndpointsGeneratedTargetsAndConfigReplacementReasons(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	kubeconfig := clientcmdapi.Config{
		CurrentContext: "first",
		Contexts: map[string]*clientcmdapi.Context{
			"first":  {Cluster: "test", AuthInfo: "test", Namespace: "unused"},
			"second": {Cluster: "test", AuthInfo: "test", Namespace: "unused"},
		},
		Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: api.server}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {}},
	}
	require.NoError(t, clientcmd.WriteToFile(kubeconfig, first))
	require.NoError(t, clientcmd.WriteToFile(kubeconfig, second))
	executor.Inputs["kubeconfig"], executor.Inputs["current-context"] = first, "first"
	executor.Inputs["metadata"] = map[string]any{"generate-name": "generated-", "namespace": nil}
	require.NoError(t, applyEndpointsPlan(t, executor, endpointsSavedPlan(t, executor)))
	path := endpointsCollection + "/generated-actual"
	api.expectCalls(t, "POST "+endpointsCollection, "GET "+path)
	require.Equal(t, "generated-", api.get(path).GenerateName)

	for _, test := range []struct {
		field  string
		input  any
		reason string
	}{
		{"metadata", map[string]any{"generate-name": "different-"}, "metadata"},
		{"metadata", map[string]any{"generate-name": "generated-", "namespace": "other"}, "metadata"},
		{"kubeconfig", second, "kubeconfig"},
		{"current-context", "second", "current-context"},
	} {
		t.Run(test.field, func(t *testing.T) {
			original := executor.Inputs[test.field]
			executor.Inputs[test.field] = test.input
			saved := endpointsSavedPlan(t, executor)
			step := endpointsStep(t, saved)
			require.Equal(t, runtime.DecisionReplace, step.Decision)
			require.Equal(t, []string{test.reason}, step.ReplacementReasons)
			require.Equal(t, new(api.server+"/uid-1"), step.ExpectedStableID)
			api.expectCalls(t, "GET "+path)
			executor.Inputs[test.field] = original
		})
	}

	executor.Inputs["metadata"].(map[string]any)["labels"] = map[string]any{"updated": "yes"}
	saved := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionUpdate, endpointsStep(t, saved).Decision)
	api.expectCalls(t, "GET "+path)
	require.NoError(t, applyEndpointsPlan(t, executor, saved))
	api.expectCalls(t, "PATCH "+path, "GET "+path)
	require.Equal(t, map[string]string{"updated": "yes"}, api.get(path).Labels)
	require.Equal(t, types.UID("uid-1"), api.get(path).UID)

	executor.Destroy = true
	saved = endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionDestroy, endpointsStep(t, saved).Decision)
	api.expectCalls(t, "GET "+path)
	require.NoError(t, applyEndpointsPlan(t, executor, saved))
	api.expectCalls(t, "GET "+path, "DELETE "+path)
	require.Nil(t, api.get(path))
}

func TestEndpointsRejectsFractionalPort(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-resource", api)
	setEndpointsPlanInputs(executor)
	valid := endpointsSubset(int64(80), nil)
	bad := endpointsSubset(80.5, nil)
	diagnostic := `field "subset[0].port[0].port": expected integer`

	setEndpointsSubset(executor, bad)
	_, err := executor.Plan(t.Context())
	require.ErrorContains(t, err, diagnostic)
	api.expectCalls(t)

	setEndpointsSubset(executor, valid)
	create := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionCreate, endpointsStep(t, create).Decision)
	create.Inputs["subset"] = bad
	for i := range create.Steps {
		if create.Steps[i].Address == "resource.subject" {
			create.Steps[i].Inputs["subset"] = bad
		}
	}
	require.ErrorContains(t, applyEndpointsPlan(t, executor, create), diagnostic)
	api.expectCalls(t)

	require.NoError(t, applyEndpointsPlan(t, executor, endpointsSavedPlan(t, executor)))
	api.expectCalls(t, "POST "+endpointsCollection, "GET "+endpointsSubject)
	setEndpointsSubset(executor, bad)
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, diagnostic)
	api.expectCalls(t)

	setEndpointsSubset(executor, valid)
	steady := endpointsSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionNoOp, endpointsStep(t, steady).Decision)
	api.expectCalls(t, "GET "+endpointsSubject)
	steady.Inputs["subset"] = bad
	for i := range steady.Steps {
		if steady.Steps[i].Address == "resource.subject" {
			steady.Steps[i].Inputs["subset"] = bad
		}
	}
	require.ErrorContains(t, applyEndpointsPlan(t, executor, steady), diagnostic)
	api.expectCalls(t)
}

func TestEndpointsRejectsDeferredFractionalPortBeforeReplacement(t *testing.T) {
	api := startEndpointsPlanAPI(t)
	executor := endpointsExecutor(t, "endpoints-deferred", api)
	maps.Copy(executor.Inputs, map[string]any{
		"name": "subject", "seed": "subject", "deferred-identity": false,
		"known-filter": "^external/", "resolve-port": false, "port-key": "selected",
	})
	require.NoError(t, applyEndpointsPlan(t, executor, endpointsSavedPlan(t, executor)))
	api.expectCalls(t, "POST "+endpointsCollection, "GET "+endpointsSubject)
	original := api.get(endpointsSubject)

	executor.Inputs["seed"] = "80.5"
	executor.Inputs["deferred-identity"] = true
	executor.Inputs["resolve-port"] = true
	saved := endpointsSavedPlan(t, executor)
	step := endpointsStep(t, saved)
	require.Contains(t, step.UnresolvedInputs, "subset")
	require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
	api.expectCalls(t, "GET "+endpointsSubject)

	err := applyEndpointsPlan(t, executor, saved)
	require.ErrorContains(t, err, "resource.subject")
	require.ErrorContains(t, err, `field "subset[0].port[0].port": expected integer`)
	api.expectCalls(t)
	require.Equal(t, original, api.get(endpointsSubject))
}
