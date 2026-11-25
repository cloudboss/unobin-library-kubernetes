package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const tokenRequestFixture = "testdata/ub/token-request"
const tokenRequestAddress = "resource.request"

type tokenRequestFactory struct {
	binary string
	work   string
}

func compileTokenRequest(t *testing.T) tokenRequestFactory {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	work := t.TempDir()
	factory, err := filepath.Abs(filepath.Join(tokenRequestFixture, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work, Build: true, StackName: "token-request",
		Version: "v0.0.0", CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	t.Setenv("KUBECONFIG", filepath.Join(work, "missing"))
	return tokenRequestFactory{binary: result.BinaryPath, work: work}
}

func (f tokenRequestFactory) isolated(t *testing.T) tokenRequestFactory {
	t.Helper()
	dir, err := os.MkdirTemp(f.work, "state-")
	require.NoError(t, err)
	f.work = dir
	return f
}

func (f tokenRequestFactory) command(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir = f.work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f tokenRequestFactory) success(t *testing.T, args ...string) string {
	t.Helper()
	out, err := f.command(t, args...)
	require.NoError(t, err, "%s", out)
	return out
}

func (f tokenRequestFactory) plan(
	t *testing.T, inputs map[string]any, flags ...string,
) (string, error) {
	t.Helper()
	template, err := os.ReadFile(filepath.Join(tokenRequestFixture, "config.ub"))
	require.NoError(t, err)
	encoded, err := ub.Marshal(inputs)
	require.NoError(t, err)
	config := bytes.Replace(template, []byte("'TOKEN_REQUEST_INPUTS'"), encoded, 1)
	require.NoError(t, os.WriteFile(filepath.Join(f.work, "config.ub"), config, 0o600))
	f.success(t, "pin", "-c", "config.ub")
	args := append([]string{"plan", "-c", "config.ub", "-o", "plan.ubp"}, flags...)
	return f.command(t, args...)
}

func (f tokenRequestFactory) readPlan(t *testing.T) *runtime.PlanFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
}

func (f tokenRequestFactory) ready(
	t *testing.T, inputs map[string]any, decision runtime.Decision, flags ...string,
) *runtime.PlanStep {
	t.Helper()
	out, err := f.plan(t, inputs, flags...)
	require.NoError(t, err, "%s", out)
	require.NotContains(t, out, "synthetic-issued-")
	for _, step := range f.readPlan(t).Steps {
		if step.Address == tokenRequestAddress {
			require.Equal(t, decision, step.Decision, "%s", out)
			return &step
		}
	}
	t.Fatal("missing TokenRequest plan step")
	return nil
}

func (f tokenRequestFactory) apply(t *testing.T) {
	t.Helper()
	require.NotContains(t, f.success(t, "apply", "plan.ubp"), "synthetic-issued-")
}

func (f tokenRequestFactory) store(t *testing.T) *local.Store {
	t.Helper()
	plan := f.readPlan(t)
	store, err := local.NewStore(
		filepath.Join(f.work, "state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	require.NoError(t, err)
	return store
}

func tokenRequestSnapshot(t *testing.T, store *local.Store) *state.Snapshot {
	t.Helper()
	snapshot, err := store.Current()
	require.NoError(t, err)
	return snapshot
}

func persistTokenRequestState(t *testing.T, store *local.Store, snapshot *state.Snapshot) {
	t.Helper()
	revision, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(revision))
}

type tokenRequestAPI struct {
	server *httptest.Server
	calls  chan tokenCall
}

func newTokenRequestAPI(t *testing.T) tokenRequestAPI {
	t.Helper()
	calls := make(chan tokenCall, 20)
	issued := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := tokenCall{method: r.Method, path: r.URL.Path}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/token") {
			calls <- call
			http.Error(w, "unexpected Kubernetes call", http.StatusBadRequest)
			return
		}
		requireJSON := r.Header.Get("Content-Type")
		assert.Equal(t, "application/json", requireJSON)
		if err := json.NewDecoder(r.Body).Decode(&call.body); !assert.NoError(t, err) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		calls <- call
		spec := call.body.Spec
		if spec.Audiences == nil {
			spec.Audiences = []string{"api-default"}
		}
		if spec.ExpirationSeconds == nil || *spec.ExpirationSeconds > 1200 {
			spec.ExpirationSeconds = new(int64(1200))
		}
		issued++
		response := authenticationv1.TokenRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "ignored", UID: "ignored"},
			Spec:       spec,
			Status: authenticationv1.TokenRequestStatus{
				Token: fmt.Sprintf("synthetic-issued-%d", issued), ExpirationTimestamp: metav1.Now(),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)
	return tokenRequestAPI{server: server, calls: calls}
}

