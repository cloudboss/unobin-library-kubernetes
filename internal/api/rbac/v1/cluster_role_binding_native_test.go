package v1

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func clusterBindingExecutor(
	t *testing.T, fixture string, library *runtime.Library, inputs map[string]any, dir string,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/" + fixture)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"rbac": library}
	store, err := local.NewStore(dir, "binding", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "binding", Version: "v0", ContentRevision: "c0"},
	}
}

func clusterBindingLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	lib := &runtime.Library{
		Name: "kubernetes-rbac-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"cluster-role-binding": clusterRoleBindingResourceRegistration(),
		},
	}
	addNativeRBACConstraints(t, lib)
	return lib
}

func applyClusterBindingPlan(t *testing.T, exec *runtime.Executor, plan *runtime.Plan) error {
	t.Helper()
	encoded, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), file)
	return err
}

func TestClusterRoleBindingSavedNoOpConstraints(t *testing.T) {
	var stored *rbacv1.ClusterRoleBinding
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		switch req.Method {
		case http.MethodPost:
			stored = &rbacv1.ClusterRoleBinding{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.UID = "uid"
		case http.MethodGet:
			require.NotNil(t, stored)
		case http.MethodDelete:
			stored = nil
			respond(t, w, metav1.Status{Status: "Success"})
			return
		default:
			t.Errorf("unexpected request: %s", req.Method)
		}
		respond(t, w, stored)
	})
	lib := clusterBindingLibrary(t)
	metadata := map[string]any{"name": "system:reader"}
	inputs := map[string]any{"server": *c.Server, "metadata": metadata, "subject-name": "alice"}
	dir := t.TempDir()
	executor := func() *runtime.Executor {
		return clusterBindingExecutor(t, "cluster-role-binding-name-state/factory.ub",
			lib, inputs, dir)
	}
	exec := executor()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, applyClusterBindingPlan(t, exec, plan))
	exec = executor()
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, plan.Steps[len(plan.Steps)-1].Decision)
	require.NoError(t, applyClusterBindingPlan(t, exec, plan))

	delete(metadata, "name")
	constraints := lib.Constraints
	lib.Constraints = nil
	exec = executor()
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, plan.Steps[len(plan.Steps)-1].Decision)
	lib.Constraints = constraints
	before := len(calls)
	err = applyClusterBindingPlan(t, exec, plan)
	require.ErrorContains(t, err, "schema: constraints[")
	require.ErrorContains(t, err, "resource.binding")
	require.Empty(t, calls[before:])
	_, err = executor().Plan(t.Context())
	require.ErrorContains(t, err, "schema: constraints[")

	lib.Constraints = nil
	exec = executor()
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, applyClusterBindingPlan(t, exec, plan))
	lib.Constraints = constraints

	// Historical state can still be observed and destroyed with saved identity.
	destroyer := executor()
	destroyer.Destroy = true
	plan, err = destroyer.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range plan.Steps {
		if step.Address == "resource.binding" {
			require.Empty(t, step.PriorInputs["metadata"])
		}
	}
	require.NoError(t, applyClusterBindingPlan(t, destroyer, plan))
	require.Nil(t, stored)
	require.Equal(t, http.MethodDelete, calls[len(calls)-1])
}

func TestClusterRoleBindingDeferredConstraints(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "valid"}[valid], func(t *testing.T) {
			objects := map[string]*rbacv1.ClusterRoleBinding{}
			var creates []string
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				name := filepath.Base(req.URL.Path)
				if req.Method == http.MethodPost {
					obj := &rbacv1.ClusterRoleBinding{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
					creates = append(creates, obj.GenerateName)
					obj.Name = obj.GenerateName + "actual"
					obj.UID = types.UID(obj.Name)
					if obj.GenerateName == "seed-" && valid {
						obj.Subjects[0].APIGroup = "resolved-"
					}
					objects[obj.Name] = obj
					name = obj.Name
				} else {
					require.Equal(t, http.MethodGet, req.Method)
				}
				respond(t, w, objects[name])
			})
			exec := clusterBindingExecutor(t, "cluster-role-binding-state/pending.ub",
				clusterBindingLibrary(t), map[string]any{"server": *c.Server}, t.TempDir())
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, creates)
			pending := false
			for _, step := range plan.Steps {
				if step.Address == "resource.binding" {
					require.Contains(t, step.UnresolvedInputs, "metadata")
					pending = true
				}
			}
			require.True(t, pending)
			err = applyClusterBindingPlan(t, exec, plan)
			if valid {
				require.NoError(t, err)
				require.Equal(t, []string{"seed-", "resolved-"}, creates)
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
				require.ErrorContains(t, err, "metadata.generate-name must not be empty")
				require.ErrorContains(t, err, "resource.binding")
				require.Equal(t, []string{"seed-"}, creates)
			}
		})
	}
}
