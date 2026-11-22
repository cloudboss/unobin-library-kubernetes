package v1

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestStatefulSetSavedInvalidInputs(t *testing.T) {
	for _, test := range []struct {
		name       string
		change     func(*StatefulSetResource)
		diagnostic string
	}{
		{"zero revision", func(r *StatefulSetResource) {
			r.Spec.RevisionHistoryLimit = new(int64(0))
		}, "constraints["},
		{"negative revision", func(r *StatefulSetResource) {
			r.Spec.RevisionHistoryLimit = new(int64(-1))
		}, "constraints["},
		{"missing name", func(r *StatefulSetResource) { r.Metadata.Name = nil }, "constraints["},
		{"conflicting name", func(r *StatefulSetResource) {
			r.Metadata.GenerateName = new("example-")
		}, "constraints["},
		{"invalid policy", func(r *StatefulSetResource) {
			r.Spec.PodManagementPolicy = new("Other")
		}, "constraints["},
		{"scalar quantity", func(r *StatefulSetResource) {
			r.Spec.Template.Spec.Volume = new([]Volume{{Name: new("scratch"),
				EmptyDir: &EmptyDirVolume{SizeLimit: new("malformed")}}})
		}, "sizeLimit"},
		{"map quantity", func(r *StatefulSetResource) {
			(*r.Spec.Template.Spec.Container)[0].Resources = &Resources{
				Requests: new(map[string]string{"cpu": "malformed"}),
			}
		}, "requests"},
	} {
		for _, decision := range []runtime.Decision{runtime.DecisionNoOp, runtime.DecisionReplace} {
			if decision == runtime.DecisionNoOp && test.diagnostic != "constraints[" {
				continue
			}
			t.Run(test.name+"/"+string(decision), func(t *testing.T) {
				var stored *appsv1.StatefulSet
				mutations := 0
				c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method != "GET" {
						mutations++
					}
					switch req.Method {
					case "POST":
						stored = &appsv1.StatefulSet{}
						require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
						defaultStatefulSet(stored)
					case "DELETE":
						stored = nil
						require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(stored))
				})
				source, err := os.ReadFile("testdata/ub/stateful-set/state/factory.ub")
				require.NoError(t, err)
				parsed, err := syntax.ParseSource("factory.ub", source)
				require.NoError(t, err)
				lib := &runtime.Library{Name: "kubernetes-apps-v1",
					Configuration: config.LibraryConfiguration(),
					Resources: map[string]runtime.ResourceRegistration{
						"stateful-set": statefulSetResourceRegistration(),
					},
				}
				addNativeStatefulSetConstraints(t, lib)
				libs := map[string]*runtime.Library{"apps": lib}
				store, err := local.NewStore(t.TempDir(), "native-stateful-set", "test", encrypters.Noop{})
				require.NoError(t, err)
				executor := func(r *StatefulSetResource) *runtime.Executor {
					encoded, err := ub.Marshal(r)
					require.NoError(t, err)
					var input map[string]any
					require.NoError(t, ub.Unmarshal(encoded, &input))
					return &runtime.Executor{
						DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
						SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store,
						Inputs: map[string]any{"server": *c.Server, "token": "first",
							"metadata": input["metadata"], "spec": input["spec"]},
						Factory: state.FactoryInfo{Name: "stateful-set", Version: "v0", ContentRevision: "c0"},
					}
				}
				saved := func(p *runtime.Plan) *runtime.PlanFile {
					encoded, err := runtime.EncodePlan(p)
					require.NoError(t, err)
					file, err := runtime.DecodePlan(encoded)
					require.NoError(t, err)
					return file
				}
				initial := executor(statefulSet())
				p, err := initial.Plan(t.Context())
				require.NoError(t, err)
				_, err = initial.ApplyPlan(t.Context(), saved(p))
				require.NoError(t, err)
				desired := statefulSet()
				if decision == runtime.DecisionReplace {
					desired.Spec.ServiceName = "replacement"
				}
				valid := executor(desired)
				p, err = valid.Plan(t.Context())
				require.NoError(t, err)
				file := saved(p)
				found := false
				for _, step := range file.Steps {
					if step.Address == "resource.workload" {
						require.Equal(t, decision, step.Decision)
						found = true
					}
				}
				require.True(t, found)
				if decision == runtime.DecisionNoOp {
					_, err = valid.ApplyPlan(t.Context(), file)
					require.NoError(t, err)
					p, err = valid.Plan(t.Context())
					require.NoError(t, err)
					file = saved(p)
				}
				test.change(desired)
				invalid := executor(desired)
				if test.diagnostic == "constraints[" {
					_, err = invalid.Plan(t.Context())
					require.ErrorContains(t, err, "resource.workload: schema: constraints[")
					rule := map[string]string{
						"zero revision":     "constraints[0]",
						"negative revision": "constraints[0]",
						"missing name":      "exactly-one-of [metadata.name, metadata.generate-name]",
						"conflicting name":  "exactly-one-of [metadata.name, metadata.generate-name]",
						"invalid policy":    "constraints[2]",
					}[test.name]
					require.ErrorContains(t, err, rule)
				} else {
					known, err := invalid.Plan(t.Context())
					require.NoError(t, err)
					_, err = invalid.ApplyPlan(t.Context(), saved(known))
					require.ErrorContains(t, err, test.diagnostic)
				}
				require.Equal(t, 1, mutations)
				file.Inputs = invalid.Inputs
				for i := range file.Steps {
					step := &file.Steps[i]
					if step.Address == "resource.workload" {
						step.Inputs["metadata"] = invalid.Inputs["metadata"]
						step.Inputs["spec"] = invalid.Inputs["spec"]
					}
				}
				_, err = invalid.ApplyPlan(t.Context(), file)
				require.ErrorContains(t, err, "resource.workload")
				require.ErrorContains(t, err, test.diagnostic)
				require.Equal(t, 1, mutations)
				require.NotNil(t, stored)
			})
		}
	}
}