func (a tokenRequestAPI) expect(t *testing.T, want ...tokenCall) {
	t.Helper()
	require.Equal(t, want, drainTokenCalls(a.calls))
}

func expectedTokenPost(
	namespace, name string, spec authenticationv1.TokenRequestSpec,
) tokenCall {
	return tokenCall{
		method: http.MethodPost,
		path:   "/api/v1/namespaces/" + namespace + "/serviceaccounts/" + name + "/token",
		body: authenticationv1.TokenRequest{
			TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenRequest"},
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: namespace,
			},
			Spec: spec,
		},
	}
}

func TestTokenRequestSavedPlans(t *testing.T) {
	f := compileTokenRequest(t)
	t.Run("local refresh update and destroy", func(t *testing.T) {
		f := f.isolated(t)
		api := newTokenRequestAPI(t)
		inputs := map[string]any{
			"server": api.server.URL, "namespace": nil,
			"labels":      map[string]string{"old": "remove", "phase": "first"},
			"annotations": map[string]string{"Example.COM/Key": "first"},
		}
		f.ready(t, inputs, runtime.DecisionCreate)
		f.apply(t)
		post := expectedTokenPost("default", "issuer", authenticationv1.TokenRequestSpec{})
		post.body.Labels = map[string]string{"old": "remove", "phase": "first"}
		post.body.Annotations = map[string]string{"Example.COM/Key": "first"}
		api.expect(t, post)
		api.server.Close()

		firstSpec := map[string]any{
			"audiences": []any{"api-default"}, "expiration-seconds": int64(1200),
			"bound-object-ref": nil,
		}
		store := f.store(t)
		firstState := tokenRequestSnapshot(t, store)
		first := firstState.Find(tokenRequestAddress)
		require.NotNil(t, first)
		require.Equal(t, map[string]any{
			"server": api.server.URL, "api-version": "authentication.k8s.io/v1", "kind": "TokenRequest",
			"metadata": map[string]any{
				"name": "issuer", "namespace": "default",
				"labels":      map[string]any{"old": "remove", "phase": "first"},
				"annotations": map[string]any{"Example.COM/Key": "first"},
			},
			"spec": firstSpec, "token": "synthetic-issued-1",
		}, first.Outputs)
		require.Equal(t, []string{"token"}, first.SensitiveOutputs)
		require.Equal(t, map[string]any{
			"issued-token": "synthetic-issued-1", "issued-spec": firstSpec,
		}, firstState.Outputs)
		out := f.success(t, "state", "show", "-c", "config.ub", tokenRequestAddress)
		require.Contains(t, out, "token: <sensitive>")
		require.NotContains(t, out, "synthetic-issued-1")
		out = f.success(t, "output", "-c", "config.ub", "--format", "json")
		require.Contains(t, out, "<sensitive>")
		require.NotContains(t, out, "synthetic-issued-1")

		step := f.ready(t, inputs, runtime.DecisionNoOp)
		require.Equal(t, first.Outputs, step.ObservedOutputs)
		require.Equal(t, []string{"token"}, step.SensitiveOutputs)
		out = f.success(t, "plan", "-c", "config.ub", "--format", "json")
		require.Contains(t, out, "plan-summary")
		require.NotContains(t, out, "synthetic-issued-1")
		f.apply(t)
		require.Equal(t, first, tokenRequestSnapshot(t, store).Find(tokenRequestAddress))

		inputs["labels"] = map[string]string{"phase": "second", "new": ""}
		inputs["annotations"] = map[string]string{}
		f.ready(t, inputs, runtime.DecisionUpdate)
		f.apply(t)
		updated := tokenRequestSnapshot(t, store).Find(tokenRequestAddress)
		require.Equal(t, first.Outputs["token"], updated.Outputs["token"])
		require.Equal(t, firstSpec, updated.Outputs["spec"])
		require.Equal(t, map[string]any{
			"name": "issuer", "namespace": "default",
			"labels": map[string]any{"phase": "second", "new": ""}, "annotations": map[string]any{},
		}, updated.Outputs["metadata"])
		f.ready(t, inputs, runtime.DecisionNoOp)
		f.apply(t)

		historical := tokenRequestSnapshot(t, store)
		legacySpec := map[string]any{"expiration-seconds": int64(599)}
		historical.Find(tokenRequestAddress).Inputs["spec"] = legacySpec
		persistTokenRequestState(t, store, historical)
		inputs["spec"] = legacySpec
		out, err := f.plan(t, inputs)
		requireTokenRequestConstraint(t, out, err, "constraints[1]", "spec.expiration-seconds")
		f.ready(t, inputs, runtime.DecisionDestroy, "--destroy")
		f.apply(t)
		require.Nil(t, tokenRequestSnapshot(t, store).Find(tokenRequestAddress))
		api.expect(t)
	})

	t.Run("empty namespace is explicit", func(t *testing.T) {
		f := f.isolated(t)
		api := newTokenRequestAPI(t)
		inputs := map[string]any{"server": api.server.URL, "namespace": ""}
		f.ready(t, inputs, runtime.DecisionCreate)
		out, err := f.command(t, "apply", "plan.ubp")
		require.Error(t, err)
		require.Contains(t, out, "an empty namespace may not be set during creation")
		api.expect(t)
	})

	t.Run("effective replacements", func(t *testing.T) {
		testTokenRequestReplacements(t, f)
	})
	t.Run("target selectors", func(t *testing.T) {
		testTokenRequestTarget(t, f)
	})
	t.Run("compiled constraints", func(t *testing.T) {
		testTokenRequestRuntimeConstraints(t, f)
	})
}

