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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestSavedStatePlanning(t *testing.T) {
	type change struct {
		name     string
		edit     func(*JobResource)
		decision runtime.Decision
		invalid  bool
		prepare  func(*JobResource)
	}
	cases := []change{
		{name: "no change", decision: runtime.DecisionNoOp, edit: func(*JobResource) {}},
		{name: "equal computed name conflicts with generate name", invalid: true,
			prepare: func(r *JobResource) {
				r.Metadata.Name = nil
				r.Metadata.GenerateName = new("example-")
			},
			edit: func(r *JobResource) { r.Metadata.Name = new("example") }},
		{
			name: "empty policy container name", decision: runtime.DecisionNoOp,
			edit: func(r *JobResource) {
				r.Spec.PodFailurePolicy.Rule[0].OnExitCodes.ContainerName = new("")
			},
		},
		{
			name: "omit empty policy container name", decision: runtime.DecisionNoOp,
			prepare: func(r *JobResource) {
				r.Spec.PodFailurePolicy.Rule[0].OnExitCodes.ContainerName = new("")
			},
			edit: func(r *JobResource) {
				r.Spec.PodFailurePolicy.Rule[0].OnExitCodes.ContainerName = nil
			},
		},
		{name: "name omitted", invalid: true, edit: func(r *JobResource) { r.Metadata.Name = nil }},
		{name: "mode omitted", decision: runtime.DecisionNoOp, edit: func(r *JobResource) {
			r.Spec.CompletionMode = nil
		}},
		{name: "selector omitted", decision: runtime.DecisionNoOp, edit: func(r *JobResource) {
			r.Spec.Selector = nil
		}},
		{name: "computed pod omitted", decision: runtime.DecisionNoOp, edit: func(r *JobResource) {
			r.Spec.Template.Spec.ReadinessGate = nil
			r.Spec.Template.Spec.Hostname = nil
			r.Spec.Template.Spec.ImagePullSecrets = nil
			(*r.Spec.Template.Spec.Container)[0].Resources = nil
			(*r.Spec.Template.Spec.Container)[0].ImagePullPolicy = nil
		}},
		{name: "template spec removed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.Template.Spec = nil
		}},
		{name: "template labels removed", decision: runtime.DecisionReplace,
			edit: func(r *JobResource) { r.Spec.Template.Metadata.Labels = nil }},
		{name: "template annotations removed", decision: runtime.DecisionReplace,
			edit: func(r *JobResource) { r.Spec.Template.Metadata.Annotations = nil }},
		{name: "image removed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			(*r.Spec.Template.Spec.Container)[0].Image = nil
		}},
		{name: "args changed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			(*r.Spec.Template.Spec.Container)[0].Args = new([]string{"other"})
		}},
		{name: "template name", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.Template.Metadata.Name = new("other")
		}},
		{name: "template computed name omission", decision: runtime.DecisionNoOp,
			edit: func(r *JobResource) { r.Spec.Template.Metadata.Name = nil }},
		{name: "name changed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Metadata.Name = new("other")
		}},
		{name: "namespace changed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Metadata.Namespace = new("other")
		}},
		{name: "generate name added", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Metadata.Name = nil
			r.Metadata.GenerateName = new("other-")
		}},
		{name: "completions changed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.Completions = new(int64(3))
		}},
		{name: "completions removal", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.Completions = nil
		}},
		{name: "index removal", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.BackoffLimitPerIndex = nil
		}},
		{name: "policy removal", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.PodFailurePolicy = nil
		}},
		{name: "policy ordered change", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.PodFailurePolicy.Rule[0],
				r.Spec.PodFailurePolicy.Rule[1] = r.Spec.PodFailurePolicy.Rule[1],
				r.Spec.PodFailurePolicy.Rule[0]
		}},
		{name: "selector changed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.Selector.MatchLabels = new(map[string]string{"other": "yes"})
		}},
		{name: "mode changed", decision: runtime.DecisionReplace, edit: func(r *JobResource) {
			r.Spec.CompletionMode = new("NonIndexed")
		}},
		{name: "TTL changed", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Spec.TTLSecondsAfterFinished = new("70")
		}},
		{name: "TTL removed", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Spec.TTLSecondsAfterFinished = nil
		}},
		{name: "max failed removed", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Spec.MaxFailedIndexes = nil
		}},
		{name: "parallelism", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Spec.Parallelism = new(int64(4))
		}},
		{name: "backoff", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Spec.BackoffLimit = new(int64(4))
		}},
		{name: "active removed", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Spec.ActiveDeadlineSeconds = nil
		}},
		{name: "labels omitted", decision: runtime.DecisionNoOp, edit: func(r *JobResource) {
			r.Metadata.Labels = nil
		}},
		{name: "labels cleared", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Metadata.Labels = new(map[string]string{})
		}},
		{name: "annotations omitted", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Metadata.Annotations = nil
		}},
		{name: "quantity equivalent", decision: runtime.DecisionNoOp, edit: func(r *JobResource) {
			(*r.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{
				"example.com/value": "0.1"})
		}},
		{name: "negative wraps to two", invalid: true, edit: func(r *JobResource) {
			r.Spec.Parallelism = new(int64(-4294967294))
		}},
		{name: "negative completion wraps", invalid: true, edit: func(r *JobResource) {
			r.Spec.Completions = new(int64(-4294967294))
		}},
		{name: "negative probe wraps", invalid: true, edit: func(r *JobResource) {
			(*r.Spec.Template.Spec.Container)[0].LivenessProbe.FailureThreshold = new(int64(-4294967294))
		}},
		{name: "negative topology wraps", invalid: true, edit: func(r *JobResource) {
			(*r.Spec.Template.Spec.TopologySpreadConstraint)[0].MaxSkew = new(int64(-4294967294))
		}},
		{name: "port wraps", invalid: true, edit: func(r *JobResource) {
			(*(*r.Spec.Template.Spec.Container)[0].Port)[0].ContainerPort = 4294967298
		}},
		{name: "wide valid parallelism", decision: runtime.DecisionUpdate, edit: func(r *JobResource) {
			r.Spec.Parallelism = new(int64(4294967298))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stored *batchv1.Job
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.Method {
				case "POST":
					stored = &batchv1.Job{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
					stored.UID = "uid"
					if stored.Name == "" {
						stored.Name = "example"
					}
					if stored.Spec.Template.Spec.ServiceAccountName == "" {
						stored.Spec.Template.Spec.ServiceAccountName = "default"
					}
					if stored.Spec.Template.Spec.SchedulerName == "" {
						stored.Spec.Template.Spec.SchedulerName = "default-scheduler"
					}
					if stored.Spec.Template.Spec.Containers[0].ImagePullPolicy == "" {
						stored.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
					}
				case "PATCH":
					patchJob(t, req, stored)
				case "DELETE":
					stored = nil
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
					return
				case "GET":
					if stored == nil {
						apiError(t, w, 404)
						return
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			source, err := os.ReadFile("testdata/ub/state/factory.ub")
			require.NoError(t, err)
			parsed, err := syntax.ParseSource("factory.ub", source)
			require.NoError(t, err)
			libs := map[string]*runtime.Library{"batch": {
				Name: "kubernetes-batch-v1", Configuration: config.LibraryConfiguration(),
				Resources: map[string]runtime.ResourceRegistration{"job": testJobRegistration(t)},
			}}
			addNativeJobConstraints(t, libs["batch"])
			store, err := local.NewStore(t.TempDir(), "job", "test", encrypters.Noop{})
			require.NoError(t, err)
			executor := func(r *JobResource) *runtime.Executor {
				data, err := ub.Marshal(r)
				require.NoError(t, err)
				var input map[string]any
				require.NoError(t, ub.Unmarshal(data, &input))
				input["server"] = *c.Server
				input["token"] = "first"
				return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
					SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: input, Store: store,
					Factory: state.FactoryInfo{Name: "job", Version: "v0", ContentRevision: "c0"}}
			}
			check := func(r *JobResource, want runtime.Decision, invalid bool) (*runtime.Executor,
				*runtime.Plan) {
				t.Helper()
				exec := executor(r)
				plan, err := exec.Plan(t.Context())
				if invalid {
					require.ErrorContains(t, err, "resource.workload: schema: constraints[")
					return exec, nil
				}
				require.NoError(t, err)
				found := false
				for _, step := range plan.Steps {
					if step.Address == "resource.workload" {
						require.Equal(t, want, step.Decision)
						found = true
					}
				}
				require.True(t, found)
				return exec, plan
			}
			input := fullInput(t)
			if tc.prepare != nil {
				tc.prepare(input)
			}
			exec, plan := check(input, runtime.DecisionCreate, false)
			encoded, err := runtime.EncodePlan(plan)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(encoded)
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), file)
			require.NoError(t, err)
			stored.Generation = 999
			stored.ResourceVersion = "changed"
			stored.Status = batchv1.JobStatus{Active: 9, Succeeded: 9, Failed: 9}
			check(input, runtime.DecisionNoOp, false)
			changed := fullInput(t)
			if tc.prepare != nil {
				tc.prepare(changed)
			}
			tc.edit(changed)
			check(changed, tc.decision, tc.invalid)
			if tc.decision == runtime.DecisionReplace {
				out, err := input.output(stored, *c.Server)
				require.NoError(t, err)
				_, err = changed.Update(t.Context(), c, prior{Inputs: *input, Outputs: out})
				require.ErrorContains(t, err, "replacement")
			}
			stored.UID = "replacement"
			_, err = executor(input).Plan(t.Context())
			require.ErrorContains(t, err, "UID changed")
			stored = nil
			check(input, runtime.DecisionCreate, false)
		})
	}
}
