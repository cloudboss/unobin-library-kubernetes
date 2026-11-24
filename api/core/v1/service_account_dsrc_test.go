package v1

import (
	"bytes"
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
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const serviceAccountLookupFixture = "testdata/ub/service-account-lookup"
const serviceAccountLookupAddress = "data-source.lookup"
const serviceAccountAnchorAddress = "resource.anchor"
const serviceAccountAnchorCollection = "/api/v1/namespaces/default/configmaps"
const serviceAccountAnchorGET = "GET " + serviceAccountAnchorCollection + "/anchor"
const serviceAccountAnchorPOST = "POST " + serviceAccountAnchorCollection
const serviceAccountLookupGET = "GET /api/v1/namespaces/default/serviceaccounts/lookup"

func serviceAccountCompileOptions(t *testing.T, factory string) (compile.Options, *bytes.Buffer) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	work := coreCompileDirectory(t)
	factory, err = filepath.Abs(factory)
	require.NoError(t, err)
	log := new(bytes.Buffer)
	return compile.Options{
		FactoryPath: factory, OutDir: work, StackName: "service-account-lookup", Version: "v0.0.0",
		CLIVersion: "v0.13.0", GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Stdout: log, Stderr: log,
	}, log
}

func TestCompiledServiceAccountLookupLiterals(t *testing.T) {
	for _, tc := range []struct {
		fixture     string
		diagnostics []string
	}{
		{fixture: "valid"},
		{"rules", []string{
			"data-source.empty-name: constraints[0]", "data-source.bad-name: constraints[0]",
			"data-source.long-name: constraints[0]", "data-source.label-key: constraints[1]",
			"data-source.label-suffix: constraints[1]", "data-source.label-value: constraints[2]",
			"data-source.long-value: constraints[2]", "data-source.annotation-key: constraints[3]",
			"data-source.annotation-suffix: constraints[3]",
		}},
		{"metadata", []string{
			`missing required input "metadata"`, "type mismatch: expected object", "got null",
		}},
		{"writable", []string{
			`unknown field "generate-name"`, `unknown field "secrets"`,
			`unknown field "image-pull-secrets"`, `unknown field "automount-service-account-token"`,
			`unknown field "default-secret-name"`,
		}},
		{"unavailable-outputs", []string{
			`unknown field "generate-name"`, `unknown field "id"`, `unknown field "data"`,
		}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			opts, log := serviceAccountCompileOptions(t,
				filepath.Join(serviceAccountLookupFixture, tc.fixture, "factory.ub"))
			_, err := compile.RunResult(opts)
			if tc.diagnostics == nil {
				require.NoError(t, err, "%s", log)
				return
			}
			require.Error(t, err, "%s", log)
			for _, diagnostic := range tc.diagnostics {
				require.Contains(t, err.Error()+log.String(), diagnostic)
			}
		})
	}
}

func TestCompiledServiceAccountLookupLibraryAPI(t *testing.T) {
	opts, log := serviceAccountCompileOptions(t,
		filepath.Join(serviceAccountLookupFixture, "rules", "factory.ub"))
	opts.LibraryAPIDescriptor = &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	_, err := compile.RunResult(opts)
	var apiErr *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &apiErr, "%s", log)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, apiErr.Required)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 0}, apiErr.Implemented)
	require.NotContains(t, log.String()+err.Error(), "constraints[")
}

type serviceAccountTestAPI struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []string
	anchor *corev1.ConfigMap
	uid    types.UID
}

func (a *serviceAccountTestAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	call := r.Method + " " + r.URL.RequestURI()
	a.calls = append(a.calls, call)
	w.Header().Set("Content-Type", "application/json")
	var response any
	switch call {
	case serviceAccountAnchorPOST:
		a.anchor = &corev1.ConfigMap{}
		if err := json.NewDecoder(r.Body).Decode(a.anchor); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a.anchor.UID = a.uid
		response = a.anchor
	case serviceAccountAnchorGET:
		if a.anchor != nil {
			response = a.anchor
		}
	case serviceAccountLookupGET:
		response = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: "lookup", GenerateName: "generated-", Namespace: "default",
				UID: "synthetic-lookup-uid", ResourceVersion: "1",
				Labels: map[string]string{"remote": "label"},
			},
			Secrets: []corev1.ObjectReference{{Name: "synthetic-secret"}},
		}
	default:
		http.Error(w, "unexpected request "+call, http.StatusBadRequest)
		return
	}
	if response == nil {
		w.WriteHeader(http.StatusNotFound)
		response = &metav1.Status{
			Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound,
			Code: http.StatusNotFound,
		}
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		a.t.Error(err)
	}
}

func (a *serviceAccountTestAPI) expect(t *testing.T, want ...string) {
	t.Helper()
	require.Equal(t, want, a.takeCalls())
}

