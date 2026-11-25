package v1

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const configMapDataAddress = "resource.attachment"
const configMapDataRuntimeFixture = "testdata/ub/config-map-data/factory.ub"
const configMapDataCollection = "/api/v1/namespaces/default/configmaps"

type configMapDataWrite struct {
	Name, UID, Manager, Force string
	Data                      map[string]string
}

type configMapDataPlanAPI struct {
	t      *testing.T
	mu     sync.Mutex
	maps   map[string]*corev1.ConfigMap
	calls  []string
	writes []configMapDataWrite
}

func (a *configMapDataPlanAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := path.Base(req.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	a.calls = append(a.calls, req.Method+" "+req.URL.Path)
	live := a.maps[name]
	switch {
	case req.Method == http.MethodPost && req.URL.Path == configMapDataCollection:
		live = &corev1.ConfigMap{}
		require.NoError(a.t, json.NewDecoder(req.Body).Decode(live))
		require.Equal(a.t, "seed", live.Name)
		live.UID = "uid-seed"
		a.maps[live.Name] = live
	case req.Method == http.MethodGet && live == nil:
		writeConfigMapDataStatus(a.t, w, apierrors.NewNotFound(corev1.Resource("configmaps"), name))
		return
	case req.Method == http.MethodGet:
		require.Equal(a.t, configMapDataCollection+"/"+name, req.URL.Path)
	case req.Method == http.MethodPatch && name == "seed":
		body, err := io.ReadAll(req.Body)
		require.NoError(a.t, err)
		patch, err := jsonpatch.DecodePatch(body)
		require.NoError(a.t, err)
		body, err = json.Marshal(live)
		require.NoError(a.t, err)
		body, err = patch.Apply(body)
		require.NoError(a.t, err)
		live = &corev1.ConfigMap{}
		require.NoError(a.t, json.Unmarshal(body, live))
		a.maps[name] = live
	case req.Method == http.MethodPatch:
		require.Equal(a.t, configMapDataCollection+"/"+name, req.URL.Path)
		require.Equal(a.t, string(types.ApplyPatchType), req.Header.Get("Content-Type"))
		var payload corev1.ConfigMap
		require.NoError(a.t, json.NewDecoder(req.Body).Decode(&payload))
		require.NotNil(a.t, live)
		require.Equal(a.t, live.UID, payload.UID)
		require.Equal(a.t, live.Name, payload.Name)
		require.Equal(a.t, live.Namespace, payload.Namespace)
		a.writes = append(a.writes, configMapDataWrite{
			Name: name, UID: string(payload.UID), Manager: req.URL.Query().Get("fieldManager"),
			Force: req.URL.Query().Get("force"), Data: payload.Data,
		})
		live.Data = payload.Data
	case req.Method == http.MethodDelete && name == "seed":
		delete(a.maps, name)
		require.NoError(a.t, json.NewEncoder(w).Encode(metav1.Status{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
			Status:   metav1.StatusSuccess,
		}))
		return
	default:
		a.t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	require.NoError(a.t, json.NewEncoder(w).Encode(live))
}

func writeConfigMapDataStatus(t *testing.T, w http.ResponseWriter, err *apierrors.StatusError) {
	t.Helper()
	status := err.Status()
	status.TypeMeta = metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}
	w.WriteHeader(int(status.Code))
	require.NoError(t, json.NewEncoder(w).Encode(status))
}

func (a *configMapDataPlanAPI) expectWrites(t *testing.T, want ...configMapDataWrite) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Equal(t, want, a.writes)
	a.writes = nil
}

func (a *configMapDataPlanAPI) expectTargetCalls(t *testing.T, want ...string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	var actual []string
	for _, call := range a.calls {
		if !strings.HasSuffix(call, "/seed") && call != "POST "+configMapDataCollection {
			actual = append(actual, call)
		}
	}
	require.Equal(t, want, actual)
	a.calls = nil
}

type configMapDataFactory struct {
	binary string
	work   string
	inputs map[string]any
}

func compileConfigMapDataFactory(t *testing.T, fixture string) *configMapDataFactory {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output", "tests", "config-map-data")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "factory-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err = filepath.Abs(fixture)
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: fixture, OutDir: work, StackName: "config-map-data",
		Version: "v0.0.0", CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	return &configMapDataFactory{binary: result.BinaryPath, work: work}
}

