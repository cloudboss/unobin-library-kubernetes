package v1

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type daemonSetLookupServer struct {
	mu    sync.Mutex
	obj   *appsv1.DaemonSet
	calls []string
	url   string
}

func (a *daemonSetLookupServer) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.calls
	a.calls = nil
	return calls
}

func newDaemonSetLookupServer(t *testing.T) *daemonSetLookupServer {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	api := &daemonSetLookupServer{obj: &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", UID: "uid"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if api.obj == nil {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Reason: metav1.StatusReasonNotFound, Code: 404,
			}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(api.obj))
	}))
	t.Cleanup(server.Close)
	api.url = server.URL
	return api
}

func daemonSetLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := restartSchema()
	require.NoError(t, err)
	lib := Library()
	copy := *schema
	copy.DataSources = maps.Clone(schema.DataSources)
	typ := *copy.DataSources["daemon-set"]
	typ.EvaluatedInputs = typ.Inputs
	copy.DataSources["daemon-set"] = &typ
	lib.Schema = &copy
	lib.Constraints = map[string][]lang.ConstraintSpec{"data-source.daemon-set": typ.Constraints}
	lib.Defaults = map[string][]lang.DefaultSpec{"data-source.daemon-set": typ.Defaults}
	return lib
}

func daemonSetLookupSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/daemon-set-lookup", fixture, "factory.ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func TestDaemonSetLookupPublicCompiler(t *testing.T) {
	lib := daemonSetLookupLibrary(t)
	for _, tc := range []struct{ fixture, reference, rule string }{
		{"valid", "", ""}, {"null-namespace-valid", "", ""}, {"maximums-valid", "", ""},
		{"required-invalid", "missing required field", ""},
		{"output-invalid", "unknown field \"unsupported\"", ""},
		{"null-name-invalid", "", "constraints[0]"},
		{"type-invalid", "type mismatch", ""}, {"namespace-type-invalid", "type mismatch", ""},
		{"extra-invalid", "", ""},
		{"empty-invalid", "", "constraints[0]"}, {"name-invalid", "", "constraints[0]"},
		{"slash-invalid", "", "constraints[0]"}, {"name-length-invalid", "", "constraints[0]"},
		{"namespace-invalid", "", "constraints[1]"},
		{"namespace-empty-invalid", "", "constraints[1]"},
		{"namespace-upper-invalid", "", "constraints[1]"},
		{"namespace-length-invalid", "", "constraints[1]"},
		{"metadata-required-invalid", "missing required input", ""},
		{"metadata-null-invalid", "", "constraints[0]"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			checker := check.NewSyntax(daemonSetLookupSyntax(t, tc.fixture),
				map[string]*runtime.Library{"apps": lib}, nil, "")
			refs, rules := checker.References(nil).Err(), checker.LiteralConstraints().Err()
			if tc.reference != "" {
				require.ErrorContains(t, refs, tc.reference)
				return
			}
			if tc.fixture == "null-name-invalid" || tc.fixture == "extra-invalid" ||
				tc.fixture == "metadata-null-invalid" {
				require.Error(t, refs)
			} else {
				require.NoError(t, refs)
			}
			if tc.rule != "" {
				require.ErrorContains(t, rules, "data-source.lookup")
				require.ErrorContains(t, rules, tc.rule)
			} else {
				require.NoError(t, rules)
			}
		})
	}
}

func daemonSetLookupExecutor(t *testing.T, lib *runtime.Library, dir, fixture string,
	inputs map[string]any) *runtime.Executor {
	t.Helper()
	lib.Resources["anchor"] = runtime.MakeResource[
		deploymentLookupAnchor, *deploymentLookupAnchorOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		deploymentLookupAnchor, *deploymentLookupAnchorOutput, *config.Configuration,
	]{SchemaVersion: 1})
	lib.Actions["seed"] = runtime.MakeAction[statusSeed, *statusSeedOutput, *config.Configuration]()
	body := daemonSetLookupSyntax(t, fixture)
	libs := map[string]*runtime.Library{"apps": lib, "remote": lib}
	store, err := local.NewStore(dir, "daemon-set-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "daemon-set-lookup", Version: "v0", ContentRevision: "c0"}}
}

