package compile_test

import (
	"bytes"
	"encoding/json"
	"io"
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
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
)

func TestResourceQuotaLiteralInputs(t *testing.T) {
	for _, name := range []string{
		"named", "generated", "missing-metadata", "missing-identity", "both", "empty-name",
		"empty-prefix", "invalid-name", "invalid-prefix", "invalid-label-key", "invalid-label-value",
		"invalid-annotation", "invalid-scope", "invalid-scope-name", "invalid-operator",
		"missing-operator", "missing-scope-name", "numeric-hard", "list-spec", "status-input",
		"invalid-ignore-labels", "invalid-ignore-annotations", "literal-partial",
	} {
		t.Run(name, func(t *testing.T) {
			err := compileFixture(t, "resource-quota/"+name)
			switch name {
			case "named", "generated":
				require.NoError(t, err)
			case "missing-metadata", "missing-operator", "missing-scope-name", "numeric-hard",
				"list-spec", "status-input":
				require.Error(t, err)
			default:
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestResourceQuotaEvaluatedPartialInputs(t *testing.T) {
	binary, fixture := compileResourceQuotaFactory(t, "deferred")
	for _, test := range []struct {
		name, target, knownOperator, resolvedOperator, planErr, applyErr string
	}{
		{name: "known invalid operator", target: "subject", knownOperator: "invalid",
			resolvedOperator: "In",
			planErr: "constraints[10] (predicate): predicate requirement not satisfied " +
				"(spec.scope-selector.match-expressions[0])"},
		{name: "known invalid metadata", target: "bad name", knownOperator: "In",
			resolvedOperator: "In", planErr: "constraints[1] (predicate)"},
		{name: "invalid deferred operator", target: "subject", knownOperator: "In",
			resolvedOperator: "invalid",
			applyErr: "constraints[10] (predicate): predicate requirement not satisfied " +
				"(spec.scope-selector.match-expressions[1])"},
		{name: "valid deferred input", target: "subject", knownOperator: "In",
			resolvedOperator: "NotIn"},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := resourceQuotaCompileDir(t)
			api := startCompiledResourceQuotaAPI(t)
			configureCompiledQuota(t, fixture, work, map[string]string{
				"QUOTA_SERVER": api.server.URL, "QUOTA_NAME": test.target,
				"QUOTA_KNOWN_OPERATOR":    test.knownOperator,
				"QUOTA_RESOLVED_OPERATOR": test.resolvedOperator,
			})
			run := quotaFactoryRunner(t, binary, work)
			out, err := run("plan", "-c", "config.ub", "-o", "plan.ubp")
			require.Empty(t, api.requests())
			if test.planErr != "" {
				require.Error(t, err)
				require.Contains(t, out, "resource.subject: schema: constraints[")
				require.Contains(t, out, test.planErr)
				return
			}
			require.NoError(t, err, "%s", out)
			step := compiledQuotaPlanStep(t, work)
			require.Equal(t, runtime.DecisionCreate, step.Decision)
			require.Equal(t, map[string][]string{"spec": {
				"data-source.live.data['hard']", "data-source.live.data['class']",
				"data-source.live.data['operator']",
			}}, step.UnresolvedInputs)
			out, err = run("apply", "plan.ubp")
			want := []string{"POST /api/v1/namespaces/default/configmaps",
				"GET /api/v1/namespaces/default/configmaps/quota-seed",
				"GET /api/v1/namespaces/default/configmaps/quota-seed"}
			if test.applyErr != "" {
				require.Error(t, err)
				require.Contains(t, out, "resource.subject: schema: constraints[")
				require.Contains(t, out, test.applyErr)
				require.Equal(t, want, api.requests())
				require.Nil(t, api.quota())
				return
			}
			require.NoError(t, err, "%s", out)
			want = append(want, "POST /api/v1/namespaces/default/resourcequotas",
				"GET /api/v1/namespaces/default/resourcequotas/subject",
				"GET /api/v1/namespaces/default/resourcequotas/subject",
				"GET /api/v1/namespaces/default/configmaps/quota-seed")
			require.Equal(t, want, api.requests())
			require.JSONEq(t, `{"hard":{"pods":"2"},"scopeSelector":{"matchExpressions":[
				{"scopeName":"PriorityClass","operator":"In","values":["worker"]},
				{"scopeName":"PriorityClass","operator":"NotIn","values":["worker"]},
				{"scopeName":"BestEffort","operator":"Exists"}]}}`,
				string(api.spec(t)))
		})
	}
}

func TestResourceQuotaEvaluatedRequiredInputs(t *testing.T) {
	binary, fixture := compileResourceQuotaFactory(t, "required")
	for _, test := range []struct {
		name, choice, metadata, diagnostic string
	}{
		{"null metadata", "valid", "null-value", `"metadata": required but is null`},
		{"missing operator", "missing-operator", "valid",
			`"spec.scope-selector.match-expressions[0].operator": required but not provided`},
		{"null operator", "null-operator", "valid",
			`"spec.scope-selector.match-expressions[0].operator": required but is null`},
		{"missing scope", "missing-scope", "valid",
			`"spec.scope-selector.match-expressions[0].scope-name": required but not provided`},
		{"null scope", "null-scope", "valid",
			`"spec.scope-selector.match-expressions[0].scope-name": required but is null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := resourceQuotaCompileDir(t)
			api := startCompiledResourceQuotaAPI(t)
			configureCompiledQuota(t, fixture, work, map[string]string{
				"QUOTA_SERVER": api.server.URL, "QUOTA_CHOICE": test.choice, "QUOTA_META": test.metadata,
			})
			run := quotaFactoryRunner(t, binary, work)
			out, err := run("plan", "-c", "config.ub")
			require.Error(t, err)
			require.Contains(t, out, test.diagnostic)
			require.Empty(t, api.requests())
		})
	}
}

func TestResourceQuotaDeferredRequiredReplacement(t *testing.T) {
	binary, fixture := compileResourceQuotaFactory(t, "shape")
	work := resourceQuotaCompileDir(t)
	api := startCompiledResourceQuotaAPI(t)
	configure := func(name, choice string) {
		t.Helper()
		configureCompiledQuota(t, fixture, work, map[string]string{
			"QUOTA_SERVER": api.server.URL, "QUOTA_CHOICE": choice, "QUOTA_NAME": name,
		})
	}
	configure("original", "valid")
	run := quotaFactoryRunner(t, binary, work)
	out, err := run("plan", "-c", "config.ub", "-o", "plan.ubp")
	require.NoError(t, err, "%s", out)
	out, err = run("apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	prior := api.quota()
	require.Equal(t, "original", prior.Name)
	configure("bad name", "missing-operator")
	_, err = run("pin", "-c", "config.ub")
	require.NoError(t, err)
	before := len(api.requests())
	out, err = run("plan", "-c", "config.ub", "-o", "plan.ubp")
	require.Error(t, err)
	require.Contains(t, out, "resource.subject: schema: constraints[1] (predicate)")
	require.Equal(t, prior, api.quota())
	require.ElementsMatch(t, []string{
		"GET /api/v1/namespaces/default/configmaps/quota-seed",
		"GET /api/v1/namespaces/default/resourcequotas/original",
	}, api.requests()[before:])

	configure("replacement", "missing-operator")
	_, err = run("pin", "-c", "config.ub")
	require.NoError(t, err)
	out, err = run("plan", "-c", "config.ub", "-o", "plan.ubp")
	require.NoError(t, err, "%s", out)
	step := compiledQuotaPlanStep(t, work)
	require.Equal(t, runtime.DecisionReplace, step.Decision)
	require.Equal(t, map[string][]string{"spec": {
		"data-source.live.data['choice']",
	}}, step.UnresolvedInputs)
	before = len(api.requests())
	out, err = run("apply", "plan.ubp")
	require.Error(t, err)
	require.Contains(t, out,
		`"spec.scope-selector.match-expressions[0].operator": required but not provided`)
	require.Equal(t, prior, api.quota())
	require.Equal(t, []string{"PATCH /api/v1/namespaces/default/configmaps/quota-seed",
		"GET /api/v1/namespaces/default/configmaps/quota-seed",
		"GET /api/v1/namespaces/default/configmaps/quota-seed"}, api.requests()[before:])

	configure("original", "valid")
	_, err = run("pin", "-c", "config.ub")
	require.NoError(t, err)
	out, err = run("plan", "-c", "config.ub", "-o", "plan.ubp")
	require.NoError(t, err, "%s", out)
	retry := compiledQuotaPlanStep(t, work)
	require.Equal(t, step.PriorInputs, retry.PriorInputs)
	require.Equal(t, step.PriorOutputs, retry.PriorOutputs)

}

func compileResourceQuotaFactory(t *testing.T, name string) (string, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	work := resourceQuotaCompileDir(t)
	fixture, err := filepath.Abs(filepath.Join("testdata/ub/resource-quota", name))
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work, Build: true,
		StackName: "resource-quota", Version: "v0.0.0",
		CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	return result.BinaryPath, fixture
}

func resourceQuotaCompileDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs("../../_output/tests/core-v1-resource-quota")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	return work
}

func configureCompiledQuota(t *testing.T, fixture, work string, values map[string]string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fixture, "config.ub"))
	require.NoError(t, err)
	for key, value := range values {
		body = bytes.ReplaceAll(body, []byte(key), []byte(value))
	}
	require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
}

func quotaFactoryRunner(t *testing.T, binary, work string) func(...string) (string, error) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(work, "missing"))
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("pin", "-c", "config.ub")
	require.NoError(t, err, "%s", out)
	return run
}

func compiledQuotaPlanStep(t *testing.T, work string) runtime.PlanStep {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(raw, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			return step
		}
	}
	require.FailNow(t, "plan has no ResourceQuota subject")
	return runtime.PlanStep{}
}

type compiledResourceQuotaAPI struct {
	server    *httptest.Server
	mu        sync.Mutex
	calls     []string
	configMap *corev1.ConfigMap
	subject   *corev1.ResourceQuota
}

func startCompiledResourceQuotaAPI(t *testing.T) *compiledResourceQuotaAPI {
	t.Helper()
	api := &compiledResourceQuotaAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, req.Method+" "+req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "/configmaps") {
			switch req.Method {
			case http.MethodPost:
				api.configMap = &corev1.ConfigMap{}
				require.NoError(t, json.NewDecoder(req.Body).Decode(api.configMap))
				api.configMap.UID = "seed-uid"
			case http.MethodPatch:
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(body)
				require.NoError(t, err)
				old, err := json.Marshal(api.configMap)
				require.NoError(t, err)
				patched, err := patch.Apply(old)
				require.NoError(t, err)
				api.configMap = &corev1.ConfigMap{}
				require.NoError(t, json.Unmarshal(patched, api.configMap))
			}
			require.NoError(t, json.NewEncoder(w).Encode(api.configMap))
			return
		}
		switch req.Method {
		case http.MethodPost:
			api.subject = &corev1.ResourceQuota{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(api.subject))
			api.subject.UID = "quota-uid"
			api.subject.Status.Hard = api.subject.Spec.Hard
		case http.MethodGet:
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		require.NoError(t, json.NewEncoder(w).Encode(api.subject))
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (api *compiledResourceQuotaAPI) requests() []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]string(nil), api.calls...)
}

func (api *compiledResourceQuotaAPI) quota() *corev1.ResourceQuota {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.subject.DeepCopy()
}

func (api *compiledResourceQuotaAPI) spec(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(api.quota().Spec)
	require.NoError(t, err)
	return body
}
