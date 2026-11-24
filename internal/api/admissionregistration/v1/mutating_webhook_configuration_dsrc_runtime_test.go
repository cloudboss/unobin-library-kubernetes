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
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	mutatingLookupPath     = "/apis/admissionregistration.k8s.io/v1/mutatingwebhookconfigurations"
	mutatingLookupAddress  = "data-source.lookup"
	mutatingLookupGET      = "GET " + mutatingLookupPath + "/lookup.test"
	mutatingLookupFixture  = "testdata/ub/mutating-webhook-lookup/evaluated"
	mutatingLookupSeedPath = "/api/v1/namespaces/default/configmaps"
	mutatingLookupSeed     = mutatingLookupSeedPath + "/seed"
)

type mutatingLookupAPI struct {
	mu          sync.Mutex
	calls       []string
	seed        corev1.ConfigMap
	uid         types.UID
	webhookName string
	missing     bool
}

func (s *mutatingLookupAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request := r.Method + " " + r.URL.RequestURI()
	s.calls = append(s.calls, request)
	w.Header().Set("Content-Type", "application/json")
	switch request {
	case "POST " + mutatingLookupSeedPath:
		if err := json.NewDecoder(r.Body).Decode(&s.seed); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.seed.UID, s.seed.Namespace, s.seed.ResourceVersion = s.uid, "default", "1"
		_ = json.NewEncoder(w).Encode(s.seed)
	case "GET " + mutatingLookupSeed:
		_ = json.NewEncoder(w).Encode(s.seed)
	case "DELETE " + mutatingLookupSeed:
		_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
	case mutatingLookupGET:
		if s.missing {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(metav1.Status{
				Status: "Failure", Code: http.StatusNotFound, Reason: metav1.StatusReasonNotFound,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(admissionv1.MutatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "lookup.test", UID: "webhook-id"},
			Webhooks:   []admissionv1.MutatingWebhook{{Name: s.webhookName}},
		})
	default:
		http.Error(w, "unexpected "+request, http.StatusBadRequest)
	}
}

func (s *mutatingLookupAPI) expect(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, want, s.calls)
	s.calls = nil
}

func (s *mutatingLookupAPI) respond(uid types.UID, webhookName string, missing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uid, s.webhookName, s.missing = uid, webhookName, missing
}

