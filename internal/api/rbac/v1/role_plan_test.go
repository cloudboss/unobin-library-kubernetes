package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
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

func TestRoleStateBackedPlanApply(t *testing.T) {
	var stored *rbacv1.Role
	var calls []string
	var credentials []string
	failure := 0
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		credentials = append(credentials, req.Header.Get("Authorization"))
		if failure != 0 {
			w.WriteHeader(failure)
			respond(t, w, metav1.Status{Status: "Failure", Code: int32(failure),
				Reason: metav1.StatusReasonForbidden, Message: "rejected"})
			return
		}
		switch req.Method {
		case http.MethodPost:
			stored = &rbacv1.Role{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			if stored.Name == "" {
				stored.Name = stored.GenerateName + "generated"
			}
			stored.UID = "uid"
		case http.MethodGet:
			if stored == nil {
				w.WriteHeader(http.StatusNotFound)
				respond(t, w, metav1.Status{Status: "Failure", Code: 404,
					Reason: metav1.StatusReasonNotFound, Message: "absent"})
				return
			}
		case http.MethodPatch:
			var operations []rolePatchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			body, err := json.Marshal(operations)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			body, err = json.Marshal(stored)
			require.NoError(t, err)
			body, err = patch.Apply(body)
			require.NoError(t, err)
			stored = &rbacv1.Role{}
			require.NoError(t, json.Unmarshal(body, stored))
		case http.MethodDelete:
			stored = nil
			respond(t, w, metav1.Status{Status: "Success"})
			return
		default:
			t.Errorf("unexpected method: %s", req.Method)
		}
		respond(t, w, stored)
	})
	body, err := os.ReadFile("testdata/ub/state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	lib := &runtime.Library{
		Name: "kubernetes-rbac-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"role": roleResourceRegistration(),
		},
	}
	addNativeRBACConstraints(t, lib)
	libs := map[string]*runtime.Library{"rbac": lib}
	store, err := local.NewStore(t.TempDir(), "role", "test", encrypters.Noop{})
	require.NoError(t, err)
	r := validRole()
	inputs := func() map[string]any {
		encoded, err := ub.Marshal(r)
		require.NoError(t, err)
		var values map[string]any
		require.NoError(t, ub.Unmarshal(encoded, &values))
		return map[string]any{"server": *c.Server, "token": "first",
			"metadata": values["metadata"], "rule": values["rule"]}
	}
	executor := func(in map[string]any) *runtime.Executor {
		return &runtime.Executor{
			DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body,
			Libraries:    libs, Inputs: in, Store: store,
			Factory: state.FactoryInfo{Name: "role", Version: "v0", ContentRevision: "c0"},
		}
	}
	plan := func(
		want runtime.Decision, change func(map[string]any),
	) (*runtime.Executor, *runtime.Plan) {
		t.Helper()
		in := inputs()
		if change != nil {
			change(in)
		}
		exec := executor(in)
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.reader" {
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
	exec, p := plan(runtime.DecisionCreate, nil)
	apply(exec, p)
	require.Equal(t, "system:reader-generated", stored.Name)
	plan(runtime.DecisionNoOp, nil)
	r.Rule[0].Verbs = []string{"get", "list"}
	r.Rule[0].ResourceNames = &[]string{}
	r.Metadata.Namespace = new("default")
	r.Metadata.Annotations, r.Metadata.Labels = &map[string]string{}, &map[string]string{}
	plan(runtime.DecisionNoOp, nil)
	stored.Generation, stored.ResourceVersion = 99, "changed"
	stored.CreationTimestamp = metav1.Now()
	plan(runtime.DecisionNoOp, nil)
	r.Rule[0].ResourceNames = &[]string{"one"}
	exec, p = plan(runtime.DecisionUpdate, nil)
	apply(exec, p)
	require.Equal(t, "uid", string(stored.UID))
	require.Equal(t, []string{"one"}, stored.Rules[0].ResourceNames)
	r.Rule[0].ResourceNames = nil
	exec, p = plan(runtime.DecisionUpdate, nil)
	apply(exec, p)
	require.Empty(t, stored.Rules[0].ResourceNames)
	plan(runtime.DecisionNoOp, nil)
	stored.Rules[0].Verbs = []string{"watch"}
	exec, p = plan(runtime.DecisionUpdate, nil)
	apply(exec, p)
	require.Equal(t, []string{"get", "list"}, stored.Rules[0].Verbs)
	plan(runtime.DecisionNoOp, nil)
	plan(runtime.DecisionReplace, func(in map[string]any) {
		in["metadata"].(map[string]any)["namespace"] = "team"
	})
	plan(runtime.DecisionReplace, func(in map[string]any) {
		in["metadata"].(map[string]any)["generate-name"] = "other-"
	})
	plan(runtime.DecisionReplace, func(in map[string]any) {
		in["server"] = "https://unused.invalid"
	})
	plan(runtime.DecisionNoOp, func(in map[string]any) { in["token"] = "current" })
	require.Contains(t, credentials, "Bearer current")
	stored.UID = "replacement"
	_, err = executor(inputs()).Plan(t.Context())
	require.ErrorContains(t, err, "UID changed")
	stored.UID = "uid"
	failure = http.StatusForbidden
	_, err = executor(inputs()).Plan(t.Context())
	require.ErrorContains(t, err, "read Role")
	failure = 0
	stored = nil
	lastMutation := len(calls)
	plan(runtime.DecisionCreate, nil)
	require.Equal(t, []string{"GET"}, calls[lastMutation:])
}

