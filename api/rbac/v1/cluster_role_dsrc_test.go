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

func clusterRoleLookupLibrary(t *testing.T) *runtime.Library {
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
	schema.DataSources["cluster-role"].EvaluatedInputs = schema.DataSources["cluster-role"].Inputs
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.cluster-role": schema.DataSources["cluster-role"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.cluster-role": schema.DataSources["cluster-role"].Defaults,
	}
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[clusterRoleLookupSeed, *clusterRoleLookupSeedOutput, any](),
		"echo": runtime.MakeAction[clusterRoleLookupEcho, *clusterRoleLookupEchoOutput, any](),
		"connection": runtime.MakeAction[
			clusterRoleLookupConnection, *clusterRoleLookupConnectionOutput, any,
		](),
	}
	lib.Resources["fixed"] = runtime.MakeResource[
		clusterRoleLookupFixed, *clusterRoleLookupFixed, any,
	](runtime.ResourceDefinition[
		clusterRoleLookupFixed, *clusterRoleLookupFixed, any,
	]{SchemaVersion: 1})
	return lib
}

func clusterRoleLookupSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	file := filepath.Join("testdata/ub/cluster-role-lookup", fixture, "factory.ub")
	body, err := os.ReadFile(file)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(file, body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func clusterRoleLookupExecutor(
	t *testing.T, lib *runtime.Library, fixture string,
) *runtime.Executor {
	t.Helper()
	body := clusterRoleLookupSyntax(t, fixture)
	libs := map[string]*runtime.Library{"rbac": lib, "helper": lib}
	store, err := local.NewStore(t.TempDir(), "cluster-role-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "cluster-role-lookup", Version: "v0", ContentRevision: "c0"}}
}

func clusterRoleLookupSaved(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

type clusterRoleLookupHTTP struct {
	sync.Mutex
	obj      *rbacv1.ClusterRole
	requests []string
	url      string
}

func newClusterRoleLookupHTTP(t *testing.T, obj *rbacv1.ClusterRole) *clusterRoleLookupHTTP {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	s := &clusterRoleLookupHTTP{obj: obj}
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

func (s *clusterRoleLookupHTTP) calls() []string {
	s.Lock()
	defer s.Unlock()
	out := slices.Clone(s.requests)
	s.requests = nil
	return out
}

func (s *clusterRoleLookupHTTP) put(obj *rbacv1.ClusterRole) {
	s.Lock()
	defer s.Unlock()
	s.obj = obj
}

type clusterRoleLookupSeed struct{ Metadata svc.ClusterRoleLookupMetadata }
type clusterRoleLookupSeedOutput struct{ Value svc.ClusterRoleLookupMetadata }

func (s *clusterRoleLookupSeed) Run(context.Context, any) (*clusterRoleLookupSeedOutput, error) {
	return &clusterRoleLookupSeedOutput{Value: s.Metadata}, nil
}

type clusterRoleLookupFixed struct{ Value string }

func (r *clusterRoleLookupFixed) Create(context.Context, any) (*clusterRoleLookupFixed, error) {
	return r, nil
}
func (r *clusterRoleLookupFixed) Read(
	_ context.Context, _ any,
	prior runtime.Prior[clusterRoleLookupFixed, *clusterRoleLookupFixed, any],
) (*clusterRoleLookupFixed, error) {
	return prior.Outputs, nil
}
func (r *clusterRoleLookupFixed) Update(
	context.Context, any, runtime.Prior[clusterRoleLookupFixed, *clusterRoleLookupFixed, any],
) (*clusterRoleLookupFixed, error) {
	return r, nil
}
func (r *clusterRoleLookupFixed) Delete(
	context.Context, any, runtime.Prior[clusterRoleLookupFixed, *clusterRoleLookupFixed, any],
) error {
	return nil
}

const clusterRoleLookupRequest = "GET /apis/rbac.authorization.k8s.io/v1/clusterroles/reader "

func TestClusterRoleLookupPublicCompiler(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
	for _, fixture := range []string{
		"valid", "colon-valid", "upper-valid", "dotted-valid", "long-valid",
		"metadata-required-invalid", "metadata-null-invalid", "metadata-type-invalid",
		"namespace-output-invalid", "name-required-invalid",
		"name-null-invalid", "name-type-invalid", "namespace-invalid", "labels-invalid",
		"annotations-invalid", "generate-name-invalid", "rules-invalid", "aggregation-rule-invalid",
		"spec-invalid", "status-invalid",
		"empty-invalid", "dot-invalid", "dotdot-invalid", "slash-invalid", "percent-invalid",
	} {
		t.Run(fixture, func(t *testing.T) {
			checker := check.NewSyntax(clusterRoleLookupSyntax(t, fixture),
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

func TestClusterRoleLookupEvaluatedInputs(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
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
			h := newClusterRoleLookupHTTP(t, &rbacv1.ClusterRole{})
			e := clusterRoleLookupExecutor(t, lib, "settled")
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
				"GET /apis/rbac.authorization.k8s.io/v1/clusterroles/" + m["name"].(string) + " ",
			}, h.calls())
		})
	}
}

func TestClusterRoleLookupDeferredInputs(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
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
			h := newClusterRoleLookupHTTP(t, &rbacv1.ClusterRole{})
			e := clusterRoleLookupExecutor(t, lib, "deferred")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, h.calls())
			_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				require.Empty(t, h.calls())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
		})
	}
}