func startConfigMapDataFactory(t *testing.T) (*configMapDataFactory, *configMapDataPlanAPI) {
	t.Helper()
	factory := compileConfigMapDataFactory(t, configMapDataRuntimeFixture)
	api := &configMapDataPlanAPI{t: t, maps: map[string]*corev1.ConfigMap{}}
	for _, name := range []string{"old", "next"} {
		api.maps[name] = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", UID: types.UID("uid-" + name),
		}}
	}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	t.Setenv("KUBECONFIG", filepath.Join(factory.work, "missing"))
	factory.inputs = map[string]any{
		"server": server.URL, "target": "old", "revision": "settled", "deferred": false,
		"data": map[string]any{"value": "", "one": "initial"},
	}
	return factory, api
}

func (f *configMapDataFactory) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir = f.work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *configMapDataFactory) plan(t *testing.T, args ...string) (string, error) {
	t.Helper()
	template, err := os.ReadFile("testdata/ub/config-map-data/config.ub")
	require.NoError(t, err)
	inputs, err := ub.Marshal(f.inputs)
	require.NoError(t, err)
	config := bytes.Replace(template, []byte("'CONFIG_MAP_DATA_INPUTS'"), inputs, 1)
	require.NoError(t, os.WriteFile(filepath.Join(f.work, "config.ub"), config, 0o600))
	out, err := f.run(t, "pin", "-c", "config.ub")
	require.NoError(t, err, "%s", out)
	return f.run(t, append([]string{"plan", "-c", "config.ub", "-o", "plan.ubp"}, args...)...)
}

