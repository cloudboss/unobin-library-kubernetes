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

func clusterRoleBindingLookupLibrary(t *testing.T) *runtime.Library {
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
	lookup := schema.DataSources["cluster-role-binding"]
	lookup.EvaluatedInputs = lookup.Inputs
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.cluster-role-binding": schema.DataSources["cluster-role-binding"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.cluster-role-binding": schema.DataSources["cluster-role-binding"].Defaults,
	}
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[
			clusterRoleBindingLookupSeed, *clusterRoleBindingLookupSeedOutput, any,
		](),
		"echo": runtime.MakeAction[
			clusterRoleBindingLookupEcho, *clusterRoleBindingLookupEchoOutput, any,
		](),
		"connection": runtime.MakeAction[
			clusterRoleBindingLookupConnection, *clusterRoleBindingLookupConnectionOutput, any,
		](),
	}
	lib.Resources["fixed"] = runtime.MakeResource[
		clusterRoleBindingLookupFixed, *clusterRoleBindingLookupFixed, any,
	](runtime.ResourceDefinition[
		clusterRoleBindingLookupFixed, *clusterRoleBindingLookupFixed, any,
	]{SchemaVersion: 1})
	return lib
}

func clusterRoleBindingLookupSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	file := filepath.Join("testdata/ub/cluster-role-binding-lookup", fixture, "factory.ub")
	body, err := os.ReadFile(file)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(file, body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func clusterRoleBindingLookupExecutor(
	t *testing.T, lib *runtime.Library, fixture string,
) *runtime.Executor {
	t.Helper()
	body := clusterRoleBindingLookupSyntax(t, fixture)
	libs := map[string]*runtime.Library{"rbac": lib, "helper": lib}
	store, err := local.NewStore(t.TempDir(), "cluster-role-binding-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Store: store,
		Factory: state.FactoryInfo{
			Name: "cluster-role-binding-lookup", Version: "v0", ContentRevision: "c0",
		}}
}

func clusterRoleBindingLookupSaved(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

type clusterRoleBindingLookupHTTP struct {
	sync.Mutex
	obj      *rbacv1.ClusterRoleBinding
	requests []string
	url      string
}

func newClusterRoleBindingLookupHTTP(
	t *testing.T, obj *rbacv1.ClusterRoleBinding,
) *clusterRoleBindingLookupHTTP {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	s := &clusterRoleBindingLookupHTTP{obj: obj}
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

func (s *clusterRoleBindingLookupHTTP) calls() []string {
	s.Lock()
	defer s.Unlock()
	out := slices.Clone(s.requests)
	s.requests = nil
	return out
}

func (s *clusterRoleBindingLookupHTTP) put(obj *rbacv1.ClusterRoleBinding) {
	s.Lock()
	defer s.Unlock()
	s.obj = obj
}

type clusterRoleBindingLookupSeed struct {
	Metadata svc.ClusterRoleBindingLookupMetadata
}
type clusterRoleBindingLookupSeedOutput struct {
	Value svc.ClusterRoleBindingLookupMetadata
}

func (s *clusterRoleBindingLookupSeed) Run(
	context.Context, any,
) (*clusterRoleBindingLookupSeedOutput, error) {
	return &clusterRoleBindingLookupSeedOutput{Value: s.Metadata}, nil
}

type clusterRoleBindingLookupFixed struct{ Value string }

func (r *clusterRoleBindingLookupFixed) Create(
	context.Context, any,
) (*clusterRoleBindingLookupFixed, error) {
	return r, nil
}
func (r *clusterRoleBindingLookupFixed) Read(
	_ context.Context, _ any,
	prior runtime.Prior[clusterRoleBindingLookupFixed, *clusterRoleBindingLookupFixed, any],
) (*clusterRoleBindingLookupFixed, error) {
	return prior.Outputs, nil
}
func (r *clusterRoleBindingLookupFixed) Update(
	context.Context, any, runtime.Prior[
		clusterRoleBindingLookupFixed, *clusterRoleBindingLookupFixed, any,
	],
) (*clusterRoleBindingLookupFixed, error) {
	return r, nil
}
func (r *clusterRoleBindingLookupFixed) Delete(
	context.Context, any, runtime.Prior[
		clusterRoleBindingLookupFixed, *clusterRoleBindingLookupFixed, any,
	],
) error {
	return nil
}

const clusterRoleBindingLookupRequest = "GET /apis/rbac.authorization.k8s.io/v1/" +
	"clusterrolebindings/reader "

func TestClusterRoleBindingLookupPublicCompiler(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	for _, fixture := range []string{
		"valid", "colon-valid", "upper-valid", "dotted-valid", "long-valid",
		"metadata-required-invalid", "metadata-null-invalid", "metadata-type-invalid",
		"namespace-output-invalid", "name-required-invalid",
		"name-null-invalid", "name-type-invalid", "namespace-invalid", "labels-invalid",
		"annotations-invalid", "generate-name-invalid", "rules-invalid",
		"role-ref-invalid", "subjects-invalid",
		"spec-invalid", "status-invalid",
		"empty-invalid", "dot-invalid", "dotdot-invalid", "slash-invalid", "percent-invalid",
	} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(clusterRoleBindingLookupSyntax(t, fixture),
				map[string]*runtime.Library{"rbac": lib}, nil, "")
			refs, rules := checker.References(nil).Err(), checker.LiteralConstraints().Err()
			if strings.HasSuffix(fixture, "-valid") || fixture == "valid" {
				require.NoError(t, refs)
				require.NoError(t, rules)
				return
			}
			if slices.Contains([]string{
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

func TestClusterRoleBindingLookupEvaluatedInputs(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	for _, tc := range []struct {
		name       string
		metadata   any
		diagnostic string
	}{
		{"minimal", map[string]any{"name": "reader"}, ""},
		{"colon", map[string]any{"name": "system:Reader"}, ""},
		{"uppercase", map[string]any{"name": "UPPER"}, ""},
		{"dotted", map[string]any{"name": "a..b"}, ""},
		{"long", map[string]any{"name": strings.Repeat("a", 254)}, ""},
		{"null metadata", nil, "required but is null"},
		{"wrong metadata", "reader", "expected object"},
		{"missing name", map[string]any{}, "required but not provided"},
		{"null name", map[string]any{"name": nil}, "required but is null"},
		{"wrong name", map[string]any{"name": int64(3)}, "expected string"},
		{"namespace", map[string]any{"name": "reader", "namespace": "default"}, "unknown field"},
		{"labels", map[string]any{"name": "reader", "labels": map[string]any{}}, "unknown field"},
		{"annotations", map[string]any{"name": "reader", "annotations": map[string]any{}},
			"unknown field"},
		{"generate name", map[string]any{"name": "reader", "generate-name": "a-"}, "unknown field"},
		{"empty", map[string]any{"name": ""}, "constraints[0]"},
		{"dot", map[string]any{"name": "."}, "constraints[0]"},
		{"dotdot", map[string]any{"name": ".."}, "constraints[0]"},
		{"slash", map[string]any{"name": "a/b"}, "constraints[0]"},
		{"percent", map[string]any{"name": "a%b"}, "constraints[0]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newClusterRoleBindingLookupHTTP(t, &rbacv1.ClusterRoleBinding{})
			e := clusterRoleBindingLookupExecutor(t, lib, "settled")
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
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/clusterrolebindings/" + m["name"].(string) + " ",
			}, h.calls())
		})
	}
}

func TestClusterRoleBindingLookupDeferredInputs(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	for _, name := range []string{"reader", "", ".", "..", "a/b", "a%b"} {
		tc := struct {
			metadata map[string]any
			rule     string
		}{
			metadata: map[string]any{"name": name},
		}
		if name != "reader" {
			tc.rule = "constraints[0]"
		}
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			h := newClusterRoleBindingLookupHTTP(t, &rbacv1.ClusterRoleBinding{})
			e := clusterRoleBindingLookupExecutor(t, lib, "deferred")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, h.calls())
			_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				require.Empty(t, h.calls())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
		})
	}
}

