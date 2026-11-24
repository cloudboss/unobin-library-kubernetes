package v1

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	api "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const (
	serviceExport  = "api-service"
	serviceRuleKey = "resource." + serviceExport
	resourceAddr   = "resource.example"
)

var readServiceSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/apiregistration/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("APIService schema extraction: %v", warnings)
	}
	return schema, nil
})

type serviceSeed struct {
	EchoServer        string
	EchoGroup         string
	EchoService       any
	EchoCABundle      any
	EchoSkip          any
	EchoGroupPriority int64
}

func (s *serviceSeed) Run(context.Context, *config.Configuration) (any, error) {
	return map[string]any{
		"server": s.EchoServer, "group": s.EchoGroup,
		"service": s.EchoService, "ca-bundle": s.EchoCABundle,
		"skip": s.EchoSkip, "group-priority": s.EchoGroupPriority,
	}, nil
}

type servicePlanner struct {
	t      *testing.T
	libs   map[string]*runtime.Library
	store  *local.Store
	inputs map[string]any
}

func newServicePlanner(t *testing.T, cfg *config.Configuration) *servicePlanner {
	t.Helper()
	schema, err := readServiceSchema()
	require.NoError(t, err)
	model := *schema.Resources[serviceExport]
	model.EvaluatedInputs, model.Inputs = model.Inputs, nil
	base := runtime.MakeResource[
		APIServiceResource, *APIServiceResourceOutput, *config.Configuration,
	]((&APIServiceResource{}).ResourceDefinition())
	lib := &runtime.Library{
		Configuration: config.LibraryConfiguration(),
		Schema: &runtime.LibrarySchema{
			Resources: map[string]*runtime.TypeSchema{serviceExport: &model},
		},
		Constraints: map[string][]lang.ConstraintSpec{serviceRuleKey: model.Constraints},
		Resources: map[string]runtime.ResourceRegistration{
			serviceExport: APIServiceRegistration[APIServiceResource, *APIServiceResourceOutput](
				base, base.Read, base.KnownReplacementReasons,
			),
		},
		Actions: map[string]runtime.ActionRegistration{
			"seed": runtime.MakeAction[serviceSeed, any, *config.Configuration](),
		},
	}
	store, err := local.NewStore(t.TempDir(), "api-service", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &servicePlanner{
		t: t, libs: map[string]*runtime.Library{"registration": lib, "seed": lib}, store: store,
		inputs: map[string]any{
			"server":             *cfg.Server,
			"metadata":           map[string]any{"name": testName},
			"ignore-annotations": nil,
			"ignore-labels":      nil,
			"spec": map[string]any{
				"group": "example.test", "version": "v1",
				"group-priority-minimum": int64(20), "version-priority": int64(10),
			},
			"group":            "example.test",
			"service":          nil,
			"ca-bundle":        nil,
			"skip":             nil,
			"group-priority":   int64(20),
			"version-priority": int64(10),
		},
	}
}

func (p *servicePlanner) executor(fixture string) *runtime.Executor {
	p.t.Helper()
	body, err := os.ReadFile("testdata/ub/" + fixture + "/factory.ub")
	require.NoError(p.t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(p.t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, p.libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: p.libs, Inputs: p.inputs, Store: p.store,
		Factory: state.FactoryInfo{Name: "api-service", Version: "v0", ContentRevision: "c0"},
	}
}

func (p *servicePlanner) plan(fixture string, want runtime.Decision) (
	*runtime.Executor, *runtime.PlanFile, *runtime.PlanStep,
) {
	p.t.Helper()
	exec := p.executor(fixture)
	exec.Destroy = want == runtime.DecisionDestroy
	plan, err := exec.Plan(p.t.Context())
	require.NoError(p.t, err)
	encoded, err := runtime.EncodePlan(plan)
	require.NoError(p.t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(p.t, err)
	for i := range file.Steps {
		step := &file.Steps[i]
		if step.Address == resourceAddr {
			require.Equal(p.t, want, step.Decision)
			return exec, file, step
		}
	}
	p.t.Fatalf("%s absent from plan", resourceAddr)
	return nil, nil, nil
}

func (p *servicePlanner) apply(fixture string, want runtime.Decision) *runtime.ExecResult {
	p.t.Helper()
	exec, file, _ := p.plan(fixture, want)
	result, err := exec.ApplyPlan(p.t.Context(), file)
	require.NoError(p.t, err)
	return result
}

func (p *servicePlanner) metadata() map[string]any {
	return p.inputs["metadata"].(map[string]any)
}

func (p *servicePlanner) spec() map[string]any {
	return p.inputs["spec"].(map[string]any)
}

func serviceBackend() map[string]any {
	return map[string]any{"name": "backend", "namespace": "remote"}
}

func requireNativeError(t *testing.T, err error, message string) {
	t.Helper()
	require.ErrorContains(t, err, resourceAddr+": schema: constraints")
	require.ErrorContains(t, err, message)
}

func TestAPIServiceSavedIdentityReplacement(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	result := p.apply("settled", runtime.DecisionCreate)
	require.Equal(t, "uid-1", result.Outputs["uid"])
	requireTrace(t, s.takeRequests(), "POST "+collection, "GET "+testItem, "GET "+testItem)

	const newName = "v2.renamed.test"
	p.metadata()["name"] = newName
	p.spec()["group"], p.spec()["version"] = "renamed.test", "v2"
	exec, saved, step := p.plan("settled", runtime.DecisionReplace)
	require.Equal(t, []string{"metadata.name"}, step.ReplacementReasons)
	require.Equal(t, new(*cfg.Server+"/uid-1"), step.ExpectedStableID)
	requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem)
	result, err := exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, "uid-2", result.Outputs["uid"])
	target := collection + "/" + newName
	requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem, "DELETE "+testItem,
		"POST "+collection, "GET "+target, "GET "+target)
	require.Equal(t, newName, s.snapshot().Name)
}

