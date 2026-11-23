package compile_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	secretLookupFixture = "testdata/ub/secret-lookup"
	secretLookupAddress = "data-source.lookup"
	secretLookupGET     = "GET /api/v1/namespaces/default/secrets/lookup"
	secretSeedPath      = "/api/v1/namespaces/default/configmaps"
	secretSeedGET       = "GET " + secretSeedPath + "/seed"
	secretSeedPOST      = "POST " + secretSeedPath
)

type secretLookupTestAPI struct {
	mu      sync.Mutex
	calls   []string
	payload map[string][]byte
	seedUID types.UID
	seed    corev1.ConfigMap
}

func (s *secretLookupTestAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request := req.Method + " " + req.URL.RequestURI()
	s.calls = append(s.calls, request)
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	switch request {
	case secretLookupGET:
		_ = encoder.Encode(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "lookup", Namespace: "default", UID: "lookup-uid", ResourceVersion: "11",
			},
			Type: "example.test/visible-type", Immutable: new(true), Data: s.payload,
		})
	case secretSeedPOST:
		if err := json.NewDecoder(req.Body).Decode(&s.seed); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.seed.UID = s.seedUID
		s.seed.ResourceVersion = "1"
		_ = encoder.Encode(&s.seed)
	case secretSeedGET:
		_ = encoder.Encode(&s.seed)
	default:
		http.Error(w, "unexpected "+request, http.StatusBadRequest)
	}
}

func (s *secretLookupTestAPI) expect(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, want, s.calls)
	s.calls = nil
}

type secretLookupFactory struct {
	binary string
	work   string
}

func newSecretLookupFactory(t *testing.T, fixture string) secretLookupFactory {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output", "tests", "secret-lookup")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, filepath.Base(fixture)+"-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	t.Setenv("KUBECONFIG", filepath.Join(work, "missing"))
	factory, err := filepath.Abs(filepath.Join(fixture, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work, Build: true, StackName: "secret-lookup",
		Version: "v0.0.0", CLIVersion: "v0.13.0-a.11", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	return secretLookupFactory{binary: result.BinaryPath, work: work}
}

func (f secretLookupFactory) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir = f.work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f secretLookupFactory) success(t *testing.T, args ...string) string {
	t.Helper()
	out, err := f.run(t, args...)
	require.NoError(t, err, "%s", out)
	return out
}

func (f secretLookupFactory) configure(t *testing.T, stack string, inputs map[string]any) string {
	t.Helper()
	template, err := os.ReadFile(filepath.Join(secretLookupFixture, "config.ub"))
	require.NoError(t, err)
	encoded, err := ub.Marshal(inputs)
	require.NoError(t, err)
	body := bytes.Replace(template, []byte("'SECRET_LOOKUP_INPUTS'"), encoded, 1)
	config := stack + ".ub"
	require.NoError(t, os.WriteFile(filepath.Join(f.work, config), body, 0o600))
	f.success(t, "pin", "-c", config)
	return config
}

func (f secretLookupFactory) saved(t *testing.T) *runtime.PlanFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
}

