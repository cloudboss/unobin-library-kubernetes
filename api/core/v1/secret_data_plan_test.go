package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
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

const secretDataSeedCollection = "/api/v1/namespaces/default/configmaps"
const secretDataCollection = "/api/v1/namespaces/default/secrets"
const secretDataOutputSentinel = "synthetic-observed-secret"
const secretDataAddress = "resource.attachment"
const secretDataRuntimeFixture = "testdata/ub/secret-data/factory.ub"

type secretDataWrite struct {
	Name, UID, Manager, Force string
	Data                      map[string]string
}

type secretDataPlanAPI struct {
	t       *testing.T
	mu      sync.Mutex
	secrets map[string]*corev1.Secret
	seed    *corev1.ConfigMap
	calls   []string
	writes  []secretDataWrite
}

func (a *secretDataPlanAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := path.Base(req.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	a.calls = append(a.calls, req.Method+" "+req.URL.Path)
	if name == "seed" || req.URL.Path == secretDataSeedCollection {
		a.serveSeed(w, req)
		return
	}
	live := a.secrets[name]
	switch {
	case req.Method == http.MethodGet && live == nil:
		writeConfigMapDataStatus(a.t, w, apierrors.NewNotFound(corev1.Resource("secrets"), name))
		return
	case req.Method == http.MethodGet:
		require.Equal(a.t, secretDataCollection+"/"+name, req.URL.Path)
	case req.Method == http.MethodPatch:
		require.Equal(a.t, secretDataCollection+"/"+name, req.URL.Path)
		require.Equal(a.t, string(types.ApplyPatchType), req.Header.Get("Content-Type"))
		var payload corev1.Secret
		require.NoError(a.t, json.NewDecoder(req.Body).Decode(&payload))
		require.NotNil(a.t, live)
		require.Equal(a.t, live.UID, payload.UID)
		require.Equal(a.t, live.Name, payload.Name)
		require.Equal(a.t, live.Namespace, payload.Namespace)
		data := make(map[string]string, len(payload.Data))
		for key, value := range payload.Data {
			data[key] = string(value)
		}
		a.writes = append(a.writes, secretDataWrite{
			Name: name, UID: string(payload.UID), Manager: req.URL.Query().Get("fieldManager"),
			Force: req.URL.Query().Get("force"), Data: data,
		})
		live.Data = payload.Data
		if _, exists := data["malformed"]; exists {
			live.Data["value"] = []byte(secretDataOutputSentinel)
			live.Data["malformed"] = []byte{'x', 0xff, 0xfe, 'y'}
			live.Data["foreign-binary"] = []byte{0, 0xff, 0xfe, 1}
		}
	default:
		a.t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	require.NoError(a.t, json.NewEncoder(w).Encode(live))
}

func (a *secretDataPlanAPI) serveSeed(w http.ResponseWriter, req *http.Request) {
	live := a.seed
	name := "seed"
	switch {
	case req.Method == http.MethodPost && req.URL.Path == secretDataSeedCollection:
		live = &corev1.ConfigMap{}
		require.NoError(a.t, json.NewDecoder(req.Body).Decode(live))
		require.Equal(a.t, "seed", live.Name)
		live.UID = "uid-seed"
		a.seed = live
	case req.Method == http.MethodGet && live == nil:
		writeConfigMapDataStatus(a.t, w, apierrors.NewNotFound(corev1.Resource("configmaps"), name))
		return
	case req.Method == http.MethodGet:
		require.Equal(a.t, secretDataSeedCollection+"/"+name, req.URL.Path)
	case req.Method == http.MethodPatch:
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
		a.seed = live
	case req.Method == http.MethodDelete:
		a.seed = nil
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

func (a *secretDataPlanAPI) expectWrites(t *testing.T, want ...secretDataWrite) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Equal(t, want, a.writes)
	a.writes = nil
}

func (a *secretDataPlanAPI) expectTargetCalls(t *testing.T, want ...string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	var actual []string
	for _, call := range a.calls {
		if !strings.HasSuffix(call, "/seed") && call != "POST "+secretDataSeedCollection {
			actual = append(actual, call)
		}
	}
	require.Equal(t, want, actual)
	a.calls = nil
}

type secretDataFactory struct {
	binary string
	work   string
	inputs map[string]any
}

func compileSecretDataFactory(t *testing.T, fixture string) *secretDataFactory {
	t.Helper()
	fixture, err := filepath.Abs(fixture)
	require.NoError(t, err)
	result, work := compileSecretFixture(t, fixture, true)
	t.Setenv("KUBECONFIG", filepath.Join(work, "missing"))
	return &secretDataFactory{binary: result.BinaryPath, work: work}
}

func startSecretDataFactory(t *testing.T) (*secretDataFactory, *secretDataPlanAPI) {
	t.Helper()
	factory := compileSecretDataFactory(t, secretDataRuntimeFixture)
	api := &secretDataPlanAPI{t: t, secrets: map[string]*corev1.Secret{}}
	for _, name := range []string{"old", "next"} {
		api.secrets[name] = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", UID: types.UID("uid-" + name),
		}}
	}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	factory.inputs = map[string]any{
		"server": server.URL, "target": "old", "revision": "settled",
		"deferred": false, "defer-data": false,
		"data": map[string]any{"value": "", "one": "initial"},
	}
	return factory, api
}

func (f *secretDataFactory) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir = f.work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *secretDataFactory) pin(t *testing.T) (string, error) {
	t.Helper()
	template, err := os.ReadFile("testdata/ub/secret-data/config.ub")
	require.NoError(t, err)
	inputs, err := ub.Marshal(f.inputs)
	require.NoError(t, err)
	config := bytes.Replace(template, []byte("'SECRET_DATA_INPUTS'"), inputs, 1)
	require.NoError(t, os.WriteFile(filepath.Join(f.work, "config.ub"), config, 0o600))
	return f.run(t, "pin", "-c", "config.ub")
}