func TestDaemonSetLookupNativeSteadyAndSaved(t *testing.T) {
	api := newDaemonSetLookupServer(t)
	lib, dir := daemonSetLookupLibrary(t), t.TempDir()
	inputs := map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}}
	e := daemonSetLookupExecutor(t, lib, dir, "steady", inputs)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	api.take()
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range p.Steps {
		if step.Address == "resource.anchor" {
			require.Equal(t, runtime.DecisionNoOp, step.Decision)
		}
	}
	require.NoError(t, restartSavedApply(t, e, p))
	get := "GET /apis/apps/v1/namespaces/default/daemonsets/example"
	require.Equal(t, []string{get, get}, api.take())
	for _, tc := range []struct {
		field      string
		value      any
		diagnostic string
	}{
		{"name", nil, "required but is null"}, {"name", false, "expected string"},
		{"name", "", "constraints[0]"}, {"name", "BAD", "constraints[0]"},
		{"name", "bad/name", "constraints[0]"},
		{"name", strings.Repeat("a", 254), "constraints[0]"},
		{"namespace", "", "constraints[1]"}, {"namespace", "bad.name", "constraints[1]"},
		{"namespace", "BAD", "constraints[1]"},
		{"namespace", strings.Repeat("a", 64), "constraints[1]"},
		{"namespace", false, "expected string"},
	} {
		t.Run(tc.field+"="+fmt.Sprint(tc.value), func(t *testing.T) {
			valid := daemonSetLookupExecutor(t, lib, dir, "steady", inputs)
			saved, err := valid.Plan(t.Context())
			require.NoError(t, err)
			api.take()
			metadata := map[string]any{"name": "example", tc.field: tc.value}
			bad := daemonSetLookupExecutor(t, lib, dir, "steady",
				map[string]any{"server": api.url, "metadata": metadata})
			_, err = bad.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.lookup")
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, api.take())
			err = restartSavedApply(t, bad, saved)
			require.ErrorContains(t, err, "data-source.lookup")
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, api.take())
		})
	}
}

func TestDaemonSetLookupNativeDeferred(t *testing.T) {
	for _, tc := range []struct {
		metadata   map[string]any
		diagnostic string
	}{
		{map[string]any{"name": "example"}, ""},
		{map[string]any{"name": "example", "namespace": nil}, ""},
		{map[string]any{"name": "example", "namespace": "selected"}, ""},
		{map[string]any{"name": "BAD"}, "constraints[0]"},
		{map[string]any{"name": nil}, "required but is null"},
		{map[string]any{"name": "example", "namespace": "bad.name"}, "constraints[1]"},
	} {
		t.Run(fmt.Sprint(tc.metadata), func(t *testing.T) {
			api := newDaemonSetLookupServer(t)
			e := daemonSetLookupExecutor(t, daemonSetLookupLibrary(t), t.TempDir(), "deferred",
				map[string]any{"server": api.url, "metadata": tc.metadata})
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, api.take())
			err = restartSavedApply(t, e, p)
			if tc.diagnostic != "" {
				require.ErrorContains(t, err, "data-source.lookup")
				require.ErrorContains(t, err, tc.diagnostic)
				require.Empty(t, api.take())
			} else {
				require.NoError(t, err)
				namespace := "default"
				if selected, ok := tc.metadata["namespace"].(string); ok {
					namespace = selected
				}
				require.Equal(t, []string{
					"GET /apis/apps/v1/namespaces/" + namespace + "/daemonsets/example",
				}, api.take())
			}
		})
	}
}

