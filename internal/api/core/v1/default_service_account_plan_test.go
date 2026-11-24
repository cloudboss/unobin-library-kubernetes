package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const defaultAccountAddress = "resource.account"
const defaultAccountReplacementPath = "/api/v1/namespaces/replacement/serviceaccounts/default"

func TestDefaultServiceAccountStatePlansAndReplacement(t *testing.T) {
	api := newDefaultAccountPlanAPI(t)
	api.accounts[defaultAccountPath].Secrets = []corev1.ObjectReference{
		{Name: "beta"}, {Name: "alpha"},
	}
	api.accounts[defaultAccountPath].ImagePullSecrets = []corev1.LocalObjectReference{{Name: "image"}}
	api.accounts[defaultAccountPath].Annotations = map[string]string{"keep": "annotation"}
	executor := defaultAccountExecutor(t, api, "settled", nil)
	inputs := executor.Inputs
	dir := t.TempDir()
	conf := clientcmdapi.Config{
		CurrentContext: "one",
		Clusters:       map[string]*clientcmdapi.Cluster{"test": {Server: *api.config.Server}},
		Contexts: map[string]*clientcmdapi.Context{
			"one": {Cluster: "test", AuthInfo: "test", Namespace: "from-kubeconfig"},
			"two": {Cluster: "test", AuthInfo: "test", Namespace: "from-kubeconfig"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {}},
	}
	first, second := filepath.Join(dir, "first.kubeconfig"), filepath.Join(dir, "second.kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(conf, first))
	require.NoError(t, clientcmd.WriteToFile(conf, second))
	inputs["kubeconfig"], inputs["current-context"] = first, "one"
	applyDefaultAccountPlan(t, executor, runtime.DecisionCreate)
	require.Equal(t, []string{
		"GET " + defaultAccountPath, "GET /version", "PATCH " + defaultAccountPath,
		"GET " + defaultAccountPath,
	}, api.requests())
	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	account := snapshot.Find(defaultAccountAddress)
	require.NotNil(t, account)
	require.Equal(t, map[string]any{
		"metadata": map[string]any{
			"namespace": "default", "name": "default", "uid": "saved-uid",
			"labels": map[string]any{}, "annotations": map[string]any{"keep": "annotation"},
		},
		"image-pull-secrets": []any{map[string]any{"name": "image"}},
		"secrets": []any{
			map[string]any{"name": "alpha"}, map[string]any{"name": "beta"},
		},
		"automount-service-account-token": true,
	}, account.Outputs["object"])
	planDefaultAccount(t, executor, runtime.DecisionNoOp)

	api.edit(func(live *corev1.ServiceAccount) {
		live.Generation, live.ResourceVersion = 8, "changed"
		live.Labels = map[string]string{"kubernetes.io/hidden": "changed"}
	})
	steady := planDefaultAccount(t, executor, runtime.DecisionNoOp)
	require.False(t, defaultAccountStep(t, steady).Drift())

	metadata := map[string]any{"name": "default", "namespace": "default"}
	inputs["object"] = map[string]any{
		"metadata": metadata, "automount-service-account-token": true,
		"image-pull-secrets": []any{}, "secrets": []any{},
	}
	planDefaultAccount(t, executor, runtime.DecisionNoOp)
	desired := inputs["object"].(map[string]any)
	desired["secrets"] = []any{map[string]any{"name": "beta"}, map[string]any{"name": "alpha"}}
	desired["image-pull-secrets"] = []any{map[string]any{"name": "image"}}
	metadata["annotations"] = map[string]any{"keep": "annotation"}
	before := len(api.requests())
	applyDefaultAccountPlan(t, executor, runtime.DecisionUpdate)
	require.Equal(t, []string{
		"GET " + defaultAccountPath, "PATCH " + defaultAccountPath, "GET " + defaultAccountPath,
	}, api.requests()[before:])
	require.Equal(t, []configMapPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: "saved-uid"},
	}, api.lastPatch())
	desired["secrets"] = []any{
		map[string]any{"name": "alpha"}, map[string]any{"name": "beta"}, map[string]any{"name": "beta"},
	}
	inputs["token"] = "new-credential"
	planDefaultAccount(t, executor, runtime.DecisionNoOp)

	api.edit(func(live *corev1.ServiceAccount) { live.AutomountServiceAccountToken = new(false) })
	before = len(api.requests())
	applyDefaultAccountPlan(t, executor, runtime.DecisionUpdate)
	require.Equal(t, []string{
		"GET " + defaultAccountPath, "PATCH " + defaultAccountPath, "GET " + defaultAccountPath,
	}, api.requests()[before:])
	require.Equal(t, []configMapPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: "saved-uid"},
		{Op: "replace", Path: "/automountServiceAccountToken", Value: true},
	}, api.lastPatch())

	for _, test := range []struct{ field, value string }{
		{"server", "https://other.invalid"}, {"kubeconfig", second}, {"current-context", "two"},
	} {
		t.Run(test.field, func(t *testing.T) {
			original := inputs[test.field]
			inputs[test.field] = test.value
			defer func() { inputs[test.field] = original }()
			plan := planDefaultAccount(t, executor, runtime.DecisionReplace)
			require.Equal(t, []string{test.field}, defaultAccountStep(t, plan).ReplacementReasons)
		})
	}

	metadata["namespace"] = "replacement"
	plan := planDefaultAccount(t, executor, runtime.DecisionReplace)
	require.Equal(t, []string{"object.metadata.namespace"},
		defaultAccountStep(t, plan).ReplacementReasons)
	before = len(api.requests())
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET " + defaultAccountPath, "DELETE " + defaultAccountPath,
		"GET " + defaultAccountReplacementPath, "GET /version",
		"PATCH " + defaultAccountReplacementPath, "GET " + defaultAccountReplacementPath,
	}, api.requests()[before:])
	require.Equal(t, []defaultAccountDeletion{{defaultAccountPath, "saved-uid"}}, api.deletions)
	snapshot, err = executor.Store.Current()
	require.NoError(t, err)
	replaced := snapshot.Find(defaultAccountAddress)
	require.NotNil(t, replaced)
	require.Equal(t, map[string]any{
		"name": "default", "namespace": "replacement", "uid": "replacement-uid",
		"labels": map[string]any{}, "annotations": map[string]any{"keep": "annotation"},
	}, replaced.Outputs["object"].(map[string]any)["metadata"])
}