func (a *serviceAccountTestAPI) takeCalls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func serviceAccountNativeTestCases() []struct {
	name, input string
	value       any
	rule        int
} {
	prefix := strings.Join([]string{
		strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63),
		strings.Repeat("d", 62),
	}, ".")
	suffix := strings.Repeat("k", 64)
	return []struct {
		name, input string
		value       any
		rule        int
	}{
		{"name syntax", "name", "Bad_Name", 0},
		{"name length", "name", prefix, 0},
		{"label syntax", "labels", map[string]any{"UPPER.test/Key": ""}, 1},
		{"label prefix length", "long-label", prefix + "/Key", 1},
		{"label suffix length", "labels", map[string]any{suffix: ""}, 1},
		{"label value syntax", "labels", map[string]any{"app": "bad value"}, 2},
		{"label value length", "labels", map[string]any{"app": suffix}, 2},
		{"annotation syntax", "annotations", map[string]any{"bad key": "value"}, 3},
		{"annotation prefix length", "annotations", map[string]any{prefix + "/Key": "value"}, 3},
		{"annotation suffix length", "annotations", map[string]any{suffix: "value"}, 3},
	}
}

type serviceAccountFactory struct {
	work, binary, server string
	template             []byte
	api                  *serviceAccountTestAPI
}

func startServiceAccountFactory(t *testing.T, fixture string) serviceAccountFactory {
	t.Helper()
	opts, log := serviceAccountCompileOptions(t,
		filepath.Join(serviceAccountLookupFixture, fixture, "factory.ub"))
	opts.Build = true
	result, err := compile.RunResult(opts)
	require.NoError(t, err, "%s", log)
	t.Setenv("KUBECONFIG", filepath.Join(opts.OutDir, "missing-kubeconfig"))
	api := &serviceAccountTestAPI{t: t, uid: "lookup"}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	template, err := os.ReadFile(filepath.Join(serviceAccountLookupFixture, "runtime/config.ub"))
	require.NoError(t, err)

	return serviceAccountFactory{
		work: opts.OutDir, binary: result.BinaryPath, server: server.URL, template: template, api: api,
	}
}

func (f serviceAccountFactory) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir = f.work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f serviceAccountFactory) success(t *testing.T, args ...string) {
	t.Helper()
	out, err := f.run(t, args...)
	require.NoError(t, err, "%s", out)
}

func (f serviceAccountFactory) plan(
	t *testing.T, stack string, inputs map[string]any,
) (string, error) {
	t.Helper()
	if inputs == nil {
		inputs = map[string]any{}
	}
	inputs["server"] = f.server
	encoded, err := ub.Marshal(inputs)
	require.NoError(t, err)
	body := bytes.Replace(f.template, []byte("'SERVICE_ACCOUNT_LOOKUP_INPUTS'"), encoded, 1)
	require.NoError(t, os.WriteFile(filepath.Join(f.work, stack+".ub"), body, 0o600))
	f.success(t, "pin", "-c", stack+".ub")
	return f.run(t, "plan", "-c", stack+".ub", "-o", "plan.ubp")
}

func (f serviceAccountFactory) savedPlan(
	t *testing.T,
) (*runtime.PlanFile, map[string]runtime.PlanStep) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.work, "plan.ubp"))
	require.NoError(t, err)
	file, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	steps := make(map[string]runtime.PlanStep, len(file.Steps))
	for _, step := range file.Steps {
		steps[step.Address] = step
	}
	return file, steps
}

func rejectServiceAccountInput(t *testing.T, out string, err error, rule int) {
	t.Helper()
	require.Error(t, err, "%s", out)
	require.Contains(t, out, fmt.Sprintf("data-source.lookup: schema: constraints[%d]", rule))
}

