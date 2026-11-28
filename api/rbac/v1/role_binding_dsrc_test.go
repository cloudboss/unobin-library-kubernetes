package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
)

func roleBindingLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin")
	module, err := cmd.Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	schema.DataSources["role-binding"].EvaluatedInputs = schema.DataSources["role-binding"].Inputs
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.role-binding": schema.DataSources["role-binding"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.role-binding": schema.DataSources["role-binding"].Defaults,
	}
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[roleBindingLookupSeed, *roleBindingLookupSeedOutput, any](),
		"echo": runtime.MakeAction[roleBindingLookupEcho, *roleBindingLookupEchoOutput, any](),
		"connection": runtime.MakeAction[
			roleBindingLookupConnection, *roleBindingLookupConnectionOutput, any,
		](),
	}
	lib.Resources["fixed"] = runtime.MakeResource[
		roleBindingLookupFixed, *roleBindingLookupFixed, any,
	](runtime.ResourceDefinition[
		roleBindingLookupFixed, *roleBindingLookupFixed, any,
	]{SchemaVersion: 1})
	return lib
}

func roleBindingLookupSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	file := filepath.Join("testdata/ub/role-binding-lookup", fixture, "factory.ub")
	body, err := os.ReadFile(file)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(file, body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func roleBindingLookupExecutor(
	t *testing.T, lib *runtime.Library, fixture string,
) *runtime.Executor {
	t.Helper()
	body := roleBindingLookupSyntax(t, fixture)
	libs := map[string]*runtime.Library{"rbac": lib, "helper": lib}
	store, err := local.NewStore(t.TempDir(), "role-binding-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "role-binding-lookup", Version: "v0", ContentRevision: "c0"}}
}

func roleBindingLookupSaved(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

type roleBindingLookupHTTP struct {
	sync.Mutex
	obj      *rbacv1.RoleBinding
	requests []string
	url      string
}

func newRoleBindingLookupHTTP(t *testing.T, obj *rbacv1.RoleBinding) *roleBindingLookupHTTP {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	s := &roleBindingLookupHTTP{obj: obj}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Lock()
		defer s.Unlock()
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		if s.obj == nil {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Reason: metav1.StatusReasonNotFound, Code: 404,
			}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(s.obj))
	}))
	t.Cleanup(server.Close)
	s.url = server.URL
	return s
}

func (s *roleBindingLookupHTTP) calls() []string {
	s.Lock()
	defer s.Unlock()
	out := slices.Clone(s.requests)
	s.requests = nil
	return out
}

func (s *roleBindingLookupHTTP) put(obj *rbacv1.RoleBinding) {
	s.Lock()
	defer s.Unlock()
	s.obj = obj
}

type roleBindingLookupSeed struct{ Metadata svc.RoleBindingLookupMetadata }
type roleBindingLookupSeedOutput struct{ Value svc.RoleBindingLookupMetadata }

func (s *roleBindingLookupSeed) Run(context.Context, any) (*roleBindingLookupSeedOutput, error) {
	return &roleBindingLookupSeedOutput{Value: s.Metadata}, nil
}

type roleBindingLookupFixed struct{ Value string }

func (r *roleBindingLookupFixed) Create(context.Context, any) (*roleBindingLookupFixed, error) {
	return r, nil
}
func (r *roleBindingLookupFixed) Read(
	_ context.Context, _ any, prior runtime.Prior[
		roleBindingLookupFixed, *roleBindingLookupFixed, any,
	],
) (*roleBindingLookupFixed, error) {
	return prior.Outputs, nil
}
func (r *roleBindingLookupFixed) Update(
	context.Context, any, runtime.Prior[roleBindingLookupFixed, *roleBindingLookupFixed, any],
) (*roleBindingLookupFixed, error) {
	return r, nil
}
func (r *roleBindingLookupFixed) Delete(
	context.Context, any, runtime.Prior[roleBindingLookupFixed, *roleBindingLookupFixed, any],
) error {
	return nil
}

const roleBindingLookupRequest = "GET /apis/rbac.authorization.k8s.io/v1/namespaces/default/" +
	"rolebindings/reader "