func TestDefaultServiceAccountPatternUpdatesUseLiveProjection(t *testing.T) {
	api := newDefaultAccountPlanAPI(t)
	executor := defaultAccountExecutor(t, api, "settled", nil)
	executor.Inputs["object"] = map[string]any{"metadata": map[string]any{
		"labels": map[string]any{"managed": "desired"},
	}}
	applyDefaultAccountPlan(t, executor, runtime.DecisionCreate)
	api.edit(func(live *corev1.ServiceAccount) {
		live.Labels["external/key"] = "preserve when ignored"
	})
	executor.Inputs["ignore-labels"] = []any{"^external/"}
	applyDefaultAccountPlan(t, executor, runtime.DecisionUpdate)
	require.Equal(t, []configMapPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: "saved-uid"},
	}, api.lastPatch())
	require.Equal(t, map[string]string{
		"managed": "desired", "external/key": "preserve when ignored",
	}, api.accounts[defaultAccountPath].Labels)

	executor.Inputs["ignore-labels"] = []any{}
	applyDefaultAccountPlan(t, executor, runtime.DecisionUpdate)
	require.Equal(t, []configMapPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: "saved-uid"},
		{Op: "remove", Path: "/metadata/labels/external~1key"},
	}, api.lastPatch())
	require.Equal(t, map[string]string{"managed": "desired"}, api.accounts[defaultAccountPath].Labels)
}