func (f *configMapDataFactory) savedPlan(t *testing.T, args ...string) *runtime.PlanFile {
	t.Helper()
	out, err := f.plan(t, args...)
	require.NoError(t, err, "%s", out)
	data, err := os.ReadFile(filepath.Join(f.work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(data, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
}

func (f *configMapDataFactory) apply(t *testing.T) {
	t.Helper()
	out, err := f.run(t, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
}

func (f *configMapDataFactory) store(t *testing.T, plan *runtime.PlanFile) *local.Store {
	t.Helper()
	store, err := local.NewStore(
		filepath.Join(f.work, "state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	require.NoError(t, err)
	return store
}

func configMapDataStep(t *testing.T, saved *runtime.PlanFile) runtime.PlanStep {
	t.Helper()
	for _, step := range saved.Steps {
		if step.Address == configMapDataAddress {
			return step
		}
	}
	t.Fatal("plan lacks resource.attachment")
	return runtime.PlanStep{}
}

func requireConfigMapDataNameError(t *testing.T, out string, err error) {
	t.Helper()
	require.Error(t, err, "%s", out)
	require.Contains(t, out, "resource.attachment: schema: constraints[0] (predicate)")
	require.Contains(t, out, "object.metadata.name")
}

func TestConfigMapDataNativeOmissionDefaults(t *testing.T) {
	factory := compileConfigMapDataFactory(t,
		"testdata/ub/config-map-data/literals/valid/factory.ub")
	factory.inputs = map[string]any{}
	plan := factory.savedPlan(t)
	wantInputs := map[string]map[string]any{
		"resource.subject": {
			"field-manager": "Terraform", "force": false,
			"object": map[string]any{
				"metadata": map[string]any{"name": "test", "namespace": "default"},
				"data":     map[string]any{},
			},
		},
		"resource.text": {
			"field-manager": "manager", "force": true,
			"object": map[string]any{
				"metadata": map[string]any{"name": "dotted.name", "namespace": ""},
				"data":     map[string]any{"empty": ""},
			},
		},
	}
	actual := map[string]map[string]any{}
	for _, step := range plan.Steps {
		if step.Kind == runtime.NodeResource {
			actual[step.Address] = step.Inputs
		}
	}
	require.Equal(t, wantInputs, actual)
}

func TestConfigMapDataNativePlans(t *testing.T) {
	factory, api := startConfigMapDataFactory(t)
	first := factory.savedPlan(t)
	step := configMapDataStep(t, first)
	require.Equal(t, runtime.DecisionCreate, step.Decision)
	factory.apply(t)
	api.expectWrites(t, configMapDataWrite{"old", "uid-old", "Terraform", "false",
		map[string]string{"value": "", "one": "initial"}})
	oldPath := configMapDataCollection + "/old"
	api.expectTargetCalls(t, "GET "+oldPath, "PATCH "+oldPath, "GET "+oldPath)

	factory.inputs["namespace"] = "default"
	saved := factory.savedPlan(t)
	step = configMapDataStep(t, saved)
	require.Equal(t, runtime.DecisionNoOp, step.Decision)
	require.Equal(t, map[string]any{
		"server": factory.inputs["server"], "api-version": "v1", "kind": "ConfigMap",
		"object": map[string]any{
			"metadata": map[string]any{"name": "old", "namespace": "default", "uid": "uid-old"},
			"data":     map[string]any{"value": "", "one": "initial"},
		},
	}, step.ObservedOutputs)
	api.expectTargetCalls(t, "GET "+oldPath)
	factory.apply(t)
	api.expectTargetCalls(t)
	api.expectWrites(t)

	saved = factory.savedPlan(t)
	api.expectTargetCalls(t, "GET "+oldPath)
	require.Equal(t, runtime.DecisionNoOp, configMapDataStep(t, saved).Decision)
	saved.Inputs["target"] = "Bad_Name"
	body, err := json.Marshal(saved)
	require.NoError(t, err)
	payload, err := state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(factory.work, "invalid.ubp"), payload, 0o600))
	out, err := factory.run(t, "apply", "invalid.ubp")
	requireConfigMapDataNameError(t, out, err)
	api.expectTargetCalls(t)
	api.expectWrites(t)

	store := factory.store(t, saved)
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find(configMapDataAddress)
	require.NotNil(t, entry)
	metadata := entry.Inputs["object"].(map[string]any)["metadata"].(map[string]any)
	metadata["name"] = "Bad_Name"
	rev, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(rev))
	factory.inputs["target"] = "Bad_Name"
	out, err = factory.plan(t)
	requireConfigMapDataNameError(t, out, err)
	api.expectTargetCalls(t, "GET "+oldPath)
	api.expectWrites(t)

	destroy := factory.savedPlan(t, "--destroy")
	step = configMapDataStep(t, destroy)
	require.Equal(t, runtime.DecisionDestroy, step.Decision)
	require.Equal(t, new(factory.inputs["server"].(string)+"/uid-old"), step.ExpectedStableID)
	factory.apply(t)
	api.expectWrites(t, configMapDataWrite{"old", "uid-old", "Terraform", "false",
		map[string]string{}})
}

func TestConfigMapDataNativeDeferredReplacement(t *testing.T) {
	factory, api := startConfigMapDataFactory(t)
	factory.inputs["manager"], factory.inputs["force"] = "old-owner", true
	factory.savedPlan(t)
	factory.apply(t)
	api.expectWrites(t, configMapDataWrite{"old", "uid-old", "old-owner", "true",
		map[string]string{"value": "", "one": "initial"}})
	oldPath, nextPath := configMapDataCollection+"/old", configMapDataCollection+"/next"
	api.expectTargetCalls(t, "GET "+oldPath, "PATCH "+oldPath, "GET "+oldPath)

	factory.inputs["deferred"], factory.inputs["revision"] = true, "Bad_Name"
	factory.inputs["manager"], factory.inputs["force"] = "next-owner", false
	saved := factory.savedPlan(t)
	step := configMapDataStep(t, saved)
	require.Equal(t, map[string][]string{"object": {"resource.seed.server"}},
		step.UnresolvedInputs)
	require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
	api.expectTargetCalls(t, "GET "+oldPath)
	out, err := factory.run(t, "apply", "plan.ubp")
	requireConfigMapDataNameError(t, out, err)
	api.expectTargetCalls(t)
	api.expectWrites(t)
	stateAfterFailure, err := factory.store(t, saved).Current()
	require.NoError(t, err)
	entry := stateAfterFailure.Find(configMapDataAddress)
	require.NotNil(t, entry)
	require.Equal(t, map[string]any{"name": "old", "namespace": "default", "uid": "uid-old"},
		entry.Outputs["object"].(map[string]any)["metadata"])

	factory.inputs["revision"], factory.inputs["data"] = "next", map[string]any{"new": "desired"}
	saved = factory.savedPlan(t)
	step = configMapDataStep(t, saved)
	require.Equal(t, map[string][]string{"object": {"resource.seed.server"}},
		step.UnresolvedInputs)
	api.expectTargetCalls(t, "GET "+oldPath)
	factory.apply(t)
	api.expectTargetCalls(t, "GET "+oldPath, "GET "+oldPath, "PATCH "+oldPath,
		"GET "+nextPath, "PATCH "+nextPath, "GET "+nextPath)
	api.expectWrites(t,
		configMapDataWrite{"old", "uid-old", "old-owner", "true", map[string]string{}},
		configMapDataWrite{"next", "uid-next", "next-owner", "false",
			map[string]string{"new": "desired"}},
	)
	api.mu.Lock()
	defer api.mu.Unlock()
	require.Equal(t, map[string]string{}, api.maps["old"].Data)
	require.Equal(t, map[string]string{"new": "desired"}, api.maps["next"].Data)
}
