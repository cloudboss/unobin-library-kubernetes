package v1

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestSecretStateBackedMissingPlan(t *testing.T) {
	status := 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "GET" && status != 0 {
			namespaceStatus(t, w, status)
			return
		}
		obj := liveSecret()
		if req.Method == "POST" {
			var input corev1.Secret
			require.NoError(t, json.NewDecoder(req.Body).Decode(&input))
			for key, value := range input.StringData {
				if obj.Data == nil {
					obj.Data = map[string][]byte{}
				}
				obj.Data[key] = []byte(value)
			}
		}
		respond(t, w, obj)
	})
	source, err := os.ReadFile("testdata/ub/secret-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": {
		Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{"secret": runtime.MakeResource[
			SecretResource, *SecretResourceOutput, *config.Configuration,
		]((&SecretResource{}).ResourceDefinition())},
	}}
	store, err := local.NewStore(t.TempDir(), "secret", "test", encrypters.Noop{})
	require.NoError(t, err)
	plan := func() (*runtime.Executor, *runtime.Plan, error) {
		executor := &runtime.Executor{
			DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
			Libraries: libs, Inputs: map[string]any{"server": *c.Server}, Store: store,
			Factory: state.FactoryInfo{Name: "secret", Version: "v0", ContentRevision: "c0"},
		}
		p, err := executor.Plan(t.Context())
		return executor, p, err
	}
	executor, p, err := plan()
	require.NoError(t, err)
	encoded, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	status = 404
	_, p, err = plan()
	require.NoError(t, err)
	found := false
	for _, step := range p.Steps {
		if step.Address == "resource.credentials" {
			require.Equal(t, runtime.DecisionCreate, step.Decision)
			found = true
		}
	}
	require.True(t, found)
	status = 403
	_, _, err = plan()
	require.Error(t, err)
}