func TestClusterRoleLookupSteadySavedInputs(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
	for _, name := range []string{"reader", "", ".", "..", "a/b", "a%b"} {
		t.Run(name, func(t *testing.T) {
			h := newClusterRoleLookupHTTP(t, &rbacv1.ClusterRole{})
			e := clusterRoleLookupExecutor(t, lib, "steady")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
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
			require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
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
				_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+rule)
				require.Empty(t, h.calls())
				return
			}
			_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
		})
	}
}

func TestClusterRoleLookupObservations(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
	for _, change := range []string{
		"stable", "name", "generate-name", "uid", "resource-version", "generation",
		"labels", "annotations", "api-groups", "resources", "verbs", "resource-names",
		"non-resource-urls", "rule-order", "member-order", "rule-duplicates",
		"api-groups-order", "resources-order", "resource-names-order", "urls-order",
		"disappear", "appear", "missing-to-empty",
	} {
		t.Run(change, func(t *testing.T) {
			obj := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
				Name: "observed", GenerateName: "prefix-", UID: "uid",
				ResourceVersion: "1", Generation: 2, Labels: map[string]string{"key": "value"},
				Annotations: map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "true"},
			}, Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"", "z"}, Resources: []string{"pods", "*"},
					Verbs: []string{"watch", "get"}, ResourceNames: []string{"z", "a"},
					NonResourceURLs: []string{"/z", "/a"}},
				{Verbs: []string{"odd"}},
			}}
			h := newClusterRoleLookupHTTP(t, obj)
			if change == "appear" || change == "missing-to-empty" {
				h.put(nil)
			}
			e := clusterRoleLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			file := clusterRoleLookupSaved(t, plan)
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
			case "api-groups":
				changed.Rules[0].APIGroups = []string{"changed"}
			case "resources":
				changed.Rules[0].Resources = []string{"changed"}
			case "verbs":
				changed.Rules[0].Verbs = []string{"changed"}
			case "resource-names":
				changed.Rules[0].ResourceNames = []string{"changed"}
			case "non-resource-urls":
				changed.Rules[0].NonResourceURLs = []string{"/changed"}
			case "rule-order":
				slices.Reverse(changed.Rules)
			case "member-order":
				slices.Reverse(changed.Rules[0].Verbs)
			case "rule-duplicates":
				changed.Rules = append(changed.Rules, changed.Rules[0])
			case "api-groups-order":
				slices.Reverse(changed.Rules[0].APIGroups)
			case "resources-order":
				slices.Reverse(changed.Rules[0].Resources)
			case "resource-names-order":
				slices.Reverse(changed.Rules[0].ResourceNames)
			case "urls-order":
				slices.Reverse(changed.Rules[0].NonResourceURLs)
			case "disappear":
				changed = nil
			case "missing-to-empty":
				changed = &rbacv1.ClusterRole{}
			}
			h.put(changed)
			result, err := e.ApplyPlan(t.Context(), file)
			if change == "stable" {
				require.NoError(t, err)
				require.Equal(t, map[string]any{
					"metadata": map[string]any{"name": "observed",
						"generate-name": "prefix-", "uid": "uid", "resource-version": "1",
						"generation": int64(2), "labels": map[string]any{"key": "value"},
						"annotations": map[string]any{"rbac.authorization.kubernetes.io/autoupdate": "true"}},
					"rules": []any{
						map[string]any{"api-groups": []any{"", "z"}, "resources": []any{"pods", "*"},
							"verbs": []any{"watch", "get"}, "resource-names": []any{"z", "a"},
							"non-resource-urls": []any{"/z", "/a"}},
						map[string]any{"api-groups": []any{}, "resources": []any{}, "verbs": []any{"odd"},
							"resource-names": []any{}, "non-resource-urls": []any{}},
					},
					"aggregation-rule": nil,
				}, result.Outputs)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			require.Equal(t, []string{clusterRoleLookupRequest, clusterRoleLookupRequest}, h.calls())
			fresh, err := e.Plan(t.Context())
			require.NoError(t, err)
			result, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, fresh))
			require.NoError(t, err)
			var observed map[string]any
			for _, step := range fresh.Steps {
				if step.Address == "data-source.endpoint" {
					observed = step.ObservedOutputs
				}
			}
			require.Equal(t, observed, result.Outputs)
			require.Equal(t, []string{clusterRoleLookupRequest, clusterRoleLookupRequest}, h.calls())
		})
	}
}

