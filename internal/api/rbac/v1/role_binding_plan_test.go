package v1

import (
	"encoding/json"
	"fmt"
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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestRoleBindingStateBackedPlanApply(t *testing.T) {
	var stored *rbacv1.RoleBinding
	var calls, credentials []string
	var patches [][]rolePatchOperation
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		credentials = append(credentials, req.Header.Get("Authorization"))
		switch req.Method {
		case http.MethodPost:
			stored = &rbacv1.RoleBinding{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.Name, stored.UID = stored.GenerateName+"actual", "uid"
		case http.MethodGet:
			if stored == nil {
				bindingFailure(t, w, 404)
				return
			}
		case http.MethodPatch:
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			var ops []rolePatchOperation
			require.NoError(t, json.Unmarshal(body, &ops))
			patches = append(patches, ops)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			body, err = json.Marshal(stored)
			require.NoError(t, err)
			body, err = patch.Apply(body)
			require.NoError(t, err)
			stored = &rbacv1.RoleBinding{}
			require.NoError(t, json.Unmarshal(body, stored))
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		for i := range stored.Subjects {
			if stored.Subjects[i].APIGroup == "" && stored.Subjects[i].Kind != "ServiceAccount" {
				stored.Subjects[i].APIGroup = rbacv1.GroupName
			}
		}
		respond(t, w, stored)
	})
	body, err := os.ReadFile("testdata/ub/role-binding-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	lib := &runtime.Library{
		Name: "kubernetes-rbac-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"role-binding": roleBindingResourceRegistration(),
		},
	}
	libs := map[string]*runtime.Library{"rbac": lib}
	store, err := local.NewStore(t.TempDir(), "binding", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := map[string]any{
		"server": *c.Server, "token": "first", "prefix": "system:reader-",
		"binding-namespace": "default", "subject-kind": "User", "subject-name": "alice",
		"subject-namespace": "default", "ref-kind": "Role", "ref-name": "reader",
		"labels": map[string]any{},
	}
	executor := func() *runtime.Executor {
		return &runtime.Executor{
			DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
			Libraries: libs, Inputs: inputs, Store: store,
			Factory: state.FactoryInfo{Name: "binding", Version: "v0", ContentRevision: "c0"},
		}
	}
	plan := func(want runtime.Decision, apply bool) {
		t.Helper()
		exec := executor()
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.binding" {
				require.Equal(t, want, step.Decision)
				found = true
			}
		}
		require.True(t, found)
		if apply {
			encoded, err := runtime.EncodePlan(p)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(encoded)
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), file)
			require.NoError(t, err)
		}
	}
	plan(runtime.DecisionCreate, true)
	plan(runtime.DecisionNoOp, false)
	stored.ResourceVersion, stored.Generation, stored.CreationTimestamp = "changed", 99, metav1.Now()
	plan(runtime.DecisionNoOp, false)
	inputs["token"] = "current"
	plan(runtime.DecisionNoOp, false)
	require.Contains(t, credentials, "Bearer current")
	inputs["labels"] = map[string]any{"app": "binding"}
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []rolePatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: "uid"},
		{Op: "add", Path: "/metadata/labels", Value: map[string]any{"app": "binding"}},
	}, patches[len(patches)-1])
	plan(runtime.DecisionNoOp, false)
	inputs["subject-namespace"] = "other"
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, rbacv1.GroupName, stored.Subjects[0].APIGroup)
	inputs["subject-kind"], inputs["subject-name"] = "ServiceAccount", "account"
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, rbacv1.Subject{Kind: "ServiceAccount", Name: "account", Namespace: "other"},
		stored.Subjects[0])
	require.Equal(t, "uid", string(stored.UID))
	plan(runtime.DecisionNoOp, false)
	stored.Subjects[0].Name = "drift"
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, "account", stored.Subjects[0].Name)
	stored.RoleRef.Name = "drift"
	plan(runtime.DecisionReplace, false)
	stored.RoleRef.Name = "reader"
	for _, field := range []string{"ref-name", "prefix", "binding-namespace", "server"} {
		old := inputs[field]
		inputs[field] = "changed"
		plan(runtime.DecisionReplace, false)
		inputs[field] = old
	}
	stored.UID = "replacement"
	_, err = executor().Plan(t.Context())
	require.ErrorContains(t, err, "UID changed")
	stored = nil
	before := len(calls)
	plan(runtime.DecisionCreate, false)
	require.Equal(t, []string{"GET"}, calls[before:])
}

func TestRoleBindingStateBackedNameRestoration(t *testing.T) {
	var stored *rbacv1.RoleBinding
	var calls []string
	creates := 0
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		switch req.Method {
		case http.MethodPost:
			stored = &rbacv1.RoleBinding{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			creates++
			stored.UID = types.UID(fmt.Sprintf("uid-%d", creates))
		case http.MethodGet:
			require.NotNil(t, stored)
		case http.MethodPatch:
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			body, err = json.Marshal(stored)
			require.NoError(t, err)
			body, err = patch.Apply(body)
			require.NoError(t, err)
			stored = &rbacv1.RoleBinding{}
			require.NoError(t, json.Unmarshal(body, stored))
		case http.MethodDelete:
			stored = nil
			respond(t, w, metav1.Status{Status: "Success"})
			return
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		stored.Subjects[0].APIGroup = rbacv1.GroupName
		respond(t, w, stored)
	})
	body, err := os.ReadFile("testdata/ub/role-binding-name-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	reg := roleBindingResourceRegistration()
	libs := map[string]*runtime.Library{"rbac": {
		Name: "kubernetes-rbac-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{"role-binding": reg},
	}}
	dir := t.TempDir()
	metadata := map[string]any{"name": "system:reader"}
	inputs := map[string]any{"server": *c.Server, "metadata": metadata, "subject-name": "alice"}
	plan := func(want runtime.Decision, apply bool) {
		t.Helper()
		store, err := local.NewStore(dir, "binding", "test", encrypters.Noop{})
		require.NoError(t, err)
		exec := &runtime.Executor{
			DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
			Libraries: libs, Inputs: inputs, Store: store,
			Factory: state.FactoryInfo{Name: "binding", Version: "v0", ContentRevision: "c0"},
		}
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.binding" {
				require.Equal(t, want, step.Decision)
				found = true
			}
		}
		require.True(t, found)
		if apply {
			encoded, err := runtime.EncodePlan(p)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(encoded)
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), file)
			require.NoError(t, err)
		}
	}
	plan(runtime.DecisionCreate, true)
	delete(metadata, "name")
	inputs["subject-name"] = "bob"
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, "bob", stored.Subjects[0].Name)
	metadata["name"] = "system:reader"
	before := len(calls)
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []string{"GET", "PATCH"}, calls[before:])
	require.Equal(t, "uid-1", string(stored.UID))
	plan(runtime.DecisionNoOp, false)
	metadata["name"] = "system:renamed"
	plan(runtime.DecisionReplace, true)
	require.Equal(t, "system:renamed", stored.Name)
	require.Equal(t, "uid-2", string(stored.UID))
}