func testTokenRequestReplacements(t *testing.T, f tokenRequestFactory) {
	t.Helper()
	binding := map[string]any{"api-version": "v1", "kind": "Secret", "name": "example", "uid": "uid"}
	for _, tc := range []struct {
		name      string
		before    map[string]any
		after     map[string]any
		first     authenticationv1.TokenRequestSpec
		second    authenticationv1.TokenRequestSpec
		nameAfter string
		namespace string
		reason    string
	}{
		{
			name: "reordered audiences", reason: "spec",
			before: map[string]any{"spec": map[string]any{"audiences": []string{"a", "b", "a", ""}}},
			after:  map[string]any{"spec": map[string]any{"audiences": []string{"b", "a", "a", ""}}},
			first:  authenticationv1.TokenRequestSpec{Audiences: []string{"a", "b", "a", ""}},
			second: authenticationv1.TokenRequestSpec{Audiences: []string{"b", "a", "a", ""}},
		},
		{
			name: "explicit empty audiences", reason: "spec",
			before: map[string]any{"spec": map[string]any{"audiences": []string{"existing"}}},
			after:  map[string]any{"spec": map[string]any{"audiences": []string{}}},
			first:  authenticationv1.TokenRequestSpec{Audiences: []string{"existing"}},
		},
		{
			name: "changed expiration", reason: "spec",
			before: map[string]any{"spec": map[string]any{"expiration-seconds": 3600}},
			after:  map[string]any{"spec": map[string]any{"expiration-seconds": 1800}},
			first:  authenticationv1.TokenRequestSpec{ExpirationSeconds: new(int64(3600))},
			second: authenticationv1.TokenRequestSpec{ExpirationSeconds: new(int64(1800))},
		},
		{
			name: "removed binding member", reason: "spec",
			before: map[string]any{"spec": map[string]any{"bound-object-ref": binding}},
			after: map[string]any{"spec": map[string]any{
				"bound-object-ref": map[string]any{"api-version": "v1", "kind": "Secret", "uid": "uid"},
			}},
			first: authenticationv1.TokenRequestSpec{BoundObjectRef: &authenticationv1.BoundObjectReference{
				APIVersion: "v1", Kind: "Secret", Name: "example", UID: "uid",
			}},
			second: authenticationv1.TokenRequestSpec{BoundObjectRef: &authenticationv1.BoundObjectReference{
				APIVersion: "v1", Kind: "Secret", UID: "uid",
			}},
		},
		{
			name: "omitted computed fields",
			before: map[string]any{"spec": map[string]any{
				"audiences": []string{"original"}, "expiration-seconds": 3600, "bound-object-ref": binding,
			}},
			after: map[string]any{"spec": map[string]any{}},
			first: authenticationv1.TokenRequestSpec{
				Audiences: []string{"original"}, ExpirationSeconds: new(int64(3600)),
				BoundObjectRef: &authenticationv1.BoundObjectReference{
					APIVersion: "v1", Kind: "Secret", Name: "example", UID: "uid",
				},
			},
		},
		{
			name: "adopt computed results",
			after: map[string]any{"spec": map[string]any{
				"audiences": []string{"api-default"}, "expiration-seconds": 1200,
			}},
		},
		{
			name:   "keep duration adjusted by server",
			before: map[string]any{"spec": map[string]any{"expiration-seconds": 3600}},
			after:  map[string]any{"spec": map[string]any{"expiration-seconds": 3600}},
			first:  authenticationv1.TokenRequestSpec{ExpirationSeconds: new(int64(3600))},
		},
		{
			name: "different account", reason: "metadata", nameAfter: "other",
			after: map[string]any{"name": "other"},
		},
		{
			name: "different namespace", reason: "metadata", namespace: "other",
			after: map[string]any{"namespace": "other"},
		},
		{
			name: "fresh defaults on identity change", reason: "metadata", nameAfter: "other",
			before: map[string]any{"spec": map[string]any{"audiences": []string{"first"}}},
			first:  authenticationv1.TokenRequestSpec{Audiences: []string{"first"}},
			after:  map[string]any{"name": "other"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := f.isolated(t)
			api := newTokenRequestAPI(t)
			initial := map[string]any{"server": api.server.URL}
			maps.Copy(initial, tc.before)
			desired := map[string]any{"server": api.server.URL}
			maps.Copy(desired, tc.after)
			f.ready(t, initial, runtime.DecisionCreate)
			f.apply(t)
			api.expect(t, expectedTokenPost("default", "issuer", tc.first))
			store := f.store(t)
			first := tokenRequestSnapshot(t, store).Find(tokenRequestAddress)

			if tc.reason == "" {
				f.ready(t, desired, runtime.DecisionNoOp)
				f.apply(t)
				api.expect(t)
				require.Equal(t, first.Outputs,
					tokenRequestSnapshot(t, store).Find(tokenRequestAddress).Outputs)
				return
			}
			step := f.ready(t, desired, runtime.DecisionReplace)
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
			api.expect(t)
			f.apply(t)
			namespace, name := tc.namespace, tc.nameAfter
			if namespace == "" {
				namespace = "default"
			}
			if name == "" {
				name = "issuer"
			}
			api.expect(t, expectedTokenPost(namespace, name, tc.second))
			require.Equal(t, "synthetic-issued-2",
				tokenRequestSnapshot(t, store).Find(tokenRequestAddress).Outputs["token"])
			f.ready(t, desired, runtime.DecisionNoOp)
			api.expect(t)
		})
	}
}