func TestClusterRoleBindingLookupSteadySavedInputs(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	for _, name := range []string{"reader", "", ".", "..", "a/b", "a%b"} {
		t.Run(name, func(t *testing.T) {
			h := newClusterRoleBindingLookupHTTP(t, &rbacv1.ClusterRoleBinding{})
			e := clusterRoleBindingLookupExecutor(t, lib, "steady")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
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
			require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
			if name != "reader" {
				invalid := maps.Clone(e.Inputs)
				metadata := map[string]any{"name": "reader"}
				metadata["name"] = name
				invalid["metadata"] = metadata
				e.Inputs = invalid
				_, err = e.Plan(t.Context())
				rule := "constraints[0]"
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+rule)
				require.Empty(t, h.calls())
				e.Inputs = nil
				plan.Inputs = invalid
				_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+rule)
				require.Empty(t, h.calls())
				return
			}
			_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
		})
	}
}

func TestClusterRoleBindingLookupObservations(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	for _, change := range []string{
		"stable", "name", "generate-name", "uid", "resource-version", "generation",
		"labels", "annotations", "ref-api-group", "ref-kind", "ref-name", "subject-kind",
		"subject-api-group", "subject-name", "subject-namespace", "subject-order", "subject-duplicates",
		"disappear", "appear", "missing-to-empty", "empty-to-content",
	} {
		t.Run(change, func(t *testing.T) {
			obj := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
				Name: "observed", GenerateName: "prefix-", UID: "uid",
				ResourceVersion: "1", Generation: 2, Labels: map[string]string{"key": "value"},
				Annotations: map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "true"},
			}, RoleRef: rbacv1.RoleRef{APIGroup: "odd", Kind: "Role", Name: "ref"},
				Subjects: []rbacv1.Subject{
					{Kind: "User", Name: "z"}, {Kind: "ServiceAccount", Name: "sa"},
					{Kind: "User", Name: "z"},
				}}
			h := newClusterRoleBindingLookupHTTP(t, obj)
			switch change {
			case "appear", "missing-to-empty":
				h.put(nil)
			case "empty-to-content":
				h.put(&rbacv1.ClusterRoleBinding{})
			}
			e := clusterRoleBindingLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			file := clusterRoleBindingLookupSaved(t, plan)
			changed := obj.DeepCopy()
			switch change {
			case "name":
				changed.Name = "changed"
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
				changed = &rbacv1.ClusterRoleBinding{}
			}
			h.put(changed)
			result, err := e.ApplyPlan(t.Context(), file)
			if change == "stable" {
				require.NoError(t, err)
				require.Equal(t, map[string]any{
					"metadata": map[string]any{
						"name": "observed", "generate-name": "prefix-",
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
			require.Equal(t, []string{
				clusterRoleBindingLookupRequest, clusterRoleBindingLookupRequest,
			}, h.calls())
			fresh, err := e.Plan(t.Context())
			require.NoError(t, err)
			result, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, fresh))
			require.NoError(t, err)
			var observed map[string]any
			for _, step := range fresh.Steps {
				if step.Address == "data-source.endpoint" {
					observed = step.ObservedOutputs
				}
			}
			require.Equal(t, result.Outputs, observed)
			require.Equal(t, []string{
				clusterRoleBindingLookupRequest, clusterRoleBindingLookupRequest,
			}, h.calls())
		})
	}
}

