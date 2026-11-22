package v1

import (
	"encoding/json"
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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type cronJobComputedCase struct {
	name    string
	set     func(*CronJobResource, bool)
	omit    func(*CronJobResource)
	observe func(*batchv1.CronJob, bool)
}

func cronJobComputedCases() []cronJobComputedCase {
	return []cronJobComputedCase{
		{
			name: "job template name",
			set: func(r *CronJobResource, other bool) {
				name := "example"
				if other {
					name = "other"
				}
				r.Spec.JobTemplate.Metadata.Name = &name
			},
			omit: func(r *CronJobResource) { r.Spec.JobTemplate.Metadata.Name = nil },
			observe: func(obj *batchv1.CronJob, other bool) {
				obj.Spec.JobTemplate.Name = "example"
				if other {
					obj.Spec.JobTemplate.Name = "other"
				}
			},
		},
		{
			name: "completion mode",
			set: func(r *CronJobResource, other bool) {
				mode := "NonIndexed"
				if other {
					mode = "Indexed"
				}
				r.Spec.JobTemplate.Spec.CompletionMode = &mode
			},
			omit: func(r *CronJobResource) { r.Spec.JobTemplate.Spec.CompletionMode = nil },
			observe: func(obj *batchv1.CronJob, other bool) {
				mode := batchv1.NonIndexedCompletion
				if other {
					mode = batchv1.IndexedCompletion
				}
				obj.Spec.JobTemplate.Spec.CompletionMode = &mode
			},
		},
		{
			name: "selector",
			set: func(r *CronJobResource, other bool) {
				value := "app"
				if other {
					value = "other"
				}
				r.Spec.JobTemplate.Spec.Selector = &Selector{MatchLabels: new(map[string]string{"app": value})}
			},
			omit: func(r *CronJobResource) { r.Spec.JobTemplate.Spec.Selector = nil },
			observe: func(obj *batchv1.CronJob, other bool) {
				value := "app"
				if other {
					value = "other"
				}
				obj.Spec.JobTemplate.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": value},
				}
			},
		},
		{
			name: "template name",
			set: func(r *CronJobResource, other bool) {
				value := "pod"
				if other {
					value = "other"
				}
				r.Spec.JobTemplate.Spec.Template.Metadata.Name = &value
			},
			omit: func(r *CronJobResource) { r.Spec.JobTemplate.Spec.Template.Metadata.Name = nil },
			observe: func(obj *batchv1.CronJob, other bool) {
				obj.Spec.JobTemplate.Spec.Template.Name = "pod"
				if other {
					obj.Spec.JobTemplate.Spec.Template.Name = "other"
				}
			},
		},
		{
			name: "image pull policy",
			set: func(r *CronJobResource, other bool) {
				value := "IfNotPresent"
				if other {
					value = "Always"
				}
				(*r.Spec.JobTemplate.Spec.Template.Spec.Container)[0].ImagePullPolicy = &value
			},
			omit: func(r *CronJobResource) {
				(*r.Spec.JobTemplate.Spec.Template.Spec.Container)[0].ImagePullPolicy = nil
			},
			observe: func(obj *batchv1.CronJob, other bool) {
				obj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
				if other {
					obj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullAlways
				}
			},
		},
		{
			name: "resources",
			set: func(r *CronJobResource, other bool) {
				value := "100m"
				if other {
					value = "200m"
				}
				(*r.Spec.JobTemplate.Spec.Template.Spec.Container)[0].Resources = &Resources{
					Requests: new(map[string]string{"cpu": value}),
				}
			},
			omit: func(r *CronJobResource) {
				(*r.Spec.JobTemplate.Spec.Template.Spec.Container)[0].Resources = nil
			},
			observe: func(obj *batchv1.CronJob, other bool) {
				value := "100m"
				if other {
					value = "200m"
				}
				obj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse(value),
				}
			},
		},
		{
			name: "readiness gates",
			set: func(r *CronJobResource, other bool) {
				value := "example.com/ready"
				if other {
					value = "example.com/other"
				}
				r.Spec.JobTemplate.Spec.Template.Spec.ReadinessGate =
					new([]ReadinessGate{{ConditionType: value}})
			},
			omit: func(r *CronJobResource) { r.Spec.JobTemplate.Spec.Template.Spec.ReadinessGate = nil },
			observe: func(obj *batchv1.CronJob, other bool) {
				value := "example.com/ready"
				if other {
					value = "example.com/other"
				}
				obj.Spec.JobTemplate.Spec.Template.Spec.ReadinessGates = []corev1.PodReadinessGate{{
					ConditionType: corev1.PodConditionType(value),
				}}
			},
		},
	}
}

