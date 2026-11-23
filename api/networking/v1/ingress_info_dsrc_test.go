package v1

import (
	"context"
	"encoding/json"
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

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/networking/v1"
)

func ingressInfoLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin")
	sdk, err := cmd.Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(sdk)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.ingress-info": schema.DataSources["ingress-info"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"data-source.ingress-info": schema.DataSources["ingress-info"].Defaults,
	}
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[ingressInfoSeed, *ingressInfoSeedOutput, any](),
	}
	lib.Resources["fixed"] = runtime.MakeResource[
		ingressInfoFixed, *ingressInfoFixed, any,
	](runtime.ResourceDefinition[ingressInfoFixed, *ingressInfoFixed, any]{SchemaVersion: 1})
	return lib
}

func ingressInfoExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	file := filepath.Join("testdata/ub/ingress-info", fixture, "factory.ub")
	source, err := os.ReadFile(file)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(file, source)
	require.NoError(t, err)
	libraries := map[string]*runtime.Library{"networking": lib, "core": core.Library()}
	store, err := local.NewStore(t.TempDir(), "ingress-info", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libraries),
		SyntaxSource: &parsed.Factory.Body, Libraries: libraries, Store: store,
		Factory: state.FactoryInfo{Name: "ingress-info", Version: "v0", ContentRevision: "c0"},
	}
}

func ingressInfoPlanFile(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

type ingressInfoHTTP struct {
	sync.Mutex

	object    *networkingv1.Ingress
	configMap *corev1.ConfigMap
	requests  []string
	server    string
}

func newIngressInfoHTTP(t *testing.T, obj *networkingv1.Ingress) *ingressInfoHTTP {
	t.Helper()
	s := &ingressInfoHTTP{object: obj}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Lock()
		defer s.Unlock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/namespaces/default/configmaps" ||
			r.URL.Path == "/api/v1/namespaces/default/configmaps/seed" {
			assert.NoError(t, json.NewEncoder(w).Encode(s.configMap))
			return
		}
		if s.object == nil {
			w.WriteHeader(http.StatusNotFound)
			status := apierrors.NewNotFound(networkingv1.Resource("ingresses"), "route").ErrStatus
			assert.NoError(t, json.NewEncoder(w).Encode(status))
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(s.object))
	}))
	s.server = server.URL
	t.Cleanup(server.Close)
	return s
}

func (s *ingressInfoHTTP) calls() []string {
	s.Lock()
	defer s.Unlock()
	result := slices.Clone(s.requests)
	s.requests = nil
	return result
}

func (s *ingressInfoHTTP) put(obj *networkingv1.Ingress) {
	s.Lock()
	defer s.Unlock()
	s.object = obj
}

type ingressInfoSeed struct{ Metadata svc.IngressLookupMetadata }

type ingressInfoSeedOutput struct{ Result svc.IngressLookupMetadata }

func (s *ingressInfoSeed) Run(context.Context, any) (*ingressInfoSeedOutput, error) {
	return &ingressInfoSeedOutput{Result: s.Metadata}, nil
}

type ingressInfoFixed struct{ Value string }

func (r *ingressInfoFixed) Create(context.Context, any) (*ingressInfoFixed, error) {
	return r, nil
}

func (r *ingressInfoFixed) Read(
	_ context.Context, _ any, prior runtime.Prior[ingressInfoFixed, *ingressInfoFixed, any],
) (*ingressInfoFixed, error) {
	return prior.Outputs, nil
}

func (r *ingressInfoFixed) Update(
	context.Context, any, runtime.Prior[ingressInfoFixed, *ingressInfoFixed, any],
) (*ingressInfoFixed, error) {
	panic("unexpected update")
}

func (r *ingressInfoFixed) Delete(
	context.Context, any, runtime.Prior[ingressInfoFixed, *ingressInfoFixed, any],
) error {
	return nil
}

const ingressInfoRequest = "GET /apis/networking.k8s.io/v1/namespaces/default/ingresses/route"