func testTokenRequestTarget(t *testing.T, f tokenRequestFactory) {
	t.Helper()
	for _, selector := range []string{"server", "kubeconfig", "context"} {
		t.Run(selector, func(t *testing.T) {
			f := f.isolated(t)
			oldAPI, newAPI := newTokenRequestAPI(t), newTokenRequestAPI(t)
			config := func(old, next string) string {
				path := filepath.Join(f.work, next)
				require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
					CurrentContext: "old",
					Clusters: map[string]*clientcmdapi.Cluster{
						"old": {Server: old}, "new": {Server: newAPI.server.URL},
					},
					Contexts: map[string]*clientcmdapi.Context{
						"old": {Cluster: "old"}, "new": {Cluster: "new"},
					},
				}, path))
				return path
			}
			initial, desired := map[string]any{}, map[string]any{}
			reason := selector
			switch selector {
			case "server":
				initial["server"], desired["server"] = oldAPI.server.URL, newAPI.server.URL
			case "kubeconfig":
				initial["kubeconfig"] = config(oldAPI.server.URL, "old")
				desired["kubeconfig"] = config(newAPI.server.URL, "new")
			case "context":
				path := config(oldAPI.server.URL, "contexts")
				initial["kubeconfig"], desired["kubeconfig"] = path, path
				initial["context"], desired["context"] = "old", "new"
				reason = "current-context"
			}
			want := expectedTokenPost("default", "issuer", authenticationv1.TokenRequestSpec{})
			f.ready(t, initial, runtime.DecisionCreate)
			f.apply(t)
			oldAPI.expect(t, want)
			oldAPI.server.Close()
			step := f.ready(t, desired, runtime.DecisionReplace)
			require.Equal(t, []string{reason}, step.ReplacementReasons)
			newAPI.expect(t)
			f.apply(t)
			oldAPI.expect(t)
			newAPI.expect(t, want)
			entry := tokenRequestSnapshot(t, f.store(t)).Find(tokenRequestAddress)
			require.Equal(t, newAPI.server.URL, entry.Outputs["server"])
		})
	}
}