func TestAPIServiceSavedBackendPresenceAndDrift(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	s.takeRequests()

	for _, tc := range []struct {
		name    string
		desired bool
		drift   bool
		want    runtime.Decision
		uid     string
	}{
		{"add", true, false, runtime.DecisionReplace, "uid-2"},
		{"remove", false, false, runtime.DecisionReplace, "uid-3"},
		{"correct drift", false, true, runtime.DecisionReplace, "uid-4"},
		{"match live drift", true, true, runtime.DecisionUpdate, "uid-4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delete(p.spec(), "service")
			if tc.desired {
				p.spec()["service"] = serviceBackend()
			}
			if tc.drift {
				s.change(func(obj *api.APIService) {
					obj.Spec.Service = &api.ServiceReference{
						Name: "backend", Namespace: "remote", Port: new(int32(defaultPort)),
					}
				})
			}
			exec, saved, step := p.plan("settled", tc.want)
			requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem)
			if tc.want == runtime.DecisionReplace {
				require.Equal(t, []string{"spec"}, step.ReplacementReasons)
			} else {
				require.Empty(t, step.ReplacementReasons)
			}
			result, err := exec.ApplyPlan(t.Context(), saved)
			require.NoError(t, err)
			require.Equal(t, tc.uid, result.Outputs["uid"])
			requests := s.takeRequests()
			if tc.want == runtime.DecisionReplace {
				requireTrace(t, requests, "GET "+testItem, "GET "+testItem, "DELETE "+testItem,
					"POST "+collection, "GET "+testItem, "GET "+testItem)
			} else {
				requireTrace(t, requests, "PATCH "+testItem, "GET "+testItem, "GET "+testItem)
				require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-4"}]`,
					requests[0].body)
			}
			if tc.desired {
				require.Equal(t, &api.ServiceReference{
					Name: "backend", Namespace: "remote", Port: new(int32(defaultPort)),
				}, s.snapshot().Spec.Service)
			} else {
				require.Nil(t, s.snapshot().Spec.Service)
			}
		})
	}
}