func TestIngressInfoEvaluatedInputs(t *testing.T) {
	lib := ingressInfoLibrary(t)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		path     string
		rule     string
	}{
		{"valid", map[string]any{"name": "route", "labels": map[string]any{
			"example.com/Key": "label", "bad key": nil,
		}, "annotations": map[string]any{"İK.Example/Key": "arbitrary value"}}, ingressInfoRequest, ""},
		{"null namespace", map[string]any{"name": "route", "namespace": nil}, ingressInfoRequest, ""},
		{"empty namespace", map[string]any{"name": "route", "namespace": ""},
			"GET /apis/networking.k8s.io/v1/ingresses/route", ""},
		{"empty name", map[string]any{"name": ""}, "", "constraints[0]"},
		{"invalid name", map[string]any{"name": "Bad_Name"}, "", "constraints[0]"},
		{"label key", map[string]any{"name": "route", "labels": map[string]any{"bad key": ""}},
			"", "constraints[1]"},
		{"label value", map[string]any{"name": "route", "labels": map[string]any{"key": "bad value"}},
			"", "constraints[2]"},
		{"annotation", map[string]any{"name": "route", "annotations": map[string]any{"bad key": ""}},
			"", "constraints[3]"},
		{"omitted name", map[string]any{}, "", ""},
		{"null name", map[string]any{"name": nil}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			http := newIngressInfoHTTP(t, &networkingv1.Ingress{})
			executor := ingressInfoExecutor(t, lib, "settled")
			executor.Inputs = map[string]any{"server": http.server, "metadata": tc.metadata}
			_, err := executor.Plan(t.Context())
			switch {
			case tc.rule != "":
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				require.Empty(t, http.calls())
			case tc.path == "":
				require.ErrorContains(t, err, "resource name may not be empty")
				require.Empty(t, http.calls())
			default:
				require.NoError(t, err)
				require.Equal(t, []string{tc.path}, http.calls())
			}
		})
	}
}

func TestIngressInfoDeferredInputs(t *testing.T) {
	lib := ingressInfoLibrary(t)
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		rule     string
	}{
		{"valid", map[string]any{"name": "route"}, ""},
		{"name", map[string]any{"name": "Bad_Name"}, "constraints[0]"},
		{"label key", map[string]any{"name": "route", "labels": map[string]any{"bad key": ""}},
			"constraints[1]"},
		{"label value", map[string]any{"name": "route", "labels": map[string]any{"key": "bad value"}},
			"constraints[2]"},
		{"annotation", map[string]any{"name": "route", "annotations": map[string]any{"bad key": ""}},
			"constraints[3]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			http := newIngressInfoHTTP(t, &networkingv1.Ingress{})
			executor := ingressInfoExecutor(t, lib, "deferred")
			executor.Inputs = map[string]any{"server": http.server, "metadata": tc.metadata}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, http.calls())
			_, err = executor.ApplyPlan(t.Context(), ingressInfoPlanFile(t, plan))
			if tc.rule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.rule)
				require.Empty(t, http.calls())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{ingressInfoRequest}, http.calls())
		})
	}
}