func requireTokenRequestConstraint(t *testing.T, out string, err error, rule, field string) {
	t.Helper()
	require.Error(t, err, "%s", out)
	require.Contains(t, out, tokenRequestAddress+": schema: "+rule)
	require.Contains(t, out, field)
	require.NotContains(t, out, "synthetic-issued-")
}

func testTokenRequestRuntimeConstraints(t *testing.T, f tokenRequestFactory) {
	t.Helper()
	f = f.isolated(t)
	api := newTokenRequestAPI(t)
	inputs := func(fields map[string]any) map[string]any {
		result := map[string]any{"server": api.server.URL}
		maps.Copy(result, fields)
		return result
	}
	for _, tt := range []struct {
		name, rule, field string
		inputs            map[string]any
	}{
		{"invalid name", "constraints[0]", "metadata.name", map[string]any{"name": "Bad"}},
		{"invalid expiration", "constraints[1]", "spec.expiration-seconds",
			map[string]any{"spec": map[string]any{"expiration-seconds": 0}}},
		{"invalid expiration with pending metadata", "constraints[1]", "spec.expiration-seconds",
			map[string]any{
				"defer-label": true, "spec": map[string]any{"expiration-seconds": 599},
			}},
		{"invalid label key", "constraints[2]", "metadata.labels",
			map[string]any{"labels": map[string]string{"BAD.COM/key": "valid"}}},
		{"invalid label value", "constraints[3]", "metadata.labels",
			map[string]any{"labels": map[string]string{"valid": "not valid"}}},
		{"invalid annotation key", "constraints[4]", "metadata.annotations",
			map[string]any{"annotations": map[string]string{"/bad": "arbitrary"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := f.plan(t, inputs(tt.inputs))
			requireTokenRequestConstraint(t, out, err, tt.rule, tt.field)
			api.expect(t)
		})
	}

	invalid := inputs(map[string]any{"defer-expiration": true, "seed": "invalid"})
	valid := inputs(map[string]any{"defer-expiration": true, "seed": "valid"})
	step := f.ready(t, invalid, runtime.DecisionCreate)
	require.Equal(t, map[string][]string{"spec": {"action.seed.token"}}, step.UnresolvedInputs)
	out, err := f.command(t, "apply", "plan.ubp")
	requireTokenRequestConstraint(t, out, err, "constraints[1]", "spec.expiration-seconds")
	api.expect(t)
	_, err = f.store(t).Current()
	require.ErrorIs(t, err, state.ErrNoCurrent)

	f.ready(t, valid, runtime.DecisionCreate)
	f.apply(t)
	api.expect(t, expectedTokenPost("default", "issuer", authenticationv1.TokenRequestSpec{
		ExpirationSeconds: new(int64(600)),
	}))
	store := f.store(t)
	first := tokenRequestSnapshot(t, store).Find(tokenRequestAddress)

	step = f.ready(t, invalid, runtime.DecisionReplace)
	require.Equal(t, []string{"spec"}, step.PendingReplacementReasons)
	require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
	out, err = f.command(t, "apply", "plan.ubp")
	requireTokenRequestConstraint(t, out, err, "constraints[1]", "spec.expiration-seconds")
	api.expect(t)
	require.Equal(t, first, tokenRequestSnapshot(t, store).Find(tokenRequestAddress))

	replacement := inputs(map[string]any{"defer-expiration": true, "seed": "replacement"})
	step = f.ready(t, replacement, runtime.DecisionReplace)
	require.Equal(t, []string{"spec"}, step.PendingReplacementReasons)
	f.apply(t)
	api.expect(t, expectedTokenPost("default", "issuer", authenticationv1.TokenRequestSpec{
		ExpirationSeconds: new(int64(900)),
	}))
	first = tokenRequestSnapshot(t, store).Find(tokenRequestAddress)
	require.Equal(t, "synthetic-issued-2", first.Outputs["token"])

	deferredLabel := inputs(map[string]any{
		"defer-label": true, "seed": "two words",
		"spec": map[string]any{"expiration-seconds": 900},
	})
	step = f.ready(t, deferredLabel, runtime.DecisionReplace)
	require.Equal(t, []string{"metadata"}, step.PendingReplacementReasons)
	out, err = f.command(t, "apply", "plan.ubp")
	requireTokenRequestConstraint(t, out, err, "constraints[3]", "metadata.labels")
	api.expect(t)
	require.Equal(t, first, tokenRequestSnapshot(t, store).Find(tokenRequestAddress))

	deferredLabel["seed"] = "ready"
	f.ready(t, deferredLabel, runtime.DecisionReplace)
	f.apply(t)
	api.expect(t)
	updated := tokenRequestSnapshot(t, store).Find(tokenRequestAddress)
	require.Equal(t, first.Outputs["token"], updated.Outputs["token"])
	require.Equal(t, map[string]any{"phase": "ready"},
		updated.Outputs["metadata"].(map[string]any)["labels"])

	concrete := inputs(map[string]any{
		"labels": map[string]string{"phase": "ready"},
		"spec":   map[string]any{"expiration-seconds": 900},
	})
	f.ready(t, concrete, runtime.DecisionNoOp)
	plan := f.readPlan(t)
	badSpec := map[string]any{"expiration-seconds": int64(599)}
	plan.Inputs["spec"] = badSpec
	for _, step := range plan.Steps {
		if step.Address == tokenRequestAddress {
			step.Inputs["spec"] = badSpec
		}
	}
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	body, err = state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.work, "historical.ubp"), body, 0o600))
	out, err = f.command(t, "apply", "historical.ubp")
	requireTokenRequestConstraint(t, out, err, "constraints[1]", "spec.expiration-seconds")
	api.expect(t)
	require.Equal(t, updated, tokenRequestSnapshot(t, store).Find(tokenRequestAddress))

	f.ready(t, concrete, runtime.DecisionNoOp)
	f.apply(t)
	api.expect(t)
}