func TestRoleBindingLookupPublicCompiler(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	for _, fixture := range []string{
		"valid", "null-namespace-valid", "colon-valid", "upper-valid", "dotted-valid", "long-valid",
		"namespace-min-valid", "namespace-max-valid",
		"metadata-required-invalid", "metadata-null-invalid", "name-required-invalid",
		"name-null-invalid", "name-type-invalid", "namespace-type-invalid", "labels-invalid",
		"annotations-invalid", "generate-name-invalid", "rules-invalid", "role-ref-invalid",
		"subjects-invalid", "spec-invalid", "status-invalid",
		"empty-invalid", "dot-invalid", "dotdot-invalid", "slash-invalid", "percent-invalid",
		"namespace-empty-invalid", "namespace-upper-invalid", "namespace-dot-invalid",
		"namespace-slash-invalid", "namespace-leading-invalid", "namespace-trailing-invalid",
		"namespace-long-invalid",
	} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(roleBindingLookupSyntax(t, fixture),
				map[string]*runtime.Library{"rbac": lib, "helper": lib}, nil, "")
			refs, rules := checker.References(nil).Err(), checker.LiteralConstraints().Err()
			if strings.HasSuffix(fixture, "-valid") || fixture == "valid" {
				require.NoError(t, refs)
				require.NoError(t, rules)
				return
			}
			if strings.HasPrefix(fixture, "namespace-") &&
				fixture != "namespace-type-invalid" {
				require.ErrorContains(t, rules, "data-source.endpoint")
				require.ErrorContains(t, rules, "constraints[1]")
			} else if slices.Contains([]string{
				"empty-invalid", "dot-invalid", "dotdot-invalid", "slash-invalid", "percent-invalid",
			}, fixture) {
				require.ErrorContains(t, rules, "data-source.endpoint")
				require.ErrorContains(t, rules, "constraints[0]")
			} else {
				require.Error(t, refs)
			}
		})
	}
}

func TestRoleBindingLookupEvaluatedInputs(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	for _, tc := range []struct {
		name       string
		metadata   any
		diagnostic string
	}{
		{"minimal", map[string]any{"name": "reader"}, ""},
		{"null namespace", map[string]any{"name": "reader", "namespace": nil}, ""},
		{"colon", map[string]any{"name": "system:Reader"}, ""},
		{"uppercase", map[string]any{"name": "UPPER"}, ""},
		{"dotted", map[string]any{"name": "a..b"}, ""},
		{"long", map[string]any{"name": strings.Repeat("a", 254)}, ""},
		{"minimum namespace", map[string]any{"name": "reader", "namespace": "a"}, ""},
		{"maximum namespace", map[string]any{"name": "reader", "namespace": strings.Repeat("a", 63)}, ""},
		{"null metadata", nil, "required but is null"},
		{"wrong metadata", "reader", "expected object"},
		{"missing name", map[string]any{}, "required but not provided"},
		{"null name", map[string]any{"name": nil}, "required but is null"},
		{"wrong name", map[string]any{"name": int64(3)}, "expected string"},
		{"wrong namespace", map[string]any{"name": "reader", "namespace": true}, "expected string"},
		{"labels", map[string]any{"name": "reader", "labels": map[string]any{}}, "unknown field"},
		{"annotations", map[string]any{"name": "reader", "annotations": map[string]any{}},
			"unknown field"},
		{"generate name", map[string]any{"name": "reader", "generate-name": "a-"}, "unknown field"},
		{"empty", map[string]any{"name": ""}, "constraints[0]"},
		{"dot", map[string]any{"name": "."}, "constraints[0]"},
		{"dotdot", map[string]any{"name": ".."}, "constraints[0]"},
		{"slash", map[string]any{"name": "a/b"}, "constraints[0]"},
		{"percent", map[string]any{"name": "a%b"}, "constraints[0]"},
		{"empty namespace", map[string]any{"name": "reader", "namespace": ""}, "constraints[1]"},
		{"upper namespace", map[string]any{"name": "reader", "namespace": "UPPER"}, "constraints[1]"},
		{"dotted namespace", map[string]any{"name": "reader", "namespace": "a.b"}, "constraints[1]"},
		{"slash namespace", map[string]any{"name": "reader", "namespace": "a/b"}, "constraints[1]"},
		{"leading namespace", map[string]any{"name": "reader", "namespace": "-a"}, "constraints[1]"},
		{"trailing namespace", map[string]any{"name": "reader", "namespace": "a-"}, "constraints[1]"},
		{"long namespace", map[string]any{"name": "reader", "namespace": strings.Repeat("a", 64)},
			"constraints[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRoleBindingLookupHTTP(t, &rbacv1.RoleBinding{})
			e := roleBindingLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			_, err := e.Plan(t.Context())
			if tc.diagnostic != "" {
				require.ErrorContains(t, err, tc.diagnostic)
				if strings.HasPrefix(tc.diagnostic, "constraints[") {
					require.ErrorContains(t, err, "data-source.endpoint: schema:")
				}
				require.Empty(t, h.calls())
				return
			}
			require.NoError(t, err)
			m := tc.metadata.(map[string]any)
			ns := "default"
			if m["namespace"] != nil {
				ns = m["namespace"].(string)
			}
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/namespaces/" + ns +
					"/rolebindings/" + m["name"].(string) + " ",
			}, h.calls())
		})
	}
}