func TestDaemonSetLookupNativeIdentityDefaults(t *testing.T) {
	name, namespace := strings.Repeat("a", 249)+".app", strings.Repeat("n", 63)
	for _, metadata := range []map[string]any{
		{"name": "example"}, {"name": "example", "namespace": nil},
		{"name": name, "namespace": namespace},
	} {
		api := newDaemonSetLookupServer(t)
		e := daemonSetLookupExecutor(t, daemonSetLookupLibrary(t), t.TempDir(), "steady",
			map[string]any{"server": api.url, "metadata": metadata})
		_, err := e.Plan(t.Context())
		require.NoError(t, err)
		selected := "default"
		if value, ok := metadata["namespace"].(string); ok {
			selected = value
		}
		require.Equal(t, []string{
			"GET /apis/apps/v1/namespaces/" + selected + "/daemonsets/" + metadata["name"].(string),
		}, api.take())
	}
	for _, metadata := range []any{nil, map[string]any{}, map[string]any{
		"name": "example", "labels": map[string]any{},
	}} {
		api := newDaemonSetLookupServer(t)
		e := daemonSetLookupExecutor(t, daemonSetLookupLibrary(t), t.TempDir(), "steady",
			map[string]any{"server": api.url, "metadata": metadata})
		_, err := e.Plan(t.Context())
		require.ErrorContains(t, err, "data-source.lookup")
		require.Empty(t, api.take())
	}
}

func TestDaemonSetLookupHistoricalInvalidDestroy(t *testing.T) {
	api := newDaemonSetLookupServer(t)
	lib := daemonSetLookupLibrary(t)
	rules := lib.Constraints
	lib.Constraints = nil
	e := daemonSetLookupExecutor(t, lib, t.TempDir(), "steady", map[string]any{
		"server": api.url, "metadata": map[string]any{"name": "BAD"},
	})
	api.obj.Name = "BAD"
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	api.take()
	lib.Constraints = rules
	err = restartSavedApply(t, e, p)
	require.ErrorContains(t, err, "data-source.lookup: schema: constraints[0]")
	require.Empty(t, api.take())
	e.Destroy = true
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	require.Empty(t, api.take())
}

func TestDaemonSetLookupObservationConsistency(t *testing.T) {
	for _, field := range []string{
		"unchanged", "metadata", "version", "spec", "status", "uid", "appears", "disappears", "order",
	} {
		t.Run(field, func(t *testing.T) {
			api := newDaemonSetLookupServer(t)
			if field == "appears" {
				api.obj = nil
			}
			if field == "order" {
				api.obj.Spec.Selector = &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "rack", Operator: "In", Values: []string{"z", "a", "a"}},
					},
				}
			}
			e := daemonSetLookupExecutor(t, daemonSetLookupLibrary(t), t.TempDir(), "steady",
				map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}})
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			api.mu.Lock()
			switch field {
			case "metadata":
				api.obj.Annotations = map[string]string{"controller.example/revision": "changed"}
			case "version":
				api.obj.ResourceVersion = "changed"
			case "spec":
				api.obj.Spec.MinReadySeconds = 9
			case "status":
				api.obj.Status.NumberReady = 2
			case "order":
				api.obj.Spec.Selector.MatchExpressions[0].Values = []string{"a", "a", "z"}
			case "uid":
				api.obj.UID = "replacement"
			case "appears":
				api.obj = &appsv1.DaemonSet{}
			case "disappears":
				api.obj = nil
			}
			api.mu.Unlock()
			err = restartSavedApply(t, e, p)
			if field == "unchanged" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			get := "GET /apis/apps/v1/namespaces/default/daemonsets/example"
			require.Equal(t, []string{get, get}, api.take())
			fresh, err := e.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{get}, api.take())
			for _, step := range fresh.Steps {
				if step.Address != "data-source.lookup" {
					continue
				}
				if api.obj == nil {
					require.Nil(t, step.ObservedOutputs["spec"])
					require.Nil(t, step.ObservedOutputs["status"])
				} else {
					metadata := step.ObservedOutputs["metadata"].(map[string]any)
					require.Equal(t, string(api.obj.UID), metadata["uid"])
					require.Equal(t, api.obj.ResourceVersion, metadata["resource-version"])
				}
			}
		})
	}
}

