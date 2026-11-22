package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestServiceAccountStateBackedPlanApply(t *testing.T) {
	var stored *corev1.ServiceAccount
	mutations := 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/version" {
			respond(t, w, map[string]string{"gitVersion": "v1.24.0"})
			return
		}
		switch req.Method {
		case "POST":
			mutations++
			require.NoError(t, json.NewDecoder(req.Body).Decode(&stored))
			stored.Name, stored.UID = "generated-actual", "uid"
		case "PATCH":
			mutations++
			raw, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(raw)
			require.NoError(t, err)
			body, err := json.Marshal(stored)
			require.NoError(t, err)
			body, err = patch.Apply(body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, stored))
		case "GET":
			if stored == nil {
				namespaceStatus(t, w, 404)
				return
			}
		default:
			t.Errorf("unexpected operation %s %s", req.Method, req.URL)
		}
		respond(t, w, stored)
	})
	source, err := os.ReadFile("testdata/ub/service-account-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": {
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{"service-account": runtime.MakeResource[
			ServiceAccountResource, *ServiceAccountResourceOutput, *config.Configuration,
		]((&ServiceAccountResource{}).ResourceDefinition())},
	}}
	addNativeCoreConstraints(t, libs["core"])
	store, err := local.NewStore(t.TempDir(), "service-account", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := map[string]any{"server": *c.Server, "value": "first", "prefix": "generated-",
		"namespace": "default", "automount": true}
	plan := func(want runtime.Decision) (*runtime.Executor, *runtime.Plan) {
		t.Helper()
		executor := &runtime.Executor{
			DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
			Libraries: libs, Inputs: inputs, Store: store,
			Factory: state.FactoryInfo{Name: "service-account", Version: "v0", ContentRevision: "c0"},
		}
		p, err := executor.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.account" {
				require.Equal(t, want, step.Decision)
				found = true
			}
		}
		require.True(t, found)
		return executor, p
	}
	apply := func(executor *runtime.Executor, p *runtime.Plan) {
		t.Helper()
		encoded, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		_, err = executor.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
	}
	executor, p := plan(runtime.DecisionCreate)
	apply(executor, p)
	plan(runtime.DecisionNoOp)
	stored.Generation, stored.ResourceVersion = 10, "new"
	stored.CreationTimestamp = metav1.Now()
	stored.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller"}}
	stored.Annotations = map[string]string{"kubernetes.io/internal": "hidden", "ignored/key": "hidden"}
	stored.Secrets = []corev1.ObjectReference{{Name: "beta"}, {Name: "alpha"}, {Name: "beta"}}
	stored.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "beta"},
		{Name: "alpha"},
		{Name: "beta"}}
	plan(runtime.DecisionNoOp)
	require.Equal(t, 1, mutations)
	stored.AutomountServiceAccountToken = new(false)
	executor, p = plan(runtime.DecisionUpdate)
	apply(executor, p)
	require.True(t, *stored.AutomountServiceAccountToken)
	inputs["value"] = "updated"
	executor, p = plan(runtime.DecisionUpdate)
	apply(executor, p)
	require.Equal(t, "uid", string(stored.UID))
	require.Equal(t, "updated", stored.Labels["value"])
	plan(runtime.DecisionNoOp)
	for _, field := range []string{"prefix", "namespace", "server"} {
		original := inputs[field]
		inputs[field] = map[string]string{"prefix": "changed-",
			"namespace": "changed",
			"server":    "https://unused.example"}[field]
		plan(runtime.DecisionReplace)
		inputs[field] = original
	}
	stored = nil
	plan(runtime.DecisionCreate)
}