func TestRoleStateBackedNameRestoration(t *testing.T) {
	var stored *rbacv1.Role
	var calls []string
	creates := 0
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		switch req.Method {
		case http.MethodPost:
			stored = &rbacv1.Role{}
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
			stored = &rbacv1.Role{}
			require.NoError(t, json.Unmarshal(body, stored))
		case http.MethodDelete:
			stored = nil
			respond(t, w, metav1.Status{Status: "Success"})
			return
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		respond(t, w, stored)
	})
	body, err := os.ReadFile("testdata/ub/state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	reg := roleResourceRegistration()
	libs := map[string]*runtime.Library{"rbac": {
		Name: "kubernetes-rbac-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{"role": reg},
	}}
	addNativeRBACConstraints(t, libs["rbac"])
	dir := t.TempDir()
	metadata := map[string]any{"name": "system:reader"}
	rules := []any{map[string]any{"api-groups": []any{""}, "resources": []any{"pods"},
		"verbs": []any{"get"}}}
	inputs := map[string]any{"server": *c.Server, "token": "first",
		"metadata": metadata, "rule": rules}
	plan := func(want runtime.Decision, apply bool) {
		t.Helper()
		store, err := local.NewStore(dir, "role", "test", encrypters.Noop{})
		require.NoError(t, err)
		exec := &runtime.Executor{
			DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
			Libraries: libs, Inputs: inputs, Store: store,
			Factory: state.FactoryInfo{Name: "role", Version: "v0", ContentRevision: "c0"},
		}
		p, err := exec.Plan(t.Context())
		if want == "invalid" {
			require.ErrorContains(t, err, "schema: constraints[")
			require.ErrorContains(t, err, "metadata.name")
			return
		}
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.reader" {
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
	plan("invalid", false)
	metadata["name"] = "system:reader"
	rules[0].(map[string]any)["verbs"] = []any{"get", "list"}
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []string{"get", "list"}, stored.Rules[0].Verbs)
	require.Equal(t, "uid-1", string(stored.UID))
	plan(runtime.DecisionNoOp, true)
	metadata["name"] = "system:renamed"
	plan(runtime.DecisionReplace, true)
	require.Equal(t, "system:renamed", stored.Name)
	require.Equal(t, "uid-2", string(stored.UID))
}