func TestDaemonSetLookupNativeProjection(t *testing.T) {
	api := newDaemonSetLookupServer(t)
	api.obj.Spec.Selector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: "rack", Operator: "In", Values: []string{"z", "a", "a"},
		}},
	}
	api.obj.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
		SupplementalGroups: []int64{2, 1, 1}, RunAsNonRoot: new(false),
	}
	api.obj.Spec.RevisionHistoryLimit = new(int32(0))
	e := daemonSetLookupExecutor(t, daemonSetLookupLibrary(t), t.TempDir(), "steady",
		map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}})
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	var outputs map[string]any
	for _, step := range p.Steps {
		if step.Address == "data-source.lookup" {
			require.Equal(t, runtime.DecisionRead, step.Decision)
			outputs = step.ObservedOutputs
		}
	}
	require.NotNil(t, outputs)
	spec := outputs["spec"].(map[string]any)
	require.Equal(t, int64(0), spec["revision-history-limit"])
	require.Equal(t, int64(0), spec["min-ready-seconds"])
	selector := spec["selector"].(map[string]any)
	require.Equal(t, []any{map[string]any{"key": "rack", "operator": "In",
		"values": []any{"z", "a", "a"}}}, selector["match-expressions"])
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	security := pod["security-context"].(map[string]any)
	require.Equal(t, []any{int64(2), int64(1), int64(1)}, security["supplemental-groups"])
	require.Equal(t, false, security["run-as-non-root"])
	require.NoError(t, restartSavedApply(t, e, p))
}

func TestDaemonSetLookupRemoved(t *testing.T) {
	api := newDaemonSetLookupServer(t)
	lib, dir := daemonSetLookupLibrary(t), t.TempDir()
	inputs := map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}}
	e := daemonSetLookupExecutor(t, lib, dir, "steady", inputs)
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	api.take()
	e = daemonSetLookupExecutor(t, lib, dir, "removed", inputs)
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, restartSavedApply(t, e, p))
	require.Empty(t, api.take())
}

func TestDaemonSetLookupPendingConfiguration(t *testing.T) {
	for _, name := range []string{"example", "BAD"} {
		t.Run(name, func(t *testing.T) {
			api := newDaemonSetLookupServer(t)
			e := daemonSetLookupExecutor(t, daemonSetLookupLibrary(t), t.TempDir(), "deferred-config",
				map[string]any{"server": api.url, "metadata": map[string]any{"name": name}})
			p, err := e.Plan(t.Context())
			if name == "BAD" {
				require.ErrorContains(t, err, "data-source.lookup: schema: constraints[0]")
				require.Empty(t, api.take())
				return
			}
			require.NoError(t, err)
			require.Empty(t, api.take())
			err = restartSavedApply(t, e, p)
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /apis/apps/v1/namespaces/default/daemonsets/example",
			}, api.take())
		})
	}
}

func TestDaemonSetLookupChangingDependency(t *testing.T) {
	api := newDaemonSetLookupServer(t)
	e := daemonSetLookupExecutor(t, daemonSetLookupLibrary(t), t.TempDir(), "dependency",
		map[string]any{"server": api.url, "metadata": map[string]any{"name": "example"}, "epoch": "one"})
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, api.take())
	require.NoError(t, restartSavedApply(t, e, p))
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/daemonsets/example",
	}, api.take())
	e.Inputs["epoch"] = "two"
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, api.take())
	require.NoError(t, restartSavedApply(t, e, p))
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/daemonsets/example",
	}, api.take())
}