func TestAPIServiceSavedBackendEditsAndNoOp(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	backend := serviceBackend()
	p.spec()["service"] = backend
	p.apply("settled", runtime.DecisionCreate)
	s.takeRequests()
	p.spec()["ca-bundle"], p.spec()["insecure-skip-tls-verify"] = "", false
	p.metadata()["annotations"], p.metadata()["labels"] = map[string]any{}, map[string]any{}
	backend["port"] = int64(defaultPort)
	s.change(func(obj *api.APIService) {
		obj.Generation, obj.ResourceVersion = 12, "new"
		obj.Status.Conditions[0].Status = api.ConditionTrue
	})
	p.apply("settled", runtime.DecisionNoOp)
	requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem)
	for _, tc := range []struct {
		field  string
		input  any
		expect string
	}{
		{"name", "other", `{"name":"other","namespace":"remote","port":443}`},
		{"namespace", "different", `{"name":"other","namespace":"different","port":443}`},
		{"port", int64(6443), `{"name":"other","namespace":"different","port":6443}`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			backend[tc.field] = tc.input
			result := p.apply("settled", runtime.DecisionUpdate)
			require.Equal(t, "uid-1", result.Outputs["uid"])
			requests := s.takeRequests()
			requireTrace(t, requests,
				"GET "+testItem, "GET "+testItem, "PATCH "+testItem, "GET "+testItem, "GET "+testItem)
			require.JSONEq(t, `[
				{"op":"test","path":"/metadata/uid","value":"uid-1"},
				{"op":"replace","path":"/spec","value":{
					"group":"example.test","version":"v1","groupPriorityMinimum":20,"versionPriority":10,
					"service":`+tc.expect+`
				}}
			]`, requests[2].body)
		})
	}
}

func TestAPIServiceDeferredConfigurationObservesBackendDrift(t *testing.T) {
	for _, tc := range []struct {
		name          string
		savedRemote   bool
		desiredRemote bool
		newName       string
		want          runtime.Decision
	}{
		{name: "restore local", want: runtime.DecisionReplace},
		{name: "restore remote", savedRemote: true, desiredRemote: true,
			want: runtime.DecisionReplace},
		{name: "accept live remote", desiredRemote: true, want: runtime.DecisionUpdate},
		{name: "accept live local", savedRemote: true, want: runtime.DecisionUpdate},
		{name: "known name change", desiredRemote: true, newName: "v1.renamed.test",
			want: runtime.DecisionReplace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newAPIServer(t)
			p := newServicePlanner(t, cfg)
			if tc.savedRemote {
				p.spec()["service"] = serviceBackend()
			}
			initial := p.apply("settled", runtime.DecisionCreate)
			require.Equal(t, "uid-1", initial.Outputs["uid"])
			requireTrace(t, s.takeRequests(), "POST "+collection, "GET "+testItem, "GET "+testItem)

			backend := &api.ServiceReference{
				Name: "backend", Namespace: "remote", Port: new(int32(defaultPort)),
			}
			s.change(func(obj *api.APIService) {
				obj.Spec.Service = backend
				if tc.savedRemote {
					obj.Spec.Service = nil
				}
			})
			var desired *api.ServiceReference
			delete(p.spec(), "service")
			if tc.desiredRemote {
				p.spec()["service"] = serviceBackend()
				desired = backend
			}
			target := testItem
			if tc.newName != "" {
				p.metadata()["name"], p.spec()["group"] = tc.newName, "renamed.test"
				target = collection + "/" + tc.newName
			}
			exec, saved, step := p.plan("deferred-config", runtime.DecisionReplace)
			require.Empty(t, s.takeRequests())
			require.Equal(t, "library-config.registration", step.DeferredConfig)
			require.Empty(t, step.UnresolvedInputs)
			require.Nil(t, step.ObservedOutputs)

			result, err := exec.ApplyPlan(t.Context(), saved)
			require.NoError(t, err)
			require.Equal(t, []string{"current-context", "kubeconfig", "server", "spec"},
				step.PendingReplacementReasons)
			switch {
			case tc.newName != "":
				require.Equal(t, []string{"metadata.name"}, step.ReplacementReasons)
				require.Equal(t, []runtime.Decision{runtime.DecisionCreate, runtime.DecisionReplace},
					step.AllowedDecisions)
			case tc.want == runtime.DecisionReplace:
				require.Empty(t, step.ReplacementReasons)
				require.Equal(t, []runtime.Decision{runtime.DecisionCreate, runtime.DecisionNoOp,
					runtime.DecisionUpdate, runtime.DecisionReplace}, step.AllowedDecisions)
			default:
				require.Empty(t, step.ReplacementReasons)
				require.Equal(t, []runtime.Decision{runtime.DecisionCreate,
					runtime.DecisionUpdate, runtime.DecisionReplace}, step.AllowedDecisions)
			}
			requests := s.takeRequests()
			if tc.want == runtime.DecisionReplace {
				require.Equal(t, "uid-2", result.Outputs["uid"])
				requireTrace(t, requests, "GET "+testItem, "GET "+testItem, "GET "+testItem,
					"GET "+testItem, "DELETE "+testItem, "POST "+collection,
					"GET "+target, "GET "+target)
				require.JSONEq(t, `{"kind":"DeleteOptions","apiVersion":"apiregistration.k8s.io/v1",
					"preconditions":{"uid":"uid-1"}}`, requests[4].body)
			} else {
				require.Equal(t, "uid-1", result.Outputs["uid"])
				requireTrace(t, requests, "GET "+testItem, "GET "+testItem, "PATCH "+testItem,
					"GET "+testItem, "GET "+testItem)
				require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid-1"}]`,
					requests[2].body)
			}
			require.Equal(t, desired, s.snapshot().Spec.Service)
		})
	}
}