func TestCronJobComputedReintroductionPlanning(t *testing.T) {
	for _, tc := range cronJobComputedCases() {
		for _, initialExplicit := range []bool{false, true} {
			name := "initial omission"
			if initialExplicit {
				name = "applied omission"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				var stored *batchv1.CronJob
				var methods []string
				c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
					methods = append(methods, req.Method)
					switch req.Method {
					case "POST":
						stored = &batchv1.CronJob{}
						require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
						stored.UID = "uid"
						tc.observe(stored, false)
					case "PUT":
						require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
					case "GET":
					default:
						t.Fatalf("unexpected %s", req.Method)
					}
					require.NoError(t, json.NewEncoder(w).Encode(stored))
				})
				source, err := os.ReadFile("testdata/ub/cron-job-state/factory.ub")
				require.NoError(t, err)
				parsed, err := syntax.ParseSource("factory.ub", source)
				require.NoError(t, err)
				path := t.TempDir()
				executor := func(r *CronJobResource) *runtime.Executor {
					libs := map[string]*runtime.Library{"batch": {
						Name: "kubernetes-batch-v1", Configuration: config.LibraryConfiguration(),
						Resources: map[string]runtime.ResourceRegistration{"cron-job": cronJobTestRegistration(t)},
					}}
					data, err := ub.Marshal(r)
					require.NoError(t, err)
					var input map[string]any
					require.NoError(t, ub.Unmarshal(data, &input))
					input["server"] = *c.Server
					input["token"] = "first"
					store, err := local.NewStore(path, "cron-job", "test", encrypters.Noop{})
					require.NoError(t, err)
					return &runtime.Executor{
						DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
						SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: input,
						Store: store, Factory: state.FactoryInfo{
							Name: "cron-job", Version: "v0", ContentRevision: "c0",
						},
					}
				}
				plan := func(r *CronJobResource, replace bool) (*runtime.Executor, *runtime.Plan) {
					exec := executor(r)
					planned, err := exec.Plan(t.Context())
					require.NoError(t, err)
					for _, step := range planned.Steps {
						if step.Address == "resource.workload" {
							if replace {
								require.Equal(t, runtime.DecisionReplace, step.Decision)
							} else {
								require.Contains(t, []runtime.Decision{
									runtime.DecisionCreate, runtime.DecisionNoOp, runtime.DecisionUpdate,
								}, step.Decision)
							}
							return exec, planned
						}
					}
					t.Fatal("missing workload plan")
					return nil, nil
				}
				apply := func(r *CronJobResource) {
					exec, planned := plan(r, false)
					data, err := runtime.EncodePlan(planned)
					require.NoError(t, err)
					file, err := runtime.DecodePlan(data)
					require.NoError(t, err)
					_, err = exec.ApplyPlan(t.Context(), file)
					require.NoError(t, err)
				}
				input := cronJob()
				input.DeleteTimeout = new("2s")
				tc.omit(input)
				if initialExplicit {
					tc.set(input, false)
				}
				apply(input)
				stored.Status = batchv1.CronJobStatus{}
				if initialExplicit {

					tc.omit(input)
					apply(input)
					_, omitted := plan(input, false)
					for _, step := range omitted.Steps {
						if step.Address != "resource.workload" {
							continue
						}
						var saved CronJobResource
						require.NoError(t, runtime.Decode(&saved, step.PriorInputs))
						want, err := ub.Marshal(input)
						require.NoError(t, err)
						got, err := ub.Marshal(&saved)
						require.NoError(t, err)
						require.Equal(t, string(want), string(got))
					}
				}
				methods = nil
				tc.set(input, false)
				apply(input)
				require.Equal(t, []string{"GET"}, methods)
				require.Equal(t, "uid", string(stored.UID))
				require.NotContains(t, methods, "POST")
				require.NotContains(t, methods, "DELETE")
				input.Spec.JobTemplate.Spec.Parallelism = new(int64(2))
				apply(input)
				require.Equal(t, int32(2), *stored.Spec.JobTemplate.Spec.Parallelism)
				require.Equal(t, "uid", string(stored.UID))
				tc.set(input, true)
				plan(input, true)
			})
		}
	}
}

func TestCronJobComputedUpdateBaseline(t *testing.T) {
	for _, tc := range cronJobComputedCases() {
		for _, latest := range []bool{false, true} {
			name := "saved"
			if latest {
				name = "observed"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				input := cronJob()
				tc.omit(input)
				obj, err := input.prepare(nil)
				require.NoError(t, err)
				obj.UID = "uid"
				tc.observe(obj, false)
				saved, err := input.output(obj, "server", input.Metadata)
				require.NoError(t, err)
				tc.observe(obj, latest)
				var methods []string
				c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
					methods = append(methods, req.Method)
					if req.Method == "PUT" {
						require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				saved.Server = *c.Server
				p := cronJobPrior{Inputs: *input, Outputs: saved}
				if latest {
					p.Observed, err = input.output(obj, *c.Server, input.Metadata)
					require.NoError(t, err)
				}
				before, err := ub.Marshal(input)
				require.NoError(t, err)
				desired := cronJob()
				tc.set(desired, latest)
				out, err := desired.Update(t.Context(), c, p)
				require.NoError(t, err)
				require.Equal(t, "uid", out.UID)
				require.Equal(t, []string{"GET", "PUT", "GET"}, methods)
				after, err := ub.Marshal(input)
				require.NoError(t, err)
				require.Equal(t, before, after)
				methods = nil
				tc.set(desired, !latest)
				_, err = desired.Update(t.Context(), c, p)
				require.ErrorContains(t, err, "replacement")
				require.NotContains(t, methods, "PUT")
			})
		}
	}
}