func TestCompiledServiceAccountLookupPlans(t *testing.T) {
	f := startServiceAccountFactory(t, "runtime")
	api := f.api

	t.Run("evaluated rules", func(t *testing.T) {
		out, err := f.plan(t, "initial-invalid", map[string]any{"name": "Bad_Name"})
		rejectServiceAccountInput(t, out, err, 0)
		api.expect(t)
		for _, inputs := range []map[string]any{nil, {"name": nil}} {
			out, err := f.plan(t, "empty-name", inputs)
			require.Error(t, err, "%s", out)
			require.Contains(t, out, "resource name may not be empty")
			api.expect(t)
		}
		out, err = f.plan(t, "steady", map[string]any{
			"name":        "lookup",
			"namespace":   nil,
			"annotations": map[string]any{"İ.COM/Key": "arbitrary", "bad annotation": nil},
			"labels":      map[string]any{"example.com/Key": "", "bad label": nil},
		})
		require.NoError(t, err, "%s", out)
		api.expect(t, serviceAccountLookupGET, serviceAccountLookupGET)
		f.success(t, "apply", "plan.ubp")
		require.ElementsMatch(t, []string{
			serviceAccountAnchorPOST, serviceAccountAnchorGET,
			serviceAccountLookupGET, serviceAccountLookupGET,
		}, api.takeCalls())
		for _, tc := range serviceAccountNativeTestCases() {
			t.Run(tc.name, func(t *testing.T) {
				inputs := map[string]any{"name": "lookup"}
				inputs[tc.input] = tc.value
				out, err := f.plan(t, "steady", inputs)
				rejectServiceAccountInput(t, out, err, tc.rule)
				api.expect(t)
			})
		}

		out, err = f.plan(t, "steady", map[string]any{"name": "lookup"})
		require.NoError(t, err, "%s", out)
		api.expect(t, serviceAccountLookupGET, serviceAccountLookupGET, serviceAccountAnchorGET)
		file, steps := f.savedPlan(t)
		require.Equal(t, runtime.DecisionNoOp, steps[serviceAccountAnchorAddress].Decision)
		lookup := steps[serviceAccountLookupAddress]
		require.Equal(t, runtime.DecisionRead, lookup.Decision)
		require.Empty(t, lookup.SensitiveInputs)
		require.Empty(t, lookup.SensitiveOutputs)
		require.Equal(t, map[string]any{
			"metadata": map[string]any{
				"name": "lookup", "namespace": "default", "generation": int64(0),
				"resource-version": "1", "uid": "synthetic-lookup-uid",
				"labels": map[string]any{"remote": "label"}, "annotations": map[string]any{},
			},
			"automount-service-account-token": false, "default-secret-name": "",
			"secrets":            []any{map[string]any{"name": "synthetic-secret"}},
			"image-pull-secrets": []any{},
		}, lookup.ObservedOutputs)
		file.Inputs["labels"] = map[string]any{"bad key": ""}
		body, err := json.Marshal(file)
		require.NoError(t, err)
		sealed, err := state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(f.work, "invalid.ubp"), sealed, 0o600))
		out, err = f.run(t, "apply", "invalid.ubp")
		rejectServiceAccountInput(t, out, err, 1)
		api.expect(t)

		f.success(t, "plan", "-c", "steady.ub", "-o", "plan.ubp")
		api.expect(t, serviceAccountLookupGET, serviceAccountLookupGET, serviceAccountAnchorGET)
		f.success(t, "apply", "plan.ubp")
		api.expect(t, serviceAccountLookupGET, serviceAccountLookupGET)
	})
}

func TestCompiledServiceAccountLookupDeferredInputs(t *testing.T) {
	f := startServiceAccountFactory(t, "deferred")
	api := f.api
	t.Run("known invalid key with pending name", func(t *testing.T) {
		out, err := f.plan(t, "partial", map[string]any{
			"defer-name": true, "labels": map[string]any{"UPPER.test/Key": ""},
		})
		rejectServiceAccountInput(t, out, err, 1)
		require.Contains(t, out, "metadata.labels['UPPER.test/Key']")
		api.expect(t)
	})

	for _, tc := range []struct {
		name     string
		uid      types.UID
		deferKey string
		wantRule int
	}{
		{"valid-deferred", "lookup", "defer-name", -1},
		{"invalid-deferred-name", "Bad_Name", "defer-name", 0},
		{"invalid-deferred-map", "UPPER.test/Key", "defer-labels", 1},
		{"valid-deferred-map", "example.com/Key", "defer-labels", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api.mu.Lock()
			api.uid = tc.uid
			api.mu.Unlock()
			out, err := f.plan(t, tc.name, map[string]any{
				tc.deferKey: true,
			})
			require.NoError(t, err, "%s", out)
			api.expect(t)
			_, steps := f.savedPlan(t)
			lookup := steps[serviceAccountLookupAddress]
			require.Equal(t, runtime.DecisionRead, lookup.Decision)
			require.Nil(t, lookup.ObservedOutputs)
			require.Equal(t, map[string][]string{
				"metadata": {"resource.anchor.object.metadata.uid"},
			}, lookup.UnresolvedInputs)
			out, err = f.run(t, "apply", "plan.ubp")
			if tc.wantRule >= 0 {
				rejectServiceAccountInput(t, out, err, tc.wantRule)
				api.expect(t, serviceAccountAnchorPOST, serviceAccountAnchorGET)
				return
			}
			require.NoError(t, err, "%s", out)
			api.expect(t, serviceAccountAnchorPOST, serviceAccountAnchorGET,
				serviceAccountLookupGET, serviceAccountLookupGET)
		})
	}
}