func TestAPIServiceMissingRecreates(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	s.takeRequests()
	s.change(func(*api.APIService) { s.live = nil })
	result := p.apply("settled", runtime.DecisionCreate)
	require.Equal(t, "uid-2", result.Outputs["uid"])
	requireTrace(t, s.takeRequests(),
		"GET "+testItem, "POST "+collection, "GET "+testItem, "GET "+testItem)
}

func TestAPIServiceSavedPlanRejectsNewUID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*servicePlanner)
		want   runtime.Decision
		trace  []string
	}{
		{"patch", func(p *servicePlanner) { p.spec()["version-priority"] = int64(12) },
			runtime.DecisionUpdate, []string{"PATCH " + testItem}},
		{"replacement", func(p *servicePlanner) { p.spec()["service"] = serviceBackend() },
			runtime.DecisionReplace, []string{"GET " + testItem}},
		{"filters", func(p *servicePlanner) { p.inputs["ignore-annotations"] = []any{"^hidden/"} },
			runtime.DecisionUpdate, []string{"GET " + testItem}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newAPIServer(t)
			p := newServicePlanner(t, cfg)
			p.apply("settled", runtime.DecisionCreate)
			s.takeRequests()
			tc.change(p)
			exec, saved, _ := p.plan("settled", tc.want)
			s.takeRequests()
			s.change(func(obj *api.APIService) { obj.UID = "new-object" })
			before := s.snapshot()
			_, err := exec.ApplyPlan(t.Context(), saved)
			if tc.name == "patch" {
				require.True(t, apierrors.IsConflict(err), "%v", err)
			} else {
				require.ErrorContains(t, err, "UID changed from uid-1 to new-object")
			}
			require.Equal(t, before, s.snapshot())
			requireTrace(t, s.takeRequests(), tc.trace...)
		})
	}
}

func TestAPIServiceNativeEvaluatedInputs(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		edit          func(*servicePlanner)
	}{
		{"identity", "metadata.name must equal spec.version", func(p *servicePlanner) {
			p.spec()["version"] = "v2"
		}},
		{"group", "invalid spec.group", func(p *servicePlanner) {
			p.spec()["group"], p.metadata()["name"] = "Bad", "v1.Bad"
		}},
		{"port lower", "spec.service.port must be between 1 and 65535", func(p *servicePlanner) {
			backend := serviceBackend()
			backend["port"] = int64(0)
			p.spec()["service"] = backend
		}},
		{"port upper", "spec.service.port must be between 1 and 65535", func(p *servicePlanner) {
			backend := serviceBackend()
			backend["port"] = int64(65536)
			p.spec()["service"] = backend
		}},
		{"empty name", "spec.service.name must not be empty", func(p *servicePlanner) {
			p.spec()["service"] = map[string]any{"name": "", "namespace": "remote"}
		}},
		{"empty namespace", "spec.service.namespace must not be empty", func(p *servicePlanner) {
			p.spec()["service"] = map[string]any{"name": "proxy", "namespace": ""}
		}},
		{"CA", "nonempty spec.ca-bundle requires a service", func(p *servicePlanner) {
			p.spec()["ca-bundle"] = "outside-service"
		}},
		{"skip", "insecure-skip-tls-verify requires a service", func(p *servicePlanner) {
			p.spec()["insecure-skip-tls-verify"] = true
		}},
		{"label", "invalid metadata.labels value", func(p *servicePlanner) {
			p.metadata()["labels"] = map[string]any{"name": "bad value"}
		}},
		{"annotation", "invalid metadata.annotations key", func(p *servicePlanner) {
			p.metadata()["annotations"] = map[string]any{"/wrong": "value"}
		}},
		{"pattern", "invalid ignore-annotations expression", func(p *servicePlanner) {
			p.inputs["ignore-annotations"] = []any{"["}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newAPIServer(t)
			p := newServicePlanner(t, cfg)
			tc.edit(p)
			_, err := p.executor("settled").Plan(t.Context())
			requireNativeError(t, err, tc.message)
			require.Empty(t, s.takeRequests())
		})
	}
}