func TestClusterRoleLookupRemovalAndDestroy(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint(destroy), func(t *testing.T) {
			lib := clusterRoleLookupLibrary(t)
			h := newClusterRoleLookupHTTP(t, &rbacv1.ClusterRole{})
			e := clusterRoleLookupExecutor(t, lib, "steady")
			e.Inputs = map[string]any{"server": h.url,
				"metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			require.NoError(t, err)
			request := "GET /apis/rbac.authorization.k8s.io/v1/clusterroles/reader "
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
				removed := clusterRoleLookupExecutor(t, lib, "removed")
				removed.Store, removed.Inputs = e.Store, e.Inputs
				e = removed
			}
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			result, err := e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
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

func TestClusterRoleLookupAPIGate(t *testing.T) {
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

func TestClusterRoleLookupChangingDependency(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
	h := newClusterRoleLookupHTTP(t, &rbacv1.ClusterRole{})
	e := clusterRoleLookupExecutor(t, lib, "dependency")
	e.Inputs = map[string]any{"server": h.url,
		"metadata": map[string]any{"name": "reader"}, "value": "first"}
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, h.calls())
	_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
	e.Inputs["value"] = "changed"
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, h.calls())
	_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
	e.Inputs["value"] = "again"
	e.Inputs["metadata"] = map[string]any{"name": ".."}
	_, err = e.Plan(t.Context())
	require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[0]")
	require.Empty(t, h.calls())
}

type clusterRoleLookupConnection struct{ Server string }
type clusterRoleLookupConnectionOutput struct{ ResolvedServer string }

func (s *clusterRoleLookupConnection) Run(
	context.Context, any,
) (*clusterRoleLookupConnectionOutput, error) {
	return &clusterRoleLookupConnectionOutput{ResolvedServer: s.Server}, nil
}

type clusterRoleLookupEcho struct{ Value any }
type clusterRoleLookupEchoOutput struct{ Resolved any }

func (s *clusterRoleLookupEcho) Run(context.Context, any) (*clusterRoleLookupEchoOutput, error) {
	return &clusterRoleLookupEchoOutput{Resolved: s.Value}, nil
}

func TestClusterRoleLookupSavedAndDeferredStructure(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
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
			h := newClusterRoleLookupHTTP(t, &rbacv1.ClusterRole{})
			e := clusterRoleLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
			e.Inputs = nil
			plan.Inputs["metadata"] = tc.metadata
			_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, h.calls())
			e = clusterRoleLookupExecutor(t, lib, "deferred-structure")
			e.Inputs = map[string]any{"server": h.url, "metadata": tc.metadata}
			plan, err = e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, h.calls())
			_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, h.calls())
		})
	}
}