func TestCompiledMutatingWebhookLookupRuntime(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	work := t.TempDir()
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(mutatingLookupFixture, "factory.ub"), OutDir: work,
		StackName: "mutating-webhook-lookup", Version: "v0",
		CLIVersion: "v0.13.0", GoVersion: "1.26.2", Build: true,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)

	const initialWebhook = "initial.example.test"
	api := &mutatingLookupAPI{uid: "lookup.test", webhookName: initialWebhook}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(work, "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "lookup",
		Clusters:       map[string]*clientcmdapi.Cluster{"lookup": {Server: server.URL}},
		Contexts:       map[string]*clientcmdapi.Context{"lookup": {Cluster: "lookup"}},
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)

	run := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = work
		body, err := cmd.CombinedOutput()
		return string(body), err
	}
	configure := func(t *testing.T, name, stateName string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(mutatingLookupFixture, name+".ub"))
		require.NoError(t, err)
		body = bytes.ReplaceAll(body, []byte("STATE_CASE"), []byte(stateName))
		require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
		out, err := run(t, "pin", "-c", "config.ub")
		require.NoError(t, err, "%s", out)
	}
	plan := func(t *testing.T) (string, error) {
		t.Helper()
		return run(t, "plan", "-c", "config.ub", "-o", "plan.ubp")
	}
	openPlan := func(t *testing.T) *runtime.PlanFile {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(work, "plan.ubp"))
		require.NoError(t, err)
		opened, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
			return encrypters.Noop{}, nil
		})
		require.NoError(t, err)
		return opened
	}
	lookupStep := func(t *testing.T, plan *runtime.PlanFile) runtime.PlanStep {
		t.Helper()
		for _, step := range plan.Steps {
			if step.Address == mutatingLookupAddress {
				require.Equal(t, runtime.DecisionRead, step.Decision)
				return step
			}
		}
		t.Fatal("missing lookup step")
		return runtime.PlanStep{}
	}
	apply := func(t *testing.T) (string, error) {
		t.Helper()
		return run(t, "apply", "plan.ubp")
	}
	fail := func(t *testing.T, out string, err error, message string) {
		t.Helper()
		require.Error(t, err, "%s", out)
		require.Contains(t, out, mutatingLookupAddress)
		require.Contains(t, out, message)
	}

	t.Run("evaluated inputs on an otherwise NoOp stack", func(t *testing.T) {
		configure(t, "config", "steady")
		out, err := plan(t)
		require.NoError(t, err, "%s", out)
		api.expect(t)
		out, err = apply(t)
		require.NoError(t, err, "%s", out)
		api.expect(t, "POST "+mutatingLookupSeedPath, "GET "+mutatingLookupSeed, mutatingLookupGET)

		for _, tc := range []struct{ config, message string }{
			{"invalid-metadata", `field "metadata.name": required but is null`},
			{"invalid-name", "constraints[0]"},
			{"invalid-label", "constraints[2]"},
			{"invalid-annotation", "constraints[3]"},
		} {
			t.Run(tc.config, func(t *testing.T) {
				configure(t, tc.config, "steady")
				out, err := plan(t)
				fail(t, out, err, tc.message)
				api.expect(t, "GET "+mutatingLookupSeed)
			})
		}

		configure(t, "config", "steady")
		out, err = plan(t)
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+mutatingLookupSeed, mutatingLookupGET)
		saved := openPlan(t)
		decisions := map[string]runtime.Decision{}
		for _, step := range saved.Steps {
			decisions[step.Address] = step.Decision
		}
		require.Equal(t, runtime.DecisionNoOp, decisions["resource.seed"])
		step := lookupStep(t, saved)
		hooks, ok := step.ObservedOutputs["webhook"].([]any)
		require.True(t, ok)
		require.Equal(t, []any{map[string]any{
			"name": initialWebhook, "admission-review-versions": []any{},
			"client-config": map[string]any{"ca-bundle": nil, "service": nil, "url": nil},
			"rule":          []any{}, "side-effects": nil, "failure-policy": nil, "match-policy": nil,
			"namespace-selector": nil, "object-selector": nil, "reinvocation-policy": nil,
			"timeout-seconds": nil,
		}}, hooks)
		out, err = apply(t)
		require.NoError(t, err, "%s", out)
		api.expect(t, mutatingLookupGET)

		out, err = plan(t)
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+mutatingLookupSeed, mutatingLookupGET)
		saved = openPlan(t)
		saved.Inputs["name"] = "Bad_Name"
		body, err := json.Marshal(saved)
		require.NoError(t, err)
		sealed, err := state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, "invalid-saved.ubp"), sealed, 0o600))
		out, err = run(t, "apply", "invalid-saved.ubp")
		fail(t, out, err, "constraints[0]")
		api.expect(t)

		for _, tc := range []struct {
			name, changedWebhook string
			missing              bool
		}{
			{"changed hit", "changed.example.test", false},
			{"hit to miss", "", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				api.respond("lookup.test", initialWebhook, false)
				out, err := plan(t)
				require.NoError(t, err, "%s", out)
				api.expect(t, "GET "+mutatingLookupSeed, mutatingLookupGET)
				api.respond("lookup.test", tc.changedWebhook, tc.missing)
				out, err = apply(t)
				fail(t, out, err, "changed since the plan was computed; plan again")
				api.expect(t, mutatingLookupGET)
			})
		}

		out, err = run(t, "plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+mutatingLookupSeed)
		out, err = run(t, "apply", "destroy.ubp")
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+mutatingLookupSeed, "DELETE "+mutatingLookupSeed)
	})

	t.Run("known violations with pending inputs", func(t *testing.T) {
		for _, tc := range []struct{ config, rule string }{
			{"pending-invalid-key", "metadata.labels keys"},
			{"pending-invalid-value", "metadata.labels values"},
		} {
			t.Run(tc.config, func(t *testing.T) {
				configure(t, tc.config, tc.config)
				out, err := plan(t)
				fail(t, out, err, tc.rule)
				require.Contains(t, out, "schema: constraints[")
				api.expect(t)
			})
		}
	})

	t.Run("saved deferred plans", func(t *testing.T) {
		for _, tc := range []struct{ name, config, uid, rule string }{
			{"valid-name", "pending-name", "lookup.test", ""},
			{"valid-label", "pending-label", "value", ""},
			{"invalid-name", "pending-name", "Bad_Name", "metadata.name"},
			{"invalid-label", "pending-label", "bad value", "metadata.labels values"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				api.respond(types.UID(tc.uid), initialWebhook, false)
				configure(t, tc.config, tc.name)
				out, err := plan(t)
				require.NoError(t, err, "%s", out)
				api.expect(t)
				step := lookupStep(t, openPlan(t))
				require.Nil(t, step.ObservedOutputs)
				require.Equal(t, map[string][]string{
					"metadata": {"resource.seed.object.metadata.uid"},
				}, step.UnresolvedInputs)
				out, err = apply(t)
				if tc.rule != "" {
					fail(t, out, err, tc.rule)
					require.Contains(t, out, "schema: constraints[")
					api.expect(t, "POST "+mutatingLookupSeedPath, "GET "+mutatingLookupSeed)
					return
				}
				require.NoError(t, err, "%s", out)
				api.expect(t, "POST "+mutatingLookupSeedPath, "GET "+mutatingLookupSeed, mutatingLookupGET)
			})
		}
	})
}