func TestAPIServiceNativePartialBackendInputs(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		wholeService  bool
		ca, namespace string
		skip          bool
		unresolved    string
	}{
		{
			name: "known TLS conflict", wholeService: true, ca: "raw text", skip: true,
			message: "nonempty spec.ca-bundle requires TLS verification",
		},
		{
			name: "known empty namespace", message: "spec.service.namespace must not be empty",
		},
		{
			name: "valid deferred service", wholeService: true, ca: "raw text",
			unresolved: "action.seed.service",
		},
		{
			name: "valid deferred name", namespace: "remote", unresolved: "action.seed.group",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newAPIServer(t)
			p := newServicePlanner(t, cfg)
			p.inputs["defer-service"], p.inputs["service"] = tc.wholeService, serviceBackend()
			p.inputs["group"], p.inputs["namespace"] = "backend", tc.namespace
			p.inputs["ca-bundle"], p.inputs["skip"] = tc.ca, tc.skip
			const fixture = "partial-backend"
			if tc.message != "" {
				_, err := p.executor(fixture).Plan(t.Context())
				requireNativeError(t, err, tc.message)
				require.Empty(t, s.takeRequests())
				return
			}
			exec, saved, step := p.plan(fixture, runtime.DecisionCreate)
			require.Equal(t, map[string][]string{"spec": {tc.unresolved}}, step.UnresolvedInputs)
			require.Empty(t, s.takeRequests())
			result, err := exec.ApplyPlan(t.Context(), saved)
			require.NoError(t, err)
			require.Equal(t, "uid-1", result.Outputs["uid"])
			requireTrace(t, s.takeRequests(), "POST "+collection, "GET "+testItem, "GET "+testItem)
			require.Equal(t, &api.ServiceReference{
				Name: "backend", Namespace: "remote", Port: new(int32(defaultPort)),
			}, s.snapshot().Spec.Service)
		})
	}
}

func TestAPIServiceDeferredSavedReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		edit          func(*servicePlanner)
	}{
		{"valid", "", func(p *servicePlanner) { p.inputs["service"] = serviceBackend() }},
		{"name relation", "metadata.name must equal spec.version", func(p *servicePlanner) {
			p.inputs["group"] = "mismatch.test"
		}},
		{"local CA", "nonempty spec.ca-bundle requires a service", func(p *servicePlanner) {
			p.inputs["ca-bundle"] = "raw text"
		}},
		{"local skip", "insecure-skip-tls-verify requires a service", func(p *servicePlanner) {
			p.inputs["skip"] = true
		}},
		{"remote TLS conflict", "nonempty spec.ca-bundle requires TLS verification",
			func(p *servicePlanner) {
				p.inputs["service"] = serviceBackend()
				p.inputs["ca-bundle"], p.inputs["skip"] = "raw text", true
			},
		},
		{"empty namespace", "spec.service.namespace must not be empty", func(p *servicePlanner) {
			p.inputs["service"] = map[string]any{"name": "backend", "namespace": ""}
		}},
		{"priority lower", "spec.group-priority-minimum must be between 1 and 20000",
			func(p *servicePlanner) { p.inputs["group-priority"] = int64(0) }},
		{"priority upper", "spec.group-priority-minimum must be between 1 and 20000",
			func(p *servicePlanner) { p.inputs["group-priority"] = int64(20001) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := newAPIServer(t)
			p := newServicePlanner(t, cfg)
			p.apply("settled", runtime.DecisionCreate)
			s.takeRequests()
			p.metadata()["name"] = "v1.changed.test"
			p.inputs["group"] = "changed.test"
			tc.edit(p)
			for _, invalid := range []int64{0, 1001} {
				p.inputs["version-priority"] = invalid
				_, err := p.executor("partial").Plan(t.Context())
				requireNativeError(t, err, "spec.version-priority must be between 1 and 1000")
				requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem)
			}
			p.inputs["version-priority"] = int64(1000)
			exec, saved, step := p.plan("partial", runtime.DecisionReplace)
			require.Equal(t, map[string][]string{"spec": {
				"action.seed.group", "action.seed.group-priority", "action.seed.ca-bundle",
				"action.seed.skip", "action.seed.service",
			}}, step.UnresolvedInputs)
			s.takeRequests()
			before := s.snapshot()
			result, err := exec.ApplyPlan(t.Context(), saved)
			if tc.message != "" {
				requireNativeError(t, err, tc.message)
				require.Empty(t, s.takeRequests())
				require.Equal(t, before, s.snapshot())
				return
			}
			require.NoError(t, err)
			require.Equal(t, "uid-2", result.Outputs["uid"])
			target := collection + "/v1.changed.test"
			requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem, "DELETE "+testItem,
				"POST "+collection, "GET "+target, "GET "+target)
			require.Equal(t, &api.ServiceReference{
				Name: "backend", Namespace: "remote", Port: new(int32(443)),
			}, s.snapshot().Spec.Service)
		})
	}
}