func TestDefaultServiceAccountNativeHistoricalNoOpAndDestroy(t *testing.T) {
	api := newDefaultAccountPlanAPI(t)
	executor := defaultAccountExecutor(t, api, "settled", nil)
	library := executor.Libraries["core"]
	key := "resource.default-service-account"
	native := library.Constraints[key]
	delete(library.Constraints, key)
	executor.Inputs["ignore-labels"] = []any{"["}
	applyDefaultAccountPlan(t, executor, runtime.DecisionCreate)
	saved := pvcSavedPlan(t, planDefaultAccount(t, executor, runtime.DecisionNoOp))
	before := len(api.requests())
	library.Constraints[key] = native
	_, err := executor.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "resource.account: schema: constraints[5]")
	require.ErrorContains(t, err, "ignore-labels must contain valid regular expressions")
	require.Empty(t, api.requests()[before:])

	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.account: schema: constraints[5]")
	require.ErrorContains(t, err, "ignore-labels must contain valid regular expressions")
	require.Equal(t, []string{"GET " + defaultAccountPath}, api.requests()[before:])
	executor.Destroy = true
	applyDefaultAccountPlan(t, executor, runtime.DecisionDestroy)
	require.Equal(t, []defaultAccountDeletion{{defaultAccountPath, "saved-uid"}}, api.deletions)
	require.Nil(t, api.accounts[defaultAccountPath])
}

func TestDefaultServiceAccountNativeDeferredMapAndReplacement(t *testing.T) {
	for _, test := range []struct {
		name, known, value string
		invalid, replaces  bool
	}{
		{"checks known value in partial map", "has spaces", "valid", true, false},
		{"rejects invalid resolved value", "valid", "has spaces", true, false},
		{"accepts valid resolved value", "valid", "valid", false, false},
		{"rejects pending replacement before delete", "valid", "has spaces", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newDefaultAccountPlanAPI(t)
			initial := defaultAccountExecutor(t, api, "settled", nil)
			if test.replaces {
				applyDefaultAccountPlan(t, initial, runtime.DecisionCreate)
			}
			executor := defaultAccountExecutor(t, api, "deferred", initial.Store)
			namespace := "default"
			if test.replaces {
				namespace = "replacement"
			}
			executor.Inputs["namespace"] = namespace
			executor.Inputs["known"], executor.Inputs["value"] = test.known, test.value
			before := len(api.requests())
			plan, err := executor.Plan(t.Context())
			if test.known == "has spaces" {
				require.ErrorContains(t, err, "resource.account: schema: constraints[2]")
				require.ErrorContains(t, err, "object.metadata.labels must have valid label values")
				require.Empty(t, api.requests()[before:])
				return
			}
			require.NoError(t, err)
			step := defaultAccountStep(t, plan)
			require.Equal(t, map[string][]string{
				"object": {"action.seed.namespace", "action.seed.value"},
			}, step.UnresolvedInputs)
			if test.replaces {
				require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
				require.Equal(t, []string{"object.metadata.namespace"}, step.PendingReplacementReasons)
			} else {
				require.Equal(t, runtime.DecisionCreate, step.Decision)
			}
			before = len(api.requests())
			_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, plan))
			if test.invalid {
				require.ErrorContains(t, err, "resource.account: schema: constraints[2]")
				require.ErrorContains(t, err, "object.metadata.labels must have valid label values")
				require.Empty(t, api.requests()[before:])
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET " + defaultAccountPath, "GET /version", "PATCH " + defaultAccountPath,
				"GET " + defaultAccountPath,
			}, api.requests()[before:])
			require.Equal(t, map[string]string{"known": "valid", "deferred": "valid"},
				api.accounts[defaultAccountPath].Labels)
		})
	}
}

type defaultAccountSeed struct {
	EchoNamespace string
	EchoValue     string
}

type defaultAccountSeedOutput struct {
	Namespace string
	Value     string
}

func (s *defaultAccountSeed) Run(
	_ context.Context, _ *config.Configuration,
) (*defaultAccountSeedOutput, error) {
	return &defaultAccountSeedOutput{Namespace: s.EchoNamespace, Value: s.EchoValue}, nil
}