func TestClusterRoleLookupDeferredConfiguration(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
	for _, name := range []string{"reader", "", ".", "..", "a/b", "a%b"} {
		t.Run(name, func(t *testing.T) {
			h := newClusterRoleLookupHTTP(t, &rbacv1.ClusterRole{})
			e := clusterRoleLookupExecutor(t, lib, "deferred-config")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": name}}
			plan, err := e.Plan(t.Context())
			require.Empty(t, h.calls())
			if name != "reader" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[0]")
				return
			}
			require.NoError(t, err)
			_, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{clusterRoleLookupRequest}, h.calls())
		})
	}
}

func TestClusterRoleLookupAggregationObservations(t *testing.T) {
	lib := clusterRoleLookupLibrary(t)
	for _, change := range []string{
		"stable", "remove", "empty", "appear", "empty-appear", "label", "selector-order",
		"expression-order", "key", "operator", "values", "value-order", "duplicates",
	} {
		t.Run(change, func(t *testing.T) {
			obj := &rbacv1.ClusterRole{AggregationRule: &rbacv1.AggregationRule{
				ClusterRoleSelectors: []metav1.LabelSelector{
					{MatchLabels: map[string]string{"key": "value"},
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "first", Operator: "Future", Values: []string{"z", "a", "z"}},
							{Key: "second", Operator: "", Values: []string{}},
						}},
					{},
				},
			}}
			h := newClusterRoleLookupHTTP(t, obj)
			if change == "appear" || change == "empty-appear" {
				h.put(&rbacv1.ClusterRole{})
			}
			e := clusterRoleLookupExecutor(t, lib, "settled")
			e.Inputs = map[string]any{"server": h.url, "metadata": map[string]any{"name": "reader"}}
			plan, err := e.Plan(t.Context())
			require.NoError(t, err)
			changed := obj.DeepCopy()
			selectors := changed.AggregationRule.ClusterRoleSelectors
			switch change {
			case "remove":
				changed.AggregationRule = nil
			case "empty", "empty-appear":
				changed.AggregationRule = &rbacv1.AggregationRule{}
			case "label":
				selectors[0].MatchLabels["key"] = "changed"
			case "selector-order":
				slices.Reverse(selectors)
			case "expression-order":
				slices.Reverse(selectors[0].MatchExpressions)
			case "key":
				selectors[0].MatchExpressions[0].Key = "changed"
			case "operator":
				selectors[0].MatchExpressions[0].Operator = "changed"
			case "values":
				selectors[0].MatchExpressions[0].Values = []string{"changed"}
			case "value-order":
				selectors[0].MatchExpressions[0].Values = []string{"a", "z", "z"}
			case "duplicates":
				selectors[0].MatchExpressions[0].Values = []string{"z", "a"}
			}
			h.put(changed)
			result, err := e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, plan))
			if change == "stable" {
				require.NoError(t, err)
				require.Equal(t, map[string]any{
					"cluster-role-selectors": []any{
						map[string]any{"match-labels": map[string]any{"key": "value"},
							"match-expressions": []any{
								map[string]any{"key": "first", "operator": "Future", "values": []any{"z", "a", "z"}},
								map[string]any{"key": "second", "operator": "", "values": []any{}},
							}},
						map[string]any{"match-labels": map[string]any{}, "match-expressions": []any{}},
					},
				}, result.Outputs["aggregation-rule"])
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			require.Equal(t, []string{clusterRoleLookupRequest, clusterRoleLookupRequest}, h.calls())
			fresh, err := e.Plan(t.Context())
			require.NoError(t, err)
			result, err = e.ApplyPlan(t.Context(), clusterRoleLookupSaved(t, fresh))
			require.NoError(t, err)
			var observed map[string]any
			for _, step := range fresh.Steps {
				if step.Address == "data-source.endpoint" {
					observed = step.ObservedOutputs
				}
			}
			require.Equal(t, observed, result.Outputs)
			require.Equal(t, []string{clusterRoleLookupRequest, clusterRoleLookupRequest}, h.calls())
		})
	}
}