func TestAPIServiceDeferredBackendCanResolveToNoOpOrReplace(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	s.takeRequests()
	exec, saved, step := p.plan("partial", runtime.DecisionReplace)
	require.Equal(t, []string{"spec"}, step.PendingReplacementReasons)
	require.Equal(t, []runtime.Decision{runtime.DecisionNoOp, runtime.DecisionUpdate,
		runtime.DecisionReplace}, step.AllowedDecisions)
	requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem)
	result, err := exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, "uid-1", result.Outputs["uid"])
	require.Empty(t, s.takeRequests())

	p.inputs["service"] = serviceBackend()
	exec, saved, step = p.plan("partial", runtime.DecisionReplace)
	require.Equal(t, []string{"spec"}, step.PendingReplacementReasons)
	s.takeRequests()
	result, err = exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	require.Equal(t, "uid-2", result.Outputs["uid"])
	requireTrace(t, s.takeRequests(), "GET "+testItem, "GET "+testItem, "DELETE "+testItem,
		"POST "+collection, "GET "+testItem, "GET "+testItem)
}

func TestAPIServiceNativeNoOpAndHistoricalCoreDestroy(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	p.metadata()["name"], p.spec()["group"] = "v1.", ""
	p.spec()["group-priority-minimum"] = int64(0)
	p.inputs["ignore-labels"] = []any{"["}
	lib := p.libs["registration"]
	rules := lib.Constraints
	lib.Constraints = nil
	p.apply("settled", runtime.DecisionCreate)
	target := collection + "/v1."
	requests := s.takeRequests()
	requireTrace(t, requests, "POST "+collection, "GET "+target, "GET "+target)
	require.JSONEq(t, `{"kind":"APIService","apiVersion":"apiregistration.k8s.io/v1",
		"metadata":{"name":"v1.","creationTimestamp":null},
		"spec":{"version":"v1","groupPriorityMinimum":0,"versionPriority":10},"status":{}}`,
		requests[0].body)
	exec, saved, _ := p.plan("settled", runtime.DecisionNoOp)
	requireTrace(t, s.takeRequests(), "GET "+target, "GET "+target)
	lib.Constraints = rules

	_, err := p.executor("settled").Plan(t.Context())
	requireNativeError(t, err, "spec.group-priority-minimum must be between 1 and 20000")
	require.ErrorContains(t, err, "invalid ignore-labels expression")
	requireTrace(t, s.takeRequests(), "GET "+target, "GET "+target)
	_, err = exec.ApplyPlan(t.Context(), saved)
	requireNativeError(t, err, "spec.group-priority-minimum must be between 1 and 20000")
	require.ErrorContains(t, err, "invalid ignore-labels expression")
	require.Empty(t, s.takeRequests())

	exec, saved, _ = p.plan("settled", runtime.DecisionDestroy)
	s.takeRequests()
	_, err = exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	requireTrace(t, s.takeRequests(), "GET "+target, "GET "+target, "DELETE "+target)
	require.Nil(t, s.snapshot())
}