func TestDeferredStatefulSetNativeConstraints(t *testing.T) {
	for _, test := range []struct {
		kind  string
		value string
		valid bool
	}{
		{"name", "replacement", true}, {"name", "BAD", false},
		{"known-name", "", false}, {"known-spec", "", false},
		{"scalar", "1Gi", true}, {"scalar", "", false}, {"scalar", "malformed", false},
		{"map", "100m", true}, {"map", "", false}, {"map", "malformed", false},
	} {
		t.Run(test.kind+"/"+test.value, func(t *testing.T) {
			objects := map[string]*appsv1.StatefulSet{}
			var mutations []string
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				name := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
				if req.Method != "GET" {
					mutations = append(mutations, req.Method+" "+name)
				}
				switch req.Method {
				case "POST":
					obj := &appsv1.StatefulSet{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
					defaultStatefulSet(obj)
					if obj.Name == "seed" {
						obj.Annotations = map[string]string{"value": test.value}
					}
					objects[obj.Name] = obj
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				case "DELETE":
					delete(objects, name)
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
				case "GET":
					obj := objects[name]
					if obj == nil {
						apiError(t, w, 404)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				default:
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
				}
			})
			lib := &runtime.Library{Name: "kubernetes-apps-v1",
				Configuration: config.LibraryConfiguration(),
				Resources: map[string]runtime.ResourceRegistration{
					"stateful-set": statefulSetResourceRegistration(),
				},
			}
			addNativeStatefulSetConstraints(t, lib)
			libs := map[string]*runtime.Library{"apps": lib}
			store, err := local.NewStore(t.TempDir(), "deferred-stateful-set", "test", encrypters.Noop{})
			require.NoError(t, err)
			executor := func(fixture string, inputs map[string]any) *runtime.Executor {
				source, err := os.ReadFile("testdata/ub/stateful-set/" + fixture + "/factory.ub")
				require.NoError(t, err)
				parsed, err := syntax.ParseSource("factory.ub", source)
				require.NoError(t, err)
				return &runtime.Executor{
					DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
					Libraries: libs, Inputs: inputs, Store: store,
					Factory: state.FactoryInfo{Name: "deferred", Version: "v0", ContentRevision: "c0"},
				}
			}
			apply := func(exec *runtime.Executor, p *runtime.Plan) error {
				encoded, err := runtime.EncodePlan(p)
				require.NoError(t, err)
				file, err := runtime.DecodePlan(encoded)
				require.NoError(t, err)
				_, err = exec.ApplyPlan(t.Context(), file)
				return err
			}
			encoded, err := ub.Marshal(statefulSet())
			require.NoError(t, err)
			var input map[string]any
			require.NoError(t, ub.Unmarshal(encoded, &input))
			inputs := map[string]any{"server": *c.Server, "token": "first",
				"metadata": input["metadata"], "spec": input["spec"]}
			initial := executor("state", inputs)
			p, err := initial.Plan(t.Context())
			require.NoError(t, err)
			require.NoError(t, apply(initial, p))
			mutations = nil
			exec := executor("deferred-"+test.kind, map[string]any{"server": *c.Server})
			p, err = exec.Plan(t.Context())
			if strings.HasPrefix(test.kind, "known-") {
				require.ErrorContains(t, err, "resource.workload: schema: constraints[")
				require.Empty(t, mutations)
				return
			}
			require.NoError(t, err)
			require.Empty(t, mutations)
			err = apply(exec, p)
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, []string{"POST statefulsets", "DELETE example", "POST statefulsets"},
					mutations)
			} else {
				require.ErrorContains(t, err, "resource.workload")
				switch test.kind {
				case "name":
					require.ErrorContains(t, err, "constraints[")
				case "scalar":
					require.ErrorContains(t, err, "sizeLimit")
				case "map":
					require.ErrorContains(t, err, "requests")
				}
				require.Equal(t, []string{"POST statefulsets"}, mutations)
				require.Contains(t, objects, "example")
			}
		})
	}
}