func (f secretLookupFactory) snapshot(t *testing.T, plan *runtime.PlanFile) *state.Snapshot {
	t.Helper()
	store, err := local.NewStore(
		filepath.Join(f.work, "state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	require.NoError(t, err)
	snap, err := store.Current()
	require.NoError(t, err)
	return snap
}

func secretLookupStep(t *testing.T, saved *runtime.PlanFile, address string) runtime.PlanStep {
	t.Helper()
	for _, step := range saved.Steps {
		if step.Address == address {
			return step
		}
	}
	t.Fatalf("plan lacks %s", address)
	return runtime.PlanStep{}
}

func TestCompiledSecretLookupSavedObservation(t *testing.T) {
	factory := newSecretLookupFactory(t, filepath.Join(secretLookupFixture, "observed"))
	const selector = "synthetic selector placeholder"
	const token = "synthetic text payload"
	const mixed = " \xff\xfe雪\xff\uFFFD e\u0301\t\n"
	const normalized = " \uFFFD\uFFFD雪\uFFFD\uFFFD e\u0301\t\n"
	const encoded = "IP/+6Zuq/++/vSBlzIEJCg=="
	const alternateText = "synthetic text changed"
	const alternateBytes = "synthetic bytes changed"
	api := &secretLookupTestAPI{payload: map[string][]byte{
		"token": []byte(token), "text": []byte(mixed), "bytes": []byte(mixed),
		"empty-text": {}, "empty-bytes": {},
	}}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	config := factory.configure(t, "observation",
		map[string]any{"server": server.URL, "selector": selector})
	assertMasked := func(out string) {
		t.Helper()
		for _, value := range []string{
			selector, token, encoded, alternateText, alternateBytes,
			base64.StdEncoding.EncodeToString([]byte(alternateBytes)),
		} {
			require.NotContains(t, out, value)
		}
	}
	wantMetadata := map[string]any{
		"name": "lookup", "generate-name": "", "namespace": "default",
		"annotations": map[string]any{}, "labels": map[string]any{},
		"generation": int64(0), "uid": "lookup-uid", "resource-version": "11",
	}
	wantBinary := map[string]any{"bytes": encoded, "absent": "", "empty-bytes": ""}
	wantText := map[string]any{"token": token, "text": normalized, "empty-text": ""}
	wantInputs := map[string]any{"bytes": selector, "absent": "", "empty-bytes": "", "token": nil}
	wantObserved := map[string]any{
		"metadata": wantMetadata, "binary-data": wantBinary, "data": wantText,
		"type": "example.test/visible-type", "immutable": true,
	}

	assertMasked(factory.success(t, "plan", "-c", config, "-o", "plan.ubp"))
	api.expect(t, secretLookupGET)
	saved := factory.saved(t)
	step := secretLookupStep(t, saved, secretLookupAddress)
	require.Equal(t, runtime.DecisionRead, step.Decision)
	require.Empty(t, step.UnresolvedInputs)
	require.Equal(t, wantInputs, step.Inputs["binary-data"])
	require.Equal(t, wantObserved, step.ObservedOutputs)
	require.Equal(t, []string{"binary-data"}, step.SensitiveInputs)
	require.Equal(t, []string{"binary-data", "data"}, step.SensitiveOutputs)
	observedBinary := step.ObservedOutputs["binary-data"].(map[string]any)
	decoded, err := base64.StdEncoding.DecodeString(observedBinary["bytes"].(string))
	require.NoError(t, err)
	require.Equal(t, []byte(mixed), decoded)

	assertMasked(factory.success(t, "apply", "plan.ubp"))
	api.expect(t, secretLookupGET)
	snapshot := factory.snapshot(t, saved)
	entry := snapshot.Find(secretLookupAddress)
	require.NotNil(t, entry)
	require.Equal(t, wantInputs, entry.Inputs["binary-data"])
	require.Equal(t, wantObserved, entry.Outputs)
	require.Equal(t, []string{"binary-data"}, entry.SensitiveInputs)
	require.Equal(t, []string{"binary-data", "data"}, entry.SensitiveOutputs)
	require.Equal(t, map[string]any{
		"metadata": wantMetadata, "text": wantText, "binary": wantBinary,
		"index": token, "dotted": token, "whole": wantObserved,
		"type": "example.test/visible-type", "immutable": true,
	}, snapshot.Outputs)

	out := factory.success(t, "output", "-c", config, "--format", "json")
	assertMasked(out)
	var shown struct {
		Outputs json.RawMessage `json:"outputs"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &shown))
	display, err := json.Marshal(map[string]any{
		"metadata": wantMetadata, "type": "example.test/visible-type", "immutable": true,
		"text": "<sensitive>", "binary": "<sensitive>", "index": "<sensitive>",
		"whole": "<sensitive>", "dotted": "<sensitive>",
	})
	require.NoError(t, err)
	require.JSONEq(t, string(display), string(shown.Outputs))
	out = factory.success(t, "state", "show", "-c", config, secretLookupAddress)
	assertMasked(out)
	require.Equal(t, 2, strings.Count(out, "\n  binary-data: <sensitive>"))
	require.Contains(t, out, "\n  data: <sensitive>")
	require.Contains(t, out, "lookup-uid")
	require.Contains(t, out, "example.test/visible-type")
	api.expect(t)

	assertMasked(factory.success(t, "plan", "-c", config, "-o", "plan.ubp"))
	api.expect(t, secretLookupGET)
	api.mu.Lock()
	api.payload["token"], api.payload["bytes"] = []byte(alternateText), []byte(alternateBytes)
	api.mu.Unlock()
	out, err = factory.run(t, "apply", "plan.ubp")
	require.Error(t, err)
	assertMasked(out)
	require.Contains(t, out, "data source data-source.lookup changed since the plan was computed")
	require.Contains(t, out, "\n  binary-data: (sensitive)")
	require.Contains(t, out, "\n  data: (sensitive)")
	api.expect(t, secretLookupGET)
	require.Equal(t, wantObserved, factory.snapshot(t, saved).Find(secretLookupAddress).Outputs)
}

func requireSecretLookupConstraint(t *testing.T, out string, err error, rule int) {
	t.Helper()
	require.Error(t, err, "%s", out)
	require.Contains(t, out, fmt.Sprintf("data-source.lookup: schema: constraints[%d] (predicate)",
		rule))
}

func TestCompiledSecretLookupNativeInputs(t *testing.T) {
	factory := newSecretLookupFactory(t, filepath.Join(secretLookupFixture, "native"))
	api := &secretLookupTestAPI{seedUID: "lookup", payload: map[string][]byte{
		"bytes": {0, 255}, "empty": {}, "text": []byte("synthetic valid text"),
	}}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	plan := func(t *testing.T, stack string, inputs map[string]any) (string, error) {
		t.Helper()
		inputs["server"] = server.URL
		config := factory.configure(t, stack, inputs)
		return factory.run(t, "plan", "-c", config, "-o", "plan.ubp")
	}
	wantText := map[string]any{"empty": "", "text": "synthetic valid text"}
	wantBinary := map[string]any{"bytes": "AP8=", "absent": ""}
	const seed = "resource.seed"
	dns := strings.Join([]string{
		strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63),
		strings.Repeat("d", 61),
	}, ".")
	key := strings.Repeat("k", 63)

	t.Run("evaluated inputs on a steady stack", func(t *testing.T) {
		out, err := plan(t, "steady", map[string]any{})
		require.NoError(t, err, "%s", out)
		api.expect(t)
		factory.success(t, "apply", "plan.ubp")
		api.expect(t, secretSeedPOST, secretSeedGET, secretLookupGET)
		for _, tc := range []struct {
			name, input string
			value       any
			rule        int
		}{
			{"name syntax", "name", "Bad_Name", 0},
			{"name length", "name", strings.Repeat("x", 254), 0},
			{"prefix", "prefix", "Upper-", 1},
			{"label key", "labels", map[string]any{"UPPER.test/Key": ""}, 2},
			{"label key length", "labels", map[string]any{key + "k": ""}, 2},
			{"label DNS length", "long-label", dns + "d/key", 2},
			{"label value", "labels", map[string]any{"valid": "bad value"}, 3},
			{"label value length", "labels", map[string]any{"valid": key + "k"}, 3},
			{"annotation key", "annotations", map[string]any{"bad key": ""}, 4},
			{"annotation key length", "annotations", map[string]any{key + "k": ""}, 4},
			{"annotation DNS length", "long-annotation", strings.ToUpper(dns) + "D/k", 4},
		} {
			t.Run(tc.name, func(t *testing.T) {
				out, err := plan(t, "steady", map[string]any{tc.input: tc.value})
				requireSecretLookupConstraint(t, out, err, tc.rule)
				api.expect(t, secretSeedGET)
			})
		}

		out, err = plan(t, "steady", map[string]any{
			"prefix":           strings.Repeat("p", 62) + "A-",
			"long-label":       dns + "/" + key,
			"long-label-value": key,
			"long-annotation":  strings.ToUpper(dns) + "/" + key,
		})
		require.NoError(t, err, "%s", out)
		api.expect(t, secretSeedGET, secretLookupGET)
		saved := factory.saved(t)
		require.Equal(t, runtime.DecisionNoOp, secretLookupStep(t, saved, seed).Decision)
		step := secretLookupStep(t, saved, secretLookupAddress)
		require.Equal(t, runtime.DecisionRead, step.Decision)
		require.Empty(t, step.UnresolvedInputs)
		require.Equal(t, wantText, step.ObservedOutputs["data"])
		require.Equal(t, wantBinary, step.ObservedOutputs["binary-data"])
		// Change the saved root input while preserving the steady plan's steps.
		saved.Inputs["prefix"] = ""
		body, err := json.Marshal(saved)
		require.NoError(t, err)
		sealed, err := state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(factory.work, "invalid.ubp"), sealed, 0o600))
		out, err = factory.run(t, "apply", "invalid.ubp")
		requireSecretLookupConstraint(t, out, err, 1)
		api.expect(t)

		factory.success(t, "plan", "-c", "steady.ub", "-o", "plan.ubp")
		api.expect(t, secretSeedGET, secretLookupGET)
		factory.success(t, "apply", "plan.ubp")
		api.expect(t, secretLookupGET)
	})

	t.Run("known rule with a pending selector", func(t *testing.T) {
		out, err := plan(t, "partial", map[string]any{
			"defer-selector": true, "labels": map[string]any{"UPPER.test/Key": ""},
		})
		requireSecretLookupConstraint(t, out, err, 2)
		require.Contains(t, out, "metadata.labels['UPPER.test/Key']")
		api.expect(t)
	})

	for _, tc := range []struct {
		stack string
		uid   types.UID
	}{
		{"resolved-valid", "lookup"}, {"resolved-invalid", "Bad_Name"},
	} {
		t.Run(tc.stack, func(t *testing.T) {
			api.mu.Lock()
			api.seedUID = tc.uid
			api.mu.Unlock()
			out, err := plan(t, tc.stack, map[string]any{"defer-name": true, "defer-selector": true})
			require.NoError(t, err, "%s", out)
			api.expect(t)
			saved := factory.saved(t)
			step := secretLookupStep(t, saved, secretLookupAddress)
			require.Equal(t, runtime.DecisionRead, step.Decision)
			require.Nil(t, step.ObservedOutputs)
			require.Equal(t, map[string][]string{
				"metadata":    {"resource.seed.object.metadata.uid"},
				"binary-data": {"resource.seed.object.metadata.uid"},
			}, step.UnresolvedInputs)
			out, err = factory.run(t, "apply", "plan.ubp")
			if tc.uid == "Bad_Name" {
				requireSecretLookupConstraint(t, out, err, 0)
				api.expect(t, secretSeedPOST, secretSeedGET)
				return
			}
			require.NoError(t, err, "%s", out)
			api.expect(t, secretSeedPOST, secretSeedGET, secretLookupGET)
			entry := factory.snapshot(t, saved).Find(secretLookupAddress)
			require.NotNil(t, entry)
			require.Equal(t, wantText, entry.Outputs["data"])
			require.Equal(t, map[string]any{"bytes": "AP8="}, entry.Outputs["binary-data"])
		})
	}
}

func TestCompiledSecretLookupScenario(t *testing.T) {
	fixture := "../../tests/integration/scenarios/core-v1/testdata/ub/secret-lookup"
	factory := newSecretLookupFactory(t, fixture)
	body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(factory.work, "config.ub"), body, 0o600))
	factory.success(t, "pin", "-c", "config.ub")
	factory.success(t, "validate", "-c", "config.ub")
}