func TestAPIServiceSavedMetadataFilters(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	initial := map[string]any{
		"hidden/keep": "old", "hidden/remove": "remove", "kubernetes.io/managed": "explicit",
	}
	p.metadata()["annotations"], p.metadata()["labels"] = initial, initial
	p.apply("settled", runtime.DecisionCreate)
	s.takeRequests()
	s.change(func(obj *api.APIService) {
		for _, fields := range []map[string]string{obj.Annotations, obj.Labels} {
			fields["hidden/unmanaged"] = "external"
			fields["external/removed"] = "will disappear"
			fields["kubernetes.io/native"] = "server"
			fields["deprecated.daemonset.template.generation"] = "1"
		}
	})
	desired := map[string]any{
		"hidden/keep": "new", "hidden/add": "added", "kubernetes.io/managed": "explicit",
	}
	p.metadata()["annotations"], p.metadata()["labels"] = desired, desired
	p.inputs["ignore-annotations"], p.inputs["ignore-labels"] = []any{"^hidden/"}, []any{"^hidden/"}
	exec, saved, _ := p.plan("settled", runtime.DecisionUpdate)
	s.change(func(obj *api.APIService) {
		delete(obj.Annotations, "external/removed")
		delete(obj.Labels, "external/removed")
	})
	result, err := exec.ApplyPlan(t.Context(), saved)
	require.NoError(t, err)
	expectedMetadata := map[string]any{"name": testName, "annotations": desired, "labels": desired}
	require.Equal(t, expectedMetadata, result.Outputs["metadata"])
	requests := s.takeRequests()
	requireTrace(t, requests, "GET "+testItem, "GET "+testItem, "GET "+testItem,
		"PATCH "+testItem, "GET "+testItem, "GET "+testItem)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"add","path":"/metadata/annotations/hidden~1add","value":"added"},
		{"op":"replace","path":"/metadata/annotations/hidden~1keep","value":"new"},
		{"op":"remove","path":"/metadata/annotations/hidden~1remove"},
		{"op":"add","path":"/metadata/labels/hidden~1add","value":"added"},
		{"op":"replace","path":"/metadata/labels/hidden~1keep","value":"new"},
		{"op":"remove","path":"/metadata/labels/hidden~1remove"}
	]`, requests[3].body)
	expected := map[string]string{
		"hidden/keep": "new", "hidden/add": "added", "kubernetes.io/managed": "explicit",
		"hidden/unmanaged": "external", "kubernetes.io/native": "server",
		"deprecated.daemonset.template.generation": "1",
	}
	require.Equal(t, expected, s.snapshot().Annotations)
	require.Equal(t, expected, s.snapshot().Labels)

	p.inputs["ignore-annotations"], p.inputs["ignore-labels"] = nil, nil
	result = p.apply("settled", runtime.DecisionUpdate)
	require.Equal(t, expectedMetadata, result.Outputs["metadata"])
	requests = s.takeRequests()
	requireTrace(t, requests, "GET "+testItem, "GET "+testItem, "GET "+testItem,
		"PATCH "+testItem, "GET "+testItem, "GET "+testItem)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"remove","path":"/metadata/annotations/hidden~1unmanaged"},
		{"op":"remove","path":"/metadata/labels/hidden~1unmanaged"}
	]`, requests[3].body)
	delete(expected, "hidden/unmanaged")
	require.Equal(t, expected, s.snapshot().Annotations)
	require.Equal(t, expected, s.snapshot().Labels)
}

func TestAPIServiceSavedMetadataEmptyProjection(t *testing.T) {
	s, cfg := newAPIServer(t)
	p := newServicePlanner(t, cfg)
	p.apply("settled", runtime.DecisionCreate)
	s.takeRequests()
	s.change(func(obj *api.APIService) {
		obj.Annotations = map[string]string{"ignored/external": "annotation"}
		obj.Labels = map[string]string{"ignored/external": "label"}
	})
	p.inputs["ignore-annotations"] = []any{"^ignored/"}
	p.inputs["ignore-labels"] = []any{"^ignored/"}
	p.metadata()["annotations"] = map[string]any{"first": "annotation"}
	p.metadata()["labels"] = map[string]any{"first": "label"}
	result := p.apply("settled", runtime.DecisionUpdate)
	require.Equal(t, map[string]any{
		"name":        testName,
		"annotations": map[string]any{"first": "annotation"},
		"labels":      map[string]any{"first": "label"},
	}, result.Outputs["metadata"])
	requests := s.takeRequests()
	requireTrace(t, requests, "GET "+testItem, "GET "+testItem, "GET "+testItem,
		"PATCH "+testItem, "GET "+testItem, "GET "+testItem)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"add","path":"/metadata/annotations","value":{"first":"annotation"}},
		{"op":"add","path":"/metadata/labels","value":{"first":"label"}}
	]`, requests[3].body)
	require.Equal(t, map[string]string{"first": "annotation"}, s.snapshot().Annotations)
	require.Equal(t, map[string]string{"first": "label"}, s.snapshot().Labels)
}
