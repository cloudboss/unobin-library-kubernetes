package v1

import (
	"bytes"
	"encoding/json"
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
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	serviceAccountLifecycleAnchorCollection = "/api/v1/namespaces/default/configmaps"
	serviceAccountAnchorPath                = serviceAccountLifecycleAnchorCollection + "/anchor"
	serviceAccountLookupPath                = "/api/v1/namespaces/default/serviceaccounts/lookup"
)

func TestServiceAccountLookupSavedPlanLifecycle(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var anchor *corev1.ConfigMap
	account := corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: "lookup", Namespace: "default", UID: "lookup-uid", ResourceVersion: "1",
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		call := r.Method + " " + r.URL.RequestURI()
		calls = append(calls, call)
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch call {
		case "POST " + serviceAccountLifecycleAnchorCollection:
			var object corev1.ConfigMap
			if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			object.UID = "anchor-uid"
			anchor = &object
			response = anchor
		case "GET " + serviceAccountAnchorPath:
			if anchor != nil {
				response = anchor
			}
		case "DELETE " + serviceAccountAnchorPath:
			anchor = nil
			response = &metav1.Status{Status: metav1.StatusSuccess}
		case "GET " + serviceAccountLookupPath:
			response = &account
		default:
			http.Error(w, "unexpected request "+call, http.StatusBadRequest)
			return
		}
		if response == nil {
			w.WriteHeader(http.StatusNotFound)
			response = &metav1.Status{
				Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: 404,
			}
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)
	expectRequests := func(want ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, want, calls)
		calls = nil
	}
	work, binary := buildServiceAccountLookupFixture(t, server.URL)
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = work
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	success := func(args ...string) {
		t.Helper()
		output, err := run(args...)
		require.NoError(t, err, "%s", output)
	}
	openPlan := func(name string) *runtime.PlanFile {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(work, name))
		require.NoError(t, err)
		saved, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
			return encrypters.Noop{}, nil
		})
		require.NoError(t, err)
		return saved
	}

	success("pin", "-c", "config.ub")
	success("plan", "-c", "config.ub", "-o", "pending.ubp")
	expectRequests()
	const lookup = "data-source.lookup"
	steps := map[string]runtime.PlanStep{}
	for _, step := range openPlan("pending.ubp").Steps {
		steps[step.Address] = step
	}
	require.Equal(t, runtime.DecisionRead, steps[lookup].Decision)
	require.Empty(t, steps[lookup].UnresolvedInputs)
	require.Nil(t, steps[lookup].ObservedOutputs)
	require.Equal(t, "library-config.core", steps[lookup].DeferredConfig)

	const anchorGET = "GET " + serviceAccountAnchorPath
	const lookupGET = "GET " + serviceAccountLookupPath
	success("apply", "pending.ubp")
	expectRequests("POST "+serviceAccountLifecycleAnchorCollection, anchorGET, lookupGET, lookupGET)

	success("plan", "-c", "config.ub", "-o", "steady.ubp")
	expectRequests(anchorGET, lookupGET, lookupGET)
	mu.Lock()
	account.ResourceVersion = "2"
	mu.Unlock()
	output, err := run("apply", "steady.ubp")
	require.Error(t, err)
	require.Contains(t, output, "data source data-source.lookup changed since the plan was computed")
	expectRequests(lookupGET, lookupGET)

	success("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
	expectRequests(anchorGET)
	saved := openPlan("destroy.ubp")
	decisions := map[string]runtime.Decision{}
	for _, step := range saved.Steps {
		decisions[step.Address] = step.Decision
	}
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionDestroy, lookup: runtime.DecisionDestroy,
	}, decisions)
	success("apply", "destroy.ubp")
	expectRequests(anchorGET, "DELETE "+serviceAccountAnchorPath)

	store, err := local.NewStore(
		filepath.Join(work, "state"), saved.Factory.Name, saved.Stack, encrypters.Noop{},
	)
	require.NoError(t, err)
	snapshot, err := store.Current()
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
}

func buildServiceAccountLookupFixture(t *testing.T, server string) (string, string) {
	t.Helper()
	fixture := filepath.Join(serviceAccountLookupFixture, "lifecycle")
	opts, log := serviceAccountCompileOptions(t, filepath.Join(fixture, "factory.ub"))
	opts.Build = true
	work := opts.OutDir
	t.Setenv("KUBECONFIG", filepath.Join(work, "missing-kubeconfig"))
	result, err := compile.RunResult(opts)
	require.NoError(t, err, "%s", log.String())
	body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
	require.NoError(t, err)
	body = bytes.ReplaceAll(body, []byte("SERVICE_ACCOUNT_LOOKUP_SERVER"), []byte(server))
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
	return work, result.BinaryPath
}