func (f *secretDataFactory) plan(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := f.pin(t)
	require.NoError(t, err, "%s", out)
	return f.run(t, append([]string{"plan", "-c", "config.ub", "-o", "plan.ubp"}, args...)...)
}

func (f *secretDataFactory) savedPlan(t *testing.T, args ...string) *runtime.PlanFile {
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

func (f *secretDataFactory) apply(t *testing.T) {
	t.Helper()
	out, err := f.run(t, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
}

func (f *secretDataFactory) applyEditedPlan(t *testing.T, plan *runtime.PlanFile) (string, error) {
	t.Helper()
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	payload, err := state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.work, "edited.ubp"), payload, 0o600))
	return f.run(t, "apply", "edited.ubp")
}

func (f *secretDataFactory) store(t *testing.T, plan *runtime.PlanFile) *local.Store {
	t.Helper()
	store, err := local.NewStore(
		filepath.Join(f.work, "state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	require.NoError(t, err)
	return store
}

func secretDataStep(t *testing.T, saved *runtime.PlanFile) runtime.PlanStep {
	t.Helper()
	for _, step := range saved.Steps {
		if step.Address == secretDataAddress {
			return step
		}
	}
	t.Fatal("plan lacks resource.attachment")
	return runtime.PlanStep{}
}

func requireSecretDataNameError(t *testing.T, out string, err error) {
	t.Helper()
	require.Error(t, err, "%s", out)
	require.Contains(t, out, "resource.attachment: schema: constraints[0] (predicate)")
	require.Contains(t, out, "object.metadata.name")
}

func TestSecretDataNativeOmissionDefaults(t *testing.T) {
	factory := compileSecretDataFactory(t,
		"testdata/ub/secret-data/literals/valid/factory.ub")
	factory.inputs = map[string]any{}
	plan := factory.savedPlan(t)
	wantInputs := map[string]map[string]any{
		"resource.subject": {
			"field-manager": "Terraform", "force": false,
			"object": map[string]any{
				"metadata": map[string]any{"name": strings.Repeat("a", 253), "namespace": "default"},
				"data":     map[string]any{},
			},
		},
		"resource.text": {
			"field-manager": nil, "force": nil,
			"object": map[string]any{
				"metadata": map[string]any{"name": "dotted.name", "namespace": nil},
				"data":     map[string]any{"empty": ""},
			},
		},
		"resource.unscoped": {
			"field-manager": "manager", "force": true,
			"object": map[string]any{
				"metadata": map[string]any{"name": "test", "namespace": ""},
				"data":     map[string]any{},
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

func TestSecretDataNativePlans(t *testing.T) {
	factory, api := startSecretDataFactory(t)
	first := factory.savedPlan(t)
	step := secretDataStep(t, first)
	require.Equal(t, runtime.DecisionCreate, step.Decision)
	factory.apply(t)
	api.expectWrites(t, secretDataWrite{"old", "uid-old", "Terraform", "false",
		map[string]string{"value": "", "one": "initial"}})
	oldPath := secretDataCollection + "/old"
	api.expectTargetCalls(t, "GET "+oldPath, "PATCH "+oldPath, "GET "+oldPath)

	factory.inputs["namespace"] = "default"
	factory.inputs["manager"], factory.inputs["force"] = "Terraform", false
	saved := factory.savedPlan(t)
	step = secretDataStep(t, saved)
	require.Equal(t, runtime.DecisionNoOp, step.Decision)
	require.Equal(t, map[string]any{
		"server": factory.inputs["server"], "api-version": "v1", "kind": "Secret",
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
	require.Equal(t, runtime.DecisionNoOp, secretDataStep(t, saved).Decision)
	saved.Inputs["target"] = "Bad_Name"
	out, err := factory.applyEditedPlan(t, saved)
	requireSecretDataNameError(t, out, err)
	api.expectTargetCalls(t)
	api.expectWrites(t)

	factory.inputs["manager"], factory.inputs["force"] = "latest", true
	saved = factory.savedPlan(t)
	require.Equal(t, runtime.DecisionUpdate, secretDataStep(t, saved).Decision)
	api.expectTargetCalls(t, "GET "+oldPath)
	factory.apply(t)
	api.expectTargetCalls(t, "GET "+oldPath, "PATCH "+oldPath, "GET "+oldPath)
	api.expectWrites(t, secretDataWrite{"old", "uid-old", "latest", "true",
		map[string]string{"value": "", "one": "initial"}})

	store := factory.store(t, saved)
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find(secretDataAddress)
	require.NotNil(t, entry)
	metadata := entry.Inputs["object"].(map[string]any)["metadata"].(map[string]any)
	metadata["name"] = "Bad_Name"
	rev, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(rev))
	factory.inputs["target"] = "Bad_Name"
	out, err = factory.plan(t)
	requireSecretDataNameError(t, out, err)
	api.expectTargetCalls(t, "GET "+oldPath)
	api.expectWrites(t)

	destroy := factory.savedPlan(t, "--destroy")
	step = secretDataStep(t, destroy)
	require.Equal(t, runtime.DecisionDestroy, step.Decision)
	require.Equal(t, new(factory.inputs["server"].(string)+"/uid-old"), step.ExpectedStableID)
	factory.apply(t)
	api.expectTargetCalls(t, "GET "+oldPath, "GET "+oldPath, "GET "+oldPath, "PATCH "+oldPath)
	api.expectWrites(t, secretDataWrite{"old", "uid-old", "latest", "true",
		map[string]string{}})
}

func TestSecretDataNativeDeferredReplacement(t *testing.T) {
	factory, api := startSecretDataFactory(t)
	factory.inputs["manager"], factory.inputs["force"] = "old-owner", true
	factory.savedPlan(t)
	factory.apply(t)
	api.expectWrites(t, secretDataWrite{"old", "uid-old", "old-owner", "true",
		map[string]string{"value": "", "one": "initial"}})
	oldPath, nextPath := secretDataCollection+"/old", secretDataCollection+"/next"
	api.expectTargetCalls(t, "GET "+oldPath, "PATCH "+oldPath, "GET "+oldPath)

	factory.inputs["target"], factory.inputs["defer-data"] = "Bad_Name", true
	factory.inputs["revision"] = "pending-data"
	out, err := factory.plan(t)
	requireSecretDataNameError(t, out, err)
	api.expectTargetCalls(t, "GET "+oldPath)
	api.expectWrites(t)

	factory.inputs["target"], factory.inputs["defer-data"] = "old", false
	factory.inputs["deferred"], factory.inputs["revision"] = true, "Bad_Name"
	factory.inputs["manager"], factory.inputs["force"] = "next-owner", false
	saved := factory.savedPlan(t)
	step := secretDataStep(t, saved)
	require.Equal(t, map[string][]string{"object": {"resource.seed.server"}},
		step.UnresolvedInputs)
	require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
	api.expectTargetCalls(t, "GET "+oldPath)
	out, err = factory.run(t, "apply", "plan.ubp")
	requireSecretDataNameError(t, out, err)
	api.expectTargetCalls(t)
	api.expectWrites(t)
	stateAfterFailure, err := factory.store(t, saved).Current()
	require.NoError(t, err)
	entry := stateAfterFailure.Find(secretDataAddress)
	require.NotNil(t, entry)
	require.Equal(t, map[string]any{"name": "old", "namespace": "default", "uid": "uid-old"},
		entry.Outputs["object"].(map[string]any)["metadata"])

	factory.inputs["revision"], factory.inputs["data"] = "next", map[string]any{"value": "desired"}
	saved = factory.savedPlan(t)
	step = secretDataStep(t, saved)
	require.Equal(t, map[string][]string{"object": {"resource.seed.server"}},
		step.UnresolvedInputs)
	api.expectTargetCalls(t, "GET "+oldPath)
	factory.apply(t)
	api.expectTargetCalls(t, "GET "+oldPath, "GET "+oldPath, "PATCH "+oldPath,
		"GET "+nextPath, "PATCH "+nextPath, "GET "+nextPath)
	api.expectWrites(t,
		secretDataWrite{"old", "uid-old", "old-owner", "true", map[string]string{}},
		secretDataWrite{"next", "uid-next", "next-owner", "false",
			map[string]string{"value": "desired"}},
	)
	api.mu.Lock()
	defer api.mu.Unlock()
	require.Equal(t, map[string][]byte{}, api.secrets["old"].Data)
	require.Equal(t, map[string][]byte{"value": []byte("desired")}, api.secrets["next"].Data)
}

func TestSecretDataNativeSensitivityAndUTF8(t *testing.T) {
	factory, api := startSecretDataFactory(t)
	const inputSentinel = "synthetic-desired-secret"
	wantInputs := map[string]any{
		"value": inputSentinel, "malformed": "before", "replacement": "�",
		"decomposed": "e\u0301", "empty": "",
	}
	factory.inputs["data"] = wantInputs
	out, err := factory.plan(t)
	require.NoError(t, err, "%s", out)
	assertMasked := func(display string) {
		t.Helper()
		require.NotContains(t, display, inputSentinel)
		require.NotContains(t, display, secretDataOutputSentinel)
		require.Contains(t, display, "sensitive")
	}
	assertMasked(out)
	saved := factory.savedPlan(t)
	step := secretDataStep(t, saved)
	require.Equal(t, []string{"object"}, step.SensitiveInputs)
	require.Equal(t, []string{"object"}, step.SensitiveOutputs)
	require.Equal(t, wantInputs, step.Inputs["object"].(map[string]any)["data"])
	out, err = factory.run(t, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	assertMasked(out)

	api.expectWrites(t, secretDataWrite{"old", "uid-old", "Terraform", "false", map[string]string{
		"value": inputSentinel, "malformed": "before", "replacement": "�",
		"decomposed": "e\u0301", "empty": "",
	}})
	oldPath := secretDataCollection + "/old"
	api.expectTargetCalls(t, "GET "+oldPath, "PATCH "+oldPath, "GET "+oldPath)
	snapshot, err := factory.store(t, saved).Current()
	require.NoError(t, err)
	entry := snapshot.Find(secretDataAddress)
	require.NotNil(t, entry)
	require.Equal(t, []string{"object"}, entry.SensitiveInputs)
	require.Equal(t, []string{"object"}, entry.SensitiveOutputs)
	require.Equal(t, wantInputs, entry.Inputs["object"].(map[string]any)["data"])
	wantObject := map[string]any{
		"metadata": map[string]any{"name": "old", "namespace": "default", "uid": "uid-old"},
		"data": map[string]any{
			"value": secretDataOutputSentinel, "malformed": "x��y", "replacement": "�",
			"decomposed": "e\u0301", "empty": "",
		},
	}
	require.Equal(t, wantObject, entry.Outputs["object"])
	require.Equal(t, map[string]any{
		"object": wantObject, "derived": secretDataOutputSentinel,
	}, snapshot.Outputs)

	out, err = factory.run(t, "output", "-c", "config.ub", "--format", "json")
	require.NoError(t, err, "%s", out)
	assertMasked(out)
	var shown struct {
		Outputs map[string]any `json:"outputs"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &shown))
	require.Equal(t, map[string]any{"object": "<sensitive>", "derived": "<sensitive>"}, shown.Outputs)
	out, err = factory.run(t, "state", "show", "-c", "config.ub", secretDataAddress)
	require.NoError(t, err, "%s", out)
	assertMasked(out)
	require.Equal(t, 2, strings.Count(out, "\n  object: <sensitive>"))

	saved = factory.savedPlan(t)
	step = secretDataStep(t, saved)
	require.Equal(t, runtime.DecisionNoOp, step.Decision)
	require.Equal(t, entry.Outputs, step.ObservedOutputs)
	factory.apply(t)
	api.expectTargetCalls(t, "GET "+oldPath)
	api.expectWrites(t)
	api.mu.Lock()
	defer api.mu.Unlock()
	require.Equal(t, []byte{0, 0xff, 0xfe, 1}, api.secrets["old"].Data["foreign-binary"])
}

func TestSecretDataNativeEvaluatedSchema(t *testing.T) {
	factory := compileSecretDataFactory(t, "testdata/ub/secret-data/evaluated/factory.ub")
	api := &secretDataPlanAPI{t: t}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	validMetadata := map[string]any{"name": "actual"}
	validData := map[string]any{}
	for _, tc := range []struct {
		name   string
		set    map[string]any
		detail string
	}{
		{"valid", map[string]any{"object": map[string]any{
			"metadata": validMetadata, "data": validData}}, ""},
		{"null object", map[string]any{"object": nil}, "object"},
		{"scalar object", map[string]any{"object": false}, "object"},
		{"missing metadata", map[string]any{"object": map[string]any{
			"data": validData}}, "object.metadata"},
		{"null metadata", map[string]any{"object": map[string]any{
			"metadata": nil, "data": validData}}, "object.metadata"},
		{"missing name", map[string]any{"object": map[string]any{
			"metadata": map[string]any{}, "data": validData}}, "object.metadata.name"},
		{"null name", map[string]any{"object": map[string]any{
			"metadata": map[string]any{"name": nil}, "data": validData}}, "object.metadata.name"},
		{"numeric name", map[string]any{"object": map[string]any{
			"metadata": map[string]any{"name": 7}, "data": validData}},
			`field "object.metadata.name": expected string`},
		{"missing data", map[string]any{"object": map[string]any{
			"metadata": validMetadata}}, "object.data"},
		{"null data", map[string]any{"object": map[string]any{
			"metadata": validMetadata, "data": nil}}, "object.data"},
		{"null value", map[string]any{"object": map[string]any{
			"metadata": validMetadata, "data": map[string]any{"null": nil}}}, "object.data"},
		{"unknown object field", map[string]any{"object": map[string]any{
			"metadata": validMetadata, "data": validData, "type": "Opaque"}}, "object.type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory.inputs = map[string]any{"server": server.URL}
			for key, value := range tc.set {
				factory.inputs[key] = value
			}
			out, err := factory.plan(t)
			if tc.detail == "" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err, "%s", out)
				require.Contains(t, out, secretDataAddress)
				require.Contains(t, out, tc.detail)
			}
			api.expectTargetCalls(t)
			api.expectWrites(t)
		})
	}
}

func TestSecretDataNativeRejectsMalformedOpaqueInputs(t *testing.T) {
	factory := compileSecretDataFactory(t, "testdata/ub/secret-data/evaluated/factory.ub")
	api := &secretDataPlanAPI{t: t}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		name, input, path, kind string
		value                   any
	}{
		{"list data", "data", "object.data", "map(string)", []any{"item"}},
		{"numeric data entry", "data", `object.data["numeric"]`,
			"string", map[string]any{"numeric": 7}},
		{"numeric manager", "field-manager", "field-manager", "string", 7},
		{"string force", "force", "force", "boolean", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := map[string]any{
				"metadata": map[string]any{"name": "actual"}, "data": map[string]any{},
			}
			factory.inputs = map[string]any{"server": server.URL, "object": object}
			valid := factory.savedPlan(t)
			require.Equal(t, runtime.DecisionCreate, secretDataStep(t, valid).Decision)
			api.expectTargetCalls(t)

			if tc.input == "data" {
				object["data"] = tc.value
			} else {
				factory.inputs[tc.input] = tc.value
			}
			diagnostic := fmt.Sprintf("field %q: expected %s", tc.path, tc.kind)
			out, err := factory.plan(t)
			require.Error(t, err, "%s", out)
			require.Contains(t, out, secretDataAddress)
			require.Contains(t, out, diagnostic)
			api.expectTargetCalls(t)
			api.expectWrites(t)

			if tc.input == "data" {
				valid.Inputs["object"].(map[string]any)["data"] = tc.value
			} else {
				valid.Inputs[tc.input] = tc.value
			}
			out, err = factory.applyEditedPlan(t, valid)
			require.Error(t, err, "%s", out)
			require.Contains(t, out, secretDataAddress)
			require.Contains(t, out, diagnostic)
			api.expectTargetCalls(t)
			api.expectWrites(t)
		})
	}
}

func TestSecretDataRejectsMalformedTypedConfiguration(t *testing.T) {
	factory, api := startSecretDataFactory(t)
	for _, tc := range []struct {
		name, input string
		value       any
	}{
		{"list data", "data", []any{"item"}},
		{"numeric data entry", "data", map[string]any{"numeric": 7}},
		{"numeric manager", "manager", 7},
		{"string force", "force", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := factory.inputs[tc.input]
			factory.inputs[tc.input] = tc.value
			t.Cleanup(func() { factory.inputs[tc.input] = original })
			out, err := factory.pin(t)
			if err == nil {
				out, err = factory.run(t, "plan", "-c", "config.ub", "-o", "plan.ubp")
			}
			require.Error(t, err, "%s", out)
			require.Contains(t, out, tc.input)
			api.expectTargetCalls(t)
			api.expectWrites(t)
		})
	}
}