func TestRoleBindingLookupDeferredInputs(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	for _, tc := range roleBindingLookupDeferredCases() {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			h := newRoleBindingLookupHTTP(t, &rbacv1.RoleBinding{})
			e := roleBindingLookupExecutor(t, lib, "deferred")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, h.calls())
			_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				require.Empty(t, h.calls())
				return
			}
			require.NoError(t, err)
			ns := "default"
			if tc.metadata["namespace"] != nil {
				ns = tc.metadata["namespace"].(string)
			}
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/namespaces/" + ns + "/rolebindings/reader ",
			}, h.calls())
		})
	}
}

func TestRoleBindingLookupSteadySavedInputs(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	for _, tc := range roleBindingLookupDeferredCases() {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			h := newRoleBindingLookupHTTP(t, &rbacv1.RoleBinding{})
			e := roleBindingLookupExecutor(t, lib, "steady")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{roleBindingLookupRequest}, h.calls())
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			decisions := map[string]runtime.Decision{}
			for _, step := range plan.Steps {
				if step.Address == "resource.fixed" || step.Address == "data-source.endpoint" {
					decisions[step.Address] = step.Decision
				}
			}
			require.Equal(t, map[string]runtime.Decision{
				"resource.fixed": runtime.DecisionNoOp, "data-source.endpoint": runtime.DecisionRead,
			}, decisions)
			require.Equal(t, []string{roleBindingLookupRequest}, h.calls())
			desired := maps.Clone(e.Inputs)
			desired["metadata"] = tc.metadata
			e.Inputs = desired
			_, err = e.Plan(t.Context())
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				require.Empty(t, h.calls())
			} else {
				require.NoError(t, err)
				ns := "default"
				if tc.metadata["namespace"] != nil {
					ns = tc.metadata["namespace"].(string)
				}
				require.Equal(t, []string{
					"GET /apis/rbac.authorization.k8s.io/v1/namespaces/" + ns + "/rolebindings/reader ",
				}, h.calls())
			}
			e.Inputs = nil
			plan.Inputs = desired
			_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				require.Empty(t, h.calls())
				return
			}
			require.NoError(t, err)
			ns := "default"
			if tc.metadata["namespace"] != nil {
				ns = tc.metadata["namespace"].(string)
			}
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/namespaces/" + ns + "/rolebindings/reader ",
			}, h.calls())
		})
	}
}

func TestRoleBindingLookupRemovalAndDestroy(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint(destroy), func(t *testing.T) {
			lib := roleBindingLookupLibrary(t)
			rules := lib.Constraints
			lib.Constraints = nil
			h := newRoleBindingLookupHTTP(t, &rbacv1.RoleBinding{})
			e := roleBindingLookupExecutor(t, lib, "steady")
			e.Inputs = map[string]any{"server": h.url,
				"metadata": map[string]any{"name": "reader", "namespace": "UPPER"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			request := "GET /apis/rbac.authorization.k8s.io/v1/namespaces/UPPER/rolebindings/reader "
			require.Equal(t, []string{request}, h.calls())
			lib.Constraints = rules
			if destroy {
				e.Destroy = true
			} else {
				removed := roleBindingLookupExecutor(t, lib, "removed")
				removed.Store, removed.Inputs = e.Store, e.Inputs
				e = removed
			}
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			result, err := e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			require.Empty(t, result.Data)
			require.Empty(t, result.Outputs)
			current, err := e.Store.Current()
			require.NoError(t, err)
			require.NotNil(t, current)
			require.Nil(t, current.Find("data-source.endpoint"))
			require.Empty(t, h.calls())
		})
	}
}