func defaultAccountExecutor(
	t *testing.T, api *defaultAccountPlanAPI, fixture string, store state.Backend,
) *runtime.Executor {
	t.Helper()
	path := filepath.Join("testdata/ub/default-service-account", fixture, "factory.ub")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	library := &runtime.Library{
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"default-service-account": runtime.MakeResource[
				DefaultServiceAccountResource, *DefaultServiceAccountResourceOutput, *config.Configuration,
			]((&DefaultServiceAccountResource{}).ResourceDefinition()),
		},
		Actions: map[string]runtime.ActionRegistration{
			"seed": runtime.MakeAction[
				defaultAccountSeed, *defaultAccountSeedOutput, *config.Configuration,
			](),
		},
	}
	schema, err := nativeCoreSchema()
	require.NoError(t, err)
	resource := schema.Resources["default-service-account"]
	library.Constraints = map[string][]lang.ConstraintSpec{
		"resource.default-service-account": resource.Constraints,
	}
	library.Defaults = map[string][]lang.DefaultSpec{
		"resource.default-service-account": resource.Defaults,
	}
	if store == nil {
		store, err = local.NewStore(t.TempDir(), "default-service-account", "test", encrypters.Noop{})
		require.NoError(t, err)
	}
	libs := map[string]*runtime.Library{"core": library}
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Inputs: map[string]any{
			"server": *api.config.Server, "kubeconfig": nil, "current-context": nil, "token": nil,
			"ignore-labels": nil, "ignore-annotations": nil,
			"object": map[string]any{"metadata": map[string]any{}},
		}, Store: store, Factory: state.FactoryInfo{
			Name: "default-service-account", Version: "v0", ContentRevision: "c0",
		},
	}
}

type defaultAccountDeletion struct {
	path string
	uid  string
}

type defaultAccountPlanAPI struct {
	mu        sync.Mutex
	config    *config.Configuration
	accounts  map[string]*corev1.ServiceAccount
	calls     []string
	patch     []configMapPatchOperation
	deletions []defaultAccountDeletion
}

func newDefaultAccountPlanAPI(t *testing.T) *defaultAccountPlanAPI {
	t.Helper()
	replacement := defaultAccountLive()
	replacement.Namespace, replacement.UID = "replacement", "replacement-uid"
	replacement.Secrets = []corev1.ObjectReference{{Name: "old"}}
	replacement.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "old"}}
	api := &defaultAccountPlanAPI{accounts: map[string]*corev1.ServiceAccount{
		defaultAccountPath: defaultAccountLive(), defaultAccountReplacementPath: replacement,
	}}
	api.config = configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		path := req.URL.Path
		api.calls = append(api.calls, req.Method+" "+path)
		if path == "/version" {
			respond(t, w, map[string]string{"gitVersion": "v1.24"})
			return
		}
		live := api.accounts[path]
		if live == nil {
			namespaceStatus(t, w, http.StatusNotFound)
			return
		}
		switch req.Method {
		case http.MethodGet:
			respond(t, w, live)
		case http.MethodPatch:
			raw, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			api.patch = nil
			require.NoError(t, json.Unmarshal(raw, &api.patch))
			require.NoError(t, applyDefaultAccountJSONPatch(live, raw))
			respond(t, w, live)
		case http.MethodDelete:
			var opts metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&opts))
			require.NotNil(t, opts.Preconditions)
			require.NotNil(t, opts.Preconditions.UID)
			require.Equal(t, live.UID, *opts.Preconditions.UID)
			api.deletions = append(api.deletions, defaultAccountDeletion{
				path: path, uid: string(*opts.Preconditions.UID),
			})
			delete(api.accounts, path)
			respond(t, w, &metav1.Status{Status: "Success"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
			namespaceStatus(t, w, http.StatusBadRequest)
		}
	})
	return api
}

func (api *defaultAccountPlanAPI) requests() []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]string(nil), api.calls...)
}

func (api *defaultAccountPlanAPI) lastPatch() []configMapPatchOperation {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]configMapPatchOperation(nil), api.patch...)
}

func (api *defaultAccountPlanAPI) edit(edit func(*corev1.ServiceAccount)) {
	api.mu.Lock()
	defer api.mu.Unlock()
	edit(api.accounts[defaultAccountPath])
}

func planDefaultAccount(
	t *testing.T, executor *runtime.Executor, decision runtime.Decision,
) *runtime.Plan {
	t.Helper()
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, decision, defaultAccountStep(t, plan).Decision)
	return plan
}

func defaultAccountStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == defaultAccountAddress {
			return step
		}
	}
	t.Fatal("plan lacks " + defaultAccountAddress)
	return nil
}

func applyDefaultAccountPlan(t *testing.T, executor *runtime.Executor, decision runtime.Decision) {
	t.Helper()
	plan := planDefaultAccount(t, executor, decision)
	_, err := executor.ApplyPlan(t.Context(), pvcSavedPlan(t, plan))
	require.NoError(t, err)
}