func TestClusterRoleBindingLookupRemovalAndDestroy(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint(destroy), func(t *testing.T) {
			lib := clusterRoleBindingLookupLibrary(t)
			h := newClusterRoleBindingLookupHTTP(t, &rbacv1.ClusterRoleBinding{})
			e := clusterRoleBindingLookupExecutor(t, lib, "steady")
			e.Inputs = map[string]any{"server": h.url,
				"metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			request := "GET /apis/rbac.authorization.k8s.io/v1/clusterrolebindings/reader "
			require.Equal(t, []string{request}, h.calls())
			prior, err := e.Store.Current()
			require.NoError(t, err)
			prior.Find("data-source.endpoint").Inputs["metadata"] = map[string]any{"name": "bad/%"}
			revision, err := e.Store.Write(prior)
			require.NoError(t, err)
			require.NoError(t, e.Store.SetCurrent(revision))
			if destroy {
				e.Destroy = true
			} else {
				removed := clusterRoleBindingLookupExecutor(t, lib, "removed")
				removed.Store, removed.Inputs = e.Store, e.Inputs
				e = removed
			}
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			result, err := e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			require.Empty(t, result.Data)
			require.Empty(t, result.Outputs)
			saved, err := e.Store.Current()
			require.NoError(t, err)
			require.Nil(t, saved.Find("data-source.endpoint"))
			require.Empty(t, h.calls())
		})
	}
}

