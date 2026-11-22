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

func TestNamespaceStateBackedPlanApply(t *testing.T) {
	var stored *corev1.Namespace
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		switch req.Method {
		case "POST":
			stored = liveNamespace()
			var desired corev1.Namespace
			require.NoError(t, json.NewDecoder(req.Body).Decode(&desired))
			stored.Labels = desired.Labels
		case "PATCH":
			encoded, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(encoded)
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
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		respond(t, w, stored)
	})
	source, err := os.ReadFile("testdata/ub/namespace-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": {
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"namespace": runtime.MakeResource[
				NamespaceResource, *NamespaceResourceOutput, *config.Configuration,
			]((&NamespaceResource{}).ResourceDefinition()),
		},
	}}
	addNativeCoreConstraints(t, libs["core"])
	store, err := local.NewStore(t.TempDir(), "namespace", "test", encrypters.Noop{})
	require.NoError(t, err)
	plan := func(value string, waiting bool, want runtime.Decision) (
		*runtime.Executor, *runtime.Plan,
	) {
		t.Helper()
		exec := &runtime.Executor{
			DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs,
			Inputs: map[string]any{"server": *c.Server, "value": value, "wait": waiting},
			Store:  store, Factory: state.FactoryInfo{
				Name: "namespace", Version: "v0", ContentRevision: "c0",
			},
		}
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.project" {
				require.Equal(t, want, step.Decision)
				found = true
			}
		}
		require.True(t, found)
		return exec, p
	}
	apply := func(exec *runtime.Executor, p *runtime.Plan) {
		t.Helper()
		encoded, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
	}
	exec, p := plan("first", false, runtime.DecisionCreate)
	apply(exec, p)
	uid := stored.UID
	plan("first", false, runtime.DecisionNoOp)
	stored.Generation = 10
	stored.ResourceVersion = "new"
	stored.CreationTimestamp = metav1.Now()
	stored.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller"}}
	stored.Status = corev1.NamespaceStatus{Phase: corev1.NamespaceActive}
	stored.Annotations = map[string]string{"kubernetes.io/internal": "hidden", "ignored/key": "hidden"}
	plan("first", false, runtime.DecisionNoOp)
	exec, p = plan("first", true, runtime.DecisionUpdate)
	apply(exec, p)
	exec, p = plan("updated", true, runtime.DecisionUpdate)
	apply(exec, p)
	require.Equal(t, uid, stored.UID)
	require.Equal(t, map[string]string{"value": "updated"}, stored.Labels)
	require.Equal(t, map[string]string{
		"kubernetes.io/internal": "hidden", "ignored/key": "hidden",
	}, stored.Annotations)
	plan("updated", true, runtime.DecisionNoOp)
	for _, call := range calls {
		require.NotContains(t, call, "/serviceaccounts/")
	}
}