func TestIngressInfoPartiallyPendingMap(t *testing.T) {
	lib := ingressInfoLibrary(t)
	for _, tc := range []struct {
		name     string
		field    string
		value    string
		planRule string
	}{
		{name: "known name", field: "name", value: "Bad_Name", planRule: "constraints[0]"},
		{name: "known label key", field: "known-key", value: "", planRule: "constraints[1]"},
		{name: "known label value", field: "known-value", value: "bad!",
			planRule: "constraints[2]"},
		{name: "known annotation", field: "known-annotation", value: "",
			planRule: "constraints[3]"},
		{name: "invalid key resolves to nonnull", field: "pending", value: ""},
		{name: "invalid key resolves to null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			http := newIngressInfoHTTP(t, nil)
			executor := ingressInfoExecutor(t, lib, "partial")
			labels := map[string]any{"pending": nil, "kept": ""}
			executor.Inputs = map[string]any{
				"server": http.server, "name": "route", "known-key": nil,
				"known-value": nil, "known-annotation": nil,
				"seed": map[string]any{"namespace": "resolved", "labels": labels},
			}
			switch tc.field {
			case "":
			case "pending":
				labels[tc.field] = tc.value
			default:
				executor.Inputs[tc.field] = tc.value
			}
			plan, err := executor.Plan(t.Context())
			require.Empty(t, http.calls())
			if tc.planRule != "" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: "+tc.planRule)
				if tc.field == "known-key" {
					require.ErrorContains(t, err, "metadata.labels['bad known']")
				}
				return
			}
			require.NoError(t, err)
			result, err := executor.ApplyPlan(t.Context(), ingressInfoPlanFile(t, plan))
			if tc.field == "pending" {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[1]")
				require.Empty(t, http.calls())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /apis/networking.k8s.io/v1/namespaces/resolved/ingresses/route",
			}, http.calls())
			require.Equal(t, map[string]any{"metadata": map[string]any{
				"name": "route", "namespace": "resolved", "labels": map[string]any{"kept": ""},
				"annotations": map[string]any{}, "uid": "", "resource-version": "",
				"generation": int64(0),
			}}, result.Outputs)
		})
	}
}

func TestIngressInfoPartiallyPendingMapComprehension(t *testing.T) {
	lib := ingressInfoLibrary(t)
	for _, tc := range []struct {
		name          string
		labelKey      string
		pendingKey    bool
		invalidAtPlan bool
	}{
		{name: "invalid known key", labelKey: "bad known", invalidAtPlan: true},
		{name: "valid known key", labelKey: "known"},
		{name: "invalid pending key", labelKey: "known", pendingKey: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			http := newIngressInfoHTTP(t, nil)
			http.Lock()
			http.configMap = &corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
				ObjectMeta: metav1.ObjectMeta{
					Name: "seed", Namespace: "default", UID: "config-map-uid",
					Annotations: map[string]string{"label-key": "bad pending"},
				},
			}
			http.Unlock()
			executor := ingressInfoExecutor(t, lib, "comprehension")
			executor.Inputs = map[string]any{
				"server": http.server, "label-key": tc.labelKey, "pending-key": tc.pendingKey,
			}
			plan, err := executor.Plan(t.Context())
			require.Empty(t, http.calls())
			if tc.invalidAtPlan {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[1]")
				require.ErrorContains(t, err, "metadata.labels['bad known']")
				return
			}
			require.NoError(t, err)
			saved := ingressInfoPlanFile(t, plan)
			endpoint := slices.IndexFunc(saved.Steps, func(step runtime.PlanStep) bool {
				return step.Address == "data-source.endpoint"
			})
			require.NotEqual(t, -1, endpoint)
			require.Equal(t, runtime.DecisionRead, saved.Steps[endpoint].Decision)
			require.Nil(t, saved.Steps[endpoint].ObservedOutputs)
			result, err := executor.ApplyPlan(t.Context(), saved)
			requests := []string{
				"POST /api/v1/namespaces/default/configmaps",
				"GET /api/v1/namespaces/default/configmaps/seed",
			}
			if tc.pendingKey {
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[1]")
				require.ErrorContains(t, err, "metadata.labels['bad pending']")
				require.Equal(t, requests, http.calls())
				return
			}
			require.NoError(t, err)
			require.Equal(t, append(requests, ingressInfoRequest), http.calls())
			require.Equal(t, map[string]any{
				"labels": map[string]any{"known": "", "upstream": "config-map-uid"},
			}, result.Outputs)
		})
	}
}