func TestClusterRoleBindingLookupAPIGate(t *testing.T) {
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

func TestClusterRoleBindingLookupChangingDependency(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	h := newClusterRoleBindingLookupHTTP(t, &rbacv1.ClusterRoleBinding{})
	e := clusterRoleBindingLookupExecutor(t, lib, "dependency")
	e.Inputs = map[string]any{"server": h.url,
		"metadata": map[string]any{"name": "reader"}, "value": "first"}
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, h.calls())
	_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
	e.Inputs["value"] = "changed"
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, h.calls())
	_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
	e.Inputs["value"] = "again"
	e.Inputs["metadata"] = map[string]any{"name": ".."}
	_, err = e.Plan(t.Context())
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[0]")
	require.Empty(t, h.calls())
}

type clusterRoleBindingLookupConnection struct{ Server string }
type clusterRoleBindingLookupConnectionOutput struct{ ResolvedServer string }

func (s *clusterRoleBindingLookupConnection) Run(
	context.Context, any,
) (*clusterRoleBindingLookupConnectionOutput, error) {
	return &clusterRoleBindingLookupConnectionOutput{ResolvedServer: s.Server}, nil
}

type clusterRoleBindingLookupEcho struct{ Value any }
type clusterRoleBindingLookupEchoOutput struct{ Resolved any }

func (s *clusterRoleBindingLookupEcho) Run(
	context.Context, any,
) (*clusterRoleBindingLookupEchoOutput, error) {
	return &clusterRoleBindingLookupEchoOutput{Resolved: s.Value}, nil
}

func TestClusterRoleBindingLookupSavedAndDeferredStructure(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	for _, tc := range []struct {
		metadata   any
		diagnostic string
	}{
		{nil, "required but is null"},
		{"reader", "expected object"},
		{map[string]any{}, "required but not provided"},
		{map[string]any{"name": nil}, "required but is null"},
		{map[string]any{"name": false}, "expected string"},
		{map[string]any{"name": "reader", "namespace": "default"}, "unknown field"},
		{map[string]any{"name": "reader", "labels": map[string]any{}}, "unknown field"},
		{map[string]any{"name": "reader", "annotations": map[string]any{}}, "unknown field"},
		{map[string]any{"name": "reader", "generate-name": "a-"}, "unknown field"},
	} {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			h := newClusterRoleBindingLookupHTTP(t, &rbacv1.ClusterRoleBinding{})
			e := clusterRoleBindingLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
			e.Inputs = nil
			plan.Inputs["metadata"] = tc.metadata
			_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, h.calls())
			e = clusterRoleBindingLookupExecutor(t, lib, "deferred-structure")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, h.calls())
			_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, h.calls())
		})
	}
}

func TestClusterRoleBindingLookupDeferredConfiguration(t *testing.T) {
	lib := clusterRoleBindingLookupLibrary(t)
	for _, name := range []string{"reader", "", ".", "..", "a/b", "a%b"} {
		t.Run(name, func(t *testing.T) {
			h := newClusterRoleBindingLookupHTTP(t, &rbacv1.ClusterRoleBinding{})
			e := clusterRoleBindingLookupExecutor(t, lib, "deferred-config")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": name}}
			plan, err := e.Plan(t.Context())
			require.Empty(t, h.calls())
			if name != "reader" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[0]")
				return
			}
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), clusterRoleBindingLookupSaved(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleBindingLookupRequest}, h.calls())
		})
	}
}