func TestRoleBindingLookupAPIGate(t *testing.T) {
	descriptor := libraryapi.Descriptor{FormatVersion: 1, ImplementedAPIs: []string{"1.0"},
		GeneratorAPI: "1.0"}
	compatibility, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &descriptor})
	require.NoError(t, err)
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(compatibility,
		func(string) (*runtime.LibrarySchema, []string, error) {
			t.Error("schema extraction ran before API gate")
			return nil, nil, nil
		}, nil)
	_, _, err = cache.Read(".")
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
}

func TestRoleBindingLookupChangingDependency(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	h := newRoleBindingLookupHTTP(t, &rbacv1.RoleBinding{})
	e := roleBindingLookupExecutor(t, lib, "dependency")
	e.Inputs = map[string]any{"server": h.url,
		"metadata": map[string]any{"name": "reader"}, "value": "first"}
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, h.calls())
	_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{roleBindingLookupRequest}, h.calls())
	e.Inputs["value"] = "changed"
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, h.calls())
	_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{roleBindingLookupRequest}, h.calls())
	e.Inputs["value"] = "again"
	e.Inputs["metadata"] = map[string]any{"name": ".."}
	_, err = e.Plan(t.Context())
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[0]")
	require.Empty(t, h.calls())
}

func roleBindingLookupDeferredCases() []struct {
	metadata map[string]any
	rule     string
} {
	cases := []struct {
		metadata map[string]any
		rule     string
	}{
		{map[string]any{"name": "reader"}, ""},
		{map[string]any{"name": "reader", "namespace": nil}, ""},
		{map[string]any{"name": "reader", "namespace": "selected"}, ""},
	}
	for _, name := range []string{"", ".", "..", "a/b", "a%b"} {
		cases = append(cases, struct {
			metadata map[string]any
			rule     string
		}{map[string]any{"name": name}, "constraints[0]"})
	}
	for _, namespace := range []string{"", "UPPER", "a.b", "a/b", "-a", "a-",
		strings.Repeat("a", 64)} {
		cases = append(cases, struct {
			metadata map[string]any
			rule     string
		}{map[string]any{"name": "reader", "namespace": namespace}, "constraints[1]"})
	}
	return cases
}

func TestRoleBindingLookupObservations(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	for _, change := range []string{
		"stable", "name", "namespace", "generate-name", "uid", "resource-version", "generation",
		"labels", "annotations", "ref-api-group", "ref-kind", "ref-name", "subject-kind",
		"subject-api-group", "subject-name", "subject-namespace", "subject-order", "subject-duplicates",
		"disappear", "appear", "missing-to-empty", "empty-to-content",
	} {
		t.Run(change, func(t *testing.T) {
			obj := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
				Name: "observed", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
				ResourceVersion: "1", Generation: 2, Labels: map[string]string{"key": "value"},
				Annotations: map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "true"},
			}, RoleRef: rbacv1.RoleRef{APIGroup: "odd", Kind: "Role", Name: "ref"},
				Subjects: []rbacv1.Subject{
					{Kind: "User", Name: "z"}, {Kind: "ServiceAccount", Name: "sa"},
					{Kind: "User", Name: "z"},
				}}
			h := newRoleBindingLookupHTTP(t, obj)
			switch change {
			case "appear", "missing-to-empty":
				h.put(nil)
			case "empty-to-content":
				h.put(&rbacv1.RoleBinding{})
			}
			e := roleBindingLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			file := roleBindingLookupSaved(t, plan)
			changed := obj.DeepCopy()
			switch change {
			case "name":
				changed.Name = "changed"
			case "namespace":
				changed.Namespace = "changed"
			case "generate-name":
				changed.GenerateName = "changed-"
			case "uid":
				changed.UID = "changed"
			case "resource-version":
				changed.ResourceVersion = "2"
			case "generation":
				changed.Generation = 3
			case "labels":
				changed.Labels["key"] = "changed"
			case "annotations":
				changed.Annotations["rbac.authorization.kubernetes.io/autoupdate"] = "false"
			case "ref-api-group":
				changed.RoleRef.APIGroup = "changed"
			case "ref-kind":
				changed.RoleRef.Kind = "ClusterRole"
			case "ref-name":
				changed.RoleRef.Name = "changed"
			case "subject-kind":
				changed.Subjects[0].Kind = "changed"
			case "subject-api-group":
				changed.Subjects[0].APIGroup = "changed"
			case "subject-name":
				changed.Subjects[0].Name = "changed"
			case "subject-namespace":
				changed.Subjects[0].Namespace = "changed"
			case "subject-order":
				changed.Subjects[0], changed.Subjects[1] =
					changed.Subjects[1], changed.Subjects[0]
			case "subject-duplicates":
				changed.Subjects = changed.Subjects[:2]
			case "disappear":
				changed = nil
			case "missing-to-empty":
				changed = &rbacv1.RoleBinding{}
			}
			h.put(changed)
			result, err := e.ApplyPlan(t.Context(), file)
			if change == "stable" {
				require.NoError(t, err)
				require.Equal(t, map[string]any{
					"metadata": map[string]any{
						"name": "observed", "namespace": "actual", "generate-name": "prefix-",
						"uid": "uid", "resource-version": "1", "generation": int64(2),
						"labels":      map[string]any{"key": "value"},
						"annotations": map[string]any{"rbac.authorization.kubernetes.io/autoupdate": "true"},
					},
					"role-ref": map[string]any{"api-group": "odd", "kind": "Role", "name": "ref"},
					"subjects": []any{
						map[string]any{"kind": "User", "api-group": "", "name": "z", "namespace": ""},
						map[string]any{"kind": "ServiceAccount", "api-group": "", "name": "sa", "namespace": ""},
						map[string]any{"kind": "User", "api-group": "", "name": "z", "namespace": ""},
					},
				}, result.Outputs)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			require.Equal(t, []string{roleBindingLookupRequest, roleBindingLookupRequest}, h.calls())
			fresh, err := e.Plan(t.Context())
			require.NoError(t, err)
			result, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, fresh))
			require.NoError(t, err)
			var observed map[string]any
			for _, step := range fresh.Steps {
				if step.Address == "data-source.endpoint" {
					observed = step.ObservedOutputs
				}
			}
			require.Equal(t, result.Outputs, observed)
			require.Equal(t, []string{roleBindingLookupRequest, roleBindingLookupRequest}, h.calls())
		})
	}
}