func TestIngressInfoSteadySavedRead(t *testing.T) {
	lib := ingressInfoLibrary(t)
	for _, outcome := range []string{"equal", "changed", "missing", "invalid-input"} {
		t.Run(outcome, func(t *testing.T) {
			obj := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
				Name: "live-route", Namespace: "live-namespace", Generation: 1,
				UID: "uid", ResourceVersion: "2",
				Labels: map[string]string{"app": "live"}, Annotations: map[string]string{},
			}}
			http := newIngressInfoHTTP(t, obj)
			executor := ingressInfoExecutor(t, lib, "settled")
			executor.Inputs = map[string]any{"server": http.server,
				"metadata": map[string]any{"name": "route", "labels": map[string]any{"app": "input"}},
			}
			first, err := executor.Plan(t.Context())
			require.NoError(t, err)
			result, err := executor.ApplyPlan(t.Context(), ingressInfoPlanFile(t, first))
			require.NoError(t, err)
			require.Equal(t, map[string]any{
				"metadata": map[string]any{
					"name": "live-route", "namespace": "live-namespace", "uid": "uid",
					"resource-version": "2", "generation": int64(1),
					"annotations": map[string]any{}, "labels": map[string]any{"app": "live"},
				},
				"spec": map[string]any{
					"ingress-class-name": nil, "default-backend": nil,
					"rule": []any{}, "tls": []any{},
				},
				"status": map[string]any{"load-balancer": map[string]any{"ingress": []any{}}},
			}, result.Outputs)
			require.Equal(t, []string{ingressInfoRequest, ingressInfoRequest}, http.calls())

			steady, err := executor.Plan(t.Context())
			require.NoError(t, err)
			decisions := map[string]runtime.Decision{}
			for _, step := range steady.Steps {
				if step.Address == "resource.fixed" || step.Address == "data-source.endpoint" {
					decisions[step.Address] = step.Decision
				}
			}
			require.Equal(t, map[string]runtime.Decision{
				"resource.fixed": runtime.DecisionNoOp, "data-source.endpoint": runtime.DecisionRead,
			}, decisions)
			require.Equal(t, []string{ingressInfoRequest}, http.calls())
			switch outcome {
			case "changed":
				changed := obj.DeepCopy()
				changed.Spec.IngressClassName = new("changed")
				http.put(changed)
			case "missing":
				http.put(nil)
			case "invalid-input":
				invalidInputs := maps.Clone(executor.Inputs)
				metadata := maps.Clone(invalidInputs["metadata"].(map[string]any))
				metadata["name"] = "Bad_Name"
				invalidInputs["metadata"] = metadata
				executor.Inputs = invalidInputs
				_, err := executor.Plan(t.Context())
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[0]")
				require.Empty(t, http.calls())
				executor.Inputs = nil
				steady.Inputs = invalidInputs
			}
			result, err = executor.ApplyPlan(t.Context(), ingressInfoPlanFile(t, steady))
			switch outcome {
			case "invalid-input":
				require.ErrorContains(t, err, "data-source.endpoint: schema: constraints[0]")
				require.Empty(t, http.calls())
			case "equal":
				require.NoError(t, err)
				require.Equal(t, []string{ingressInfoRequest}, http.calls())
				require.Equal(t, "live-route", result.Outputs["metadata"].(map[string]any)["name"])
			default:
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
				require.Equal(t, []string{ingressInfoRequest}, http.calls())
			}
		})
	}
}

func TestIngressInfoRemovalAndDestroy(t *testing.T) {
	lib := ingressInfoLibrary(t)
	for _, destroy := range []bool{false, true} {
		http := newIngressInfoHTTP(t, &networkingv1.Ingress{})
		executor := ingressInfoExecutor(t, lib, "settled")
		executor.Inputs = map[string]any{"server": http.server,
			"metadata": map[string]any{"name": "route"},
		}
		plan, err := executor.Plan(t.Context())
		require.NoError(t, err)
		_, err = executor.ApplyPlan(t.Context(), ingressInfoPlanFile(t, plan))
		require.NoError(t, err)
		require.Equal(t, []string{ingressInfoRequest, ingressInfoRequest}, http.calls())

		if destroy {
			executor.Destroy = true
		} else {
			removed := ingressInfoExecutor(t, lib, "removed")
			removed.Store = executor.Store
			removed.Inputs = executor.Inputs
			executor = removed
		}
		plan, err = executor.Plan(t.Context())
		require.NoError(t, err)
		result, err := executor.ApplyPlan(t.Context(), ingressInfoPlanFile(t, plan))
		require.NoError(t, err)
		require.Empty(t, result.Data)
		require.Empty(t, http.calls())
	}
}