type roleBindingLookupConnection struct{ Server string }
type roleBindingLookupConnectionOutput struct{ ResolvedServer string }

func (s *roleBindingLookupConnection) Run(
	context.Context, any,
) (*roleBindingLookupConnectionOutput, error) {
	return &roleBindingLookupConnectionOutput{ResolvedServer: s.Server}, nil
}

func TestRoleBindingLookupDeferredConfiguration(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	for _, tc := range roleBindingLookupDeferredCases() {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			h := newRoleBindingLookupHTTP(t, &rbacv1.RoleBinding{})
			e := roleBindingLookupExecutor(t, lib, "deferred-config")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			plan, err := e.Plan(t.Context())
			require.Empty(t, h.calls())
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				return
			}
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			ns := "default"
			if tc.metadata["namespace"] != nil {
				ns = tc.metadata["namespace"].(string)
			}
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/namespaces/" + ns + "/rolebindings/reader ",
			}, h.calls())
		})
	}
}

type roleBindingLookupEcho struct{ Value any }
type roleBindingLookupEchoOutput struct{ Resolved any }

func (s *roleBindingLookupEcho) Run(context.Context, any) (*roleBindingLookupEchoOutput, error) {
	return &roleBindingLookupEchoOutput{Resolved: s.Value}, nil
}

func TestRoleBindingLookupSavedAndDeferredStructure(t *testing.T) {
	lib := roleBindingLookupLibrary(t)
	for _, tc := range []struct {
		metadata   any
		diagnostic string
	}{
		{nil, "required but is null"},
		{"reader", "expected object"},
		{map[string]any{}, "required but not provided"},
		{map[string]any{"name": nil}, "required but is null"},
		{map[string]any{"name": false}, "expected string"},
		{map[string]any{"name": "reader", "namespace": false}, "expected string"},
		{map[string]any{"name": "reader", "labels": map[string]any{}}, "unknown field"},
		{map[string]any{"name": "reader", "annotations": map[string]any{}}, "unknown field"},
		{map[string]any{"name": "reader", "generate-name": "a-"}, "unknown field"},
	} {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			h := newRoleBindingLookupHTTP(t, &rbacv1.RoleBinding{})
			e := roleBindingLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{roleBindingLookupRequest}, h.calls())
			e.Inputs = nil
			plan.Inputs["metadata"] = tc.metadata
			_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, h.calls())
			e = roleBindingLookupExecutor(t, lib, "deferred-structure")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, h.calls())
			_, err = e.ApplyPlan(t.Context(), roleBindingLookupSaved(t, plan))
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, h.calls())
		})
	}
}
