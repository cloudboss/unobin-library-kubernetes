package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

var nativeCronJobSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/batch/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

var nativeCronJobRules = sync.OnceValues(func() ([]lang.ConstraintEntry, error) {
	schema, err := nativeCronJobSchema()
	if err != nil {
		return nil, err
	}
	entries, errors := lang.ParseSpecs(schema.Resources["cron-job"].Constraints)
	return entries, errors.Err()
})

func checkNativeCronJobInputs(t *testing.T, input any) []string {
	t.Helper()
	entries, err := nativeCronJobRules()
	require.NoError(t, err)
	encoded, err := ub.Marshal(input)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, ub.Unmarshal(encoded, &values))
	eval := func(expr lang.Expr, binds []lang.EachBinding) (any, error) {
		ctx := &runtime.EvalContext{Inputs: values, MissingAsNull: true}
		runtime.ApplyBindings(ctx, binds)
		return runtime.Eval(expr, ctx)
	}
	return lang.CheckConstraintEntries(entries, values, eval, lang.DisplayNodeRelative).Messages()
}

func nativeCronJobExecutor(
	t *testing.T, fixture string, lib *runtime.Library, store *local.Store, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	source, err := os.ReadFile("testdata/ub/" + fixture + "/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"batch": lib}
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs), SyntaxSource: &parsed.Factory.Body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "native-job", Version: "v0", ContentRevision: "c0"},
	}
}

func nativeCronJobInputs(t *testing.T, input *CronJobResource, server string) map[string]any {
	t.Helper()
	data, err := ub.Marshal(input)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, ub.Unmarshal(data, &values))
	values["server"] = server
	values["token"] = "first"
	return values
}

func savedCronJobPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	data, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(data)
	require.NoError(t, err)
	return file
}

func TestNativeCronJobPlanAndSavedApply(t *testing.T) {
	mutations := 0
	var stored *batchv1.CronJob
	c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			mutations++
		}
		switch req.Method {
		case http.MethodPost:
			stored = &batchv1.CronJob{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.Name, stored.UID = "example", "uid"
		case http.MethodPut:
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
		case http.MethodDelete:
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		}
		if stored == nil {
			apiError(t, w, 404)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	lib := &runtime.Library{
		Name: "kubernetes-batch-v1", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{"cron-job": cronJobTestRegistration(t)},
	}
	addNativeCronJobConstraints(t, lib)
	store, err := local.NewStore(t.TempDir(), "cron-job", "test", encrypters.Noop{})
	require.NoError(t, err)
	executor := func(input *CronJobResource) *runtime.Executor {
		return nativeCronJobExecutor(t, "cron-job-state", lib, store, nativeCronJobInputs(t, input,
			*c.Server))
	}
	input := cronJob()
	exec := executor(input)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
	require.NoError(t, err)
	before := mutations
	exec = executor(input)
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range plan.Steps {
		if step.Address == "resource.workload" {
			require.Equal(t, runtime.DecisionNoOp, step.Decision)
		}
	}
	_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, before, mutations)
	t.Run("otherwise no-op", func(t *testing.T) {
		invalid := cronJob()
		invalid.Metadata.Name = nil
		_, err := executor(invalid).Plan(t.Context())
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		require.Equal(t, before, mutations)
	})
	t.Run("saved no-op", func(t *testing.T) {
		exec := executor(cronJob())
		plan, err := exec.Plan(t.Context())
		require.NoError(t, err)
		file := savedCronJobPlan(t, plan)
		exec.Inputs["metadata"].(map[string]any)["name"] = ""
		file.Inputs["metadata"].(map[string]any)["name"] = ""
		for i := range file.Steps {
			if step := &file.Steps[i]; step.Address == "resource.workload" {
				step.Inputs["metadata"].(map[string]any)["name"] = ""
			}
		}
		_, err = exec.ApplyPlan(t.Context(), file)
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		require.Equal(t, before, mutations)
	})
	t.Run("TTL replacement", func(t *testing.T) {
		desired := cronJob()
		desired.Metadata.Name = new("replacement")
		desired.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = new("2147483648")
		exec := executor(desired)
		plan, err := exec.Plan(t.Context())
		if err == nil {
			_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
		}
		require.ErrorContains(t, err, "TTL")
		require.Equal(t, before, mutations)
	})
	for _, value := range []string{"2147483648", "malformed"} {
		t.Run("saved replacement "+value, func(t *testing.T) {
			desired := cronJob()
			desired.Metadata.Name = new("replacement")
			exec := executor(desired)
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			file := savedCronJobPlan(t, plan)
			cronJobPlanJobSpec(exec.Inputs)["ttl-seconds-after-finished"] = value
			cronJobPlanJobSpec(file.Inputs)["ttl-seconds-after-finished"] = value
			for i := range file.Steps {
				if step := &file.Steps[i]; step.Address == "resource.workload" {
					require.Equal(t, runtime.DecisionReplace, step.Decision)
					cronJobPlanJobSpec(step.Inputs)["ttl-seconds-after-finished"] = value
				}
			}
			_, err = exec.ApplyPlan(t.Context(), file)
			require.ErrorContains(t, err, "TTL")
			require.Equal(t, before, mutations)
		})
	}
	t.Run("destroy historical identity", func(t *testing.T) {
		lib.Constraints = nil
		historical := cronJob()
		historical.Metadata.Name = nil
		exec := executor(historical)
		plan, err := exec.Plan(t.Context())
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
		require.NoError(t, err)
		addNativeCronJobConstraints(t, lib)
		_, err = exec.Plan(t.Context())
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		exec.Destroy = true
		plan, err = exec.Plan(t.Context())
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
		require.NoError(t, err)
		require.Nil(t, stored)
	})
}

func TestNativeCronJobDeferredInputs(t *testing.T) {
	for _, test := range []struct {
		fixture string
		value   string
		replace bool
		valid   bool
	}{
		{"cron-job-deferred-job-identity", "template-prefix-", false, false},
		{"cron-job-deferred-job-identity", "template-prefix-", true, false},
		{"cron-job-deferred-pod-identity", "template-prefix-", false, false},
		{"cron-job-deferred-pod-identity", "template-prefix-", true, false},
		{"cron-job-deferred-schedule", "@every 0s", false, true},
		{"cron-job-deferred-schedule", "@every 0s", true, true},
		{"cron-job-deferred-schedule", "malformed", false, false},
		{"cron-job-deferred-schedule", "malformed", true, false},
		{"cron-job-deferred-map-quantity", "1Gi", false, true},
		{"cron-job-deferred-map-quantity", "1Gi", true, true},
		{"cron-job-deferred-map-quantity", "malformed", false, false},
		{"cron-job-deferred-map-quantity", "malformed", true, false},
		{"cron-job-deferred-numeric", "42", false, true},
		{"cron-job-deferred-numeric", "42", true, true},
		{"cron-job-deferred-numeric", "9223372036854775808", false, false},
		{"cron-job-deferred-numeric", "9223372036854775808", true, false},
		{"cron-job-deferred-native", "deferred", false, true},
		{"cron-job-deferred-native", "deferred", true, true},
		{"cron-job-deferred-native", "BAD", false, false},
		{"cron-job-deferred-native", "BAD", true, false},
		{"cron-job-deferred-quantity", "1Gi", false, true},
		{"cron-job-deferred-quantity", "1Gi", true, true},
		{"cron-job-deferred-quantity", "malformed", false, false},
		{"cron-job-deferred-quantity", "malformed", true, false},
		{"cron-job-deferred-ttl", "60", false, true},
		{"cron-job-deferred-ttl", "60", true, true},
		{"cron-job-deferred-ttl", "2147483648", false, false},
		{"cron-job-deferred-ttl", "2147483648", true, false},
	} {
		t.Run(fmt.Sprintf("%s/%s/replace=%t", test.fixture, test.value, test.replace),
			func(t *testing.T) {
				stored := map[string]*batchv1.CronJob{}
				var mutations []string
				c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method == http.MethodPost {
						obj := &batchv1.CronJob{}
						require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
						obj.UID = "uid"
						if obj.Name == "seed" {
							obj.Annotations = map[string]string{"value": test.value}
						}
						stored[obj.Name] = obj
						mutations = append(mutations, "POST "+obj.Name)
						require.NoError(t, json.NewEncoder(w).Encode(obj))
						return
					}
					if req.Method != http.MethodGet {
						mutations = append(mutations, req.Method+" "+req.URL.Path)
					}
					name := req.URL.Path[len("/apis/batch/v1/namespaces/default/cronjobs/"):]
					if req.Method == http.MethodDelete {
						delete(stored, name)
						require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
						return
					}
					if obj := stored[name]; obj != nil {
						require.NoError(t, json.NewEncoder(w).Encode(obj))
					} else {
						apiError(t, w, 404)
					}
				})
				lib := &runtime.Library{
					Name: "kubernetes-batch-v1", Configuration: config.LibraryConfiguration(),
					Resources: map[string]runtime.ResourceRegistration{"cron-job": cronJobTestRegistration(t)},
				}
				addNativeCronJobConstraints(t, lib)
				store, err := local.NewStore(t.TempDir(), "cron-job", "test", encrypters.Noop{})
				require.NoError(t, err)
				if test.replace {
					exec := nativeCronJobExecutor(t, "cron-job-state", lib, store,
						nativeCronJobInputs(t, cronJob(), *c.Server))
					plan, err := exec.Plan(t.Context())
					require.NoError(t, err)
					_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
					require.NoError(t, err)
					mutations = nil
				}
				exec := nativeCronJobExecutor(t, test.fixture, lib, store,
					map[string]any{"server": *c.Server, "value": test.value})
				plan, err := exec.Plan(t.Context())
				require.NoError(t, err)
				require.Empty(t, mutations)
				pending := false
				for _, step := range plan.Steps {
					if step.Address == "resource.workload" {
						pending = len(step.UnresolvedInputs) != 0
					}
				}
				require.True(t, pending)
				_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
				if test.valid {
					require.NoError(t, err)
					name := "replacement"
					if test.fixture == "cron-job-deferred-native" {
						name = test.value
					}
					want := []string{"POST seed", "POST " + name}
					if test.replace {
						want = []string{"POST seed", "DELETE /apis/batch/v1/namespaces/default/cronjobs/example",
							"POST " + name}
					}
					require.Equal(t, want, mutations)
				} else {
					require.Error(t, err)
					switch test.fixture {
					case "cron-job-deferred-job-identity", "cron-job-deferred-pod-identity":
						require.ErrorContains(t, err, "resource.workload: schema: constraints[")
						require.ErrorContains(t, err, "at-most-one-of")
					case "cron-job-deferred-native":
						require.ErrorContains(t, err, "resource.workload: schema: constraints[")
					case "cron-job-deferred-schedule":
						require.ErrorContains(t, err, "schedule")
					case "cron-job-deferred-map-quantity":
						require.ErrorContains(t, err, "requests")
					case "cron-job-deferred-numeric":
						require.ErrorContains(t, err, "runAsUser")
					case "cron-job-deferred-ttl":
						require.ErrorContains(t, err, "TTL")
					default:
						require.ErrorContains(t, err, "sizeLimit")
					}
					require.Equal(t, []string{"POST seed"}, mutations)
				}
			})
	}
}

func addNativeCronJobConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativeCronJobSchema()
	require.NoError(t, err)
	library.Constraints = map[string][]lang.ConstraintSpec{
		"resource.cron-job": schema.Resources["cron-job"].Constraints,
	}
}

func TestNativeCronJobInputs(t *testing.T) {
	require.Empty(t, checkNativeCronJobInputs(t, cronJob()))
	require.Empty(t, checkNativeCronJobInputs(t, cronJobFullInput(t)))
	for _, test := range []struct {
		name string
		set  func(*CronJobResource)
	}{
		{"missing identity", func(r *CronJobResource) { r.Metadata.Name = nil }},
		{"both identities", func(r *CronJobResource) { r.Metadata.GenerateName = new("prefix-") }},
		{"empty name", func(r *CronJobResource) { r.Metadata.Name = new("") }},
		{"invalid name", func(r *CronJobResource) { r.Metadata.Name = new("BAD") }},
		{"label", func(r *CronJobResource) {
			r.Metadata.Labels = new(map[string]string{"bad key": "value"})
		}},
		{"completion mode", func(r *CronJobResource) {
			r.Spec.JobTemplate.Spec.CompletionMode = new("bad")
		}},
		{"parallelism", func(r *CronJobResource) {
			r.Spec.JobTemplate.Spec.Parallelism = new(int64(-1))
		}},
		{"deadline", func(r *CronJobResource) {
			r.Spec.JobTemplate.Spec.ActiveDeadlineSeconds = new(int64(0))
		}},
		{"timeout", func(r *CronJobResource) { r.DeleteTimeout = new("0s") }},
		{"regex", func(r *CronJobResource) { r.IgnoreLabels = new([]string{"["}) }},
		{"exit zero", func(r *CronJobResource) {
			r.Spec.JobTemplate.Spec.PodFailurePolicy = &PodFailurePolicy{Rule: []PodFailureRule{{
				OnExitCodes: &ExitCodes{Values: []int64{0}},
			}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := cronJob()
			test.set(r)
			require.NotEmpty(t, checkNativeCronJobInputs(t, r))
		})
	}
	for _, policy := range []string{"Always", "OnFailure", "Never"} {
		r := cronJob()
		r.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy = &policy
		require.Empty(t, checkNativeCronJobInputs(t, r))
	}
	r := cronJob()
	r.Metadata.Name, r.Metadata.GenerateName = nil, new("generated-")
	r.Spec.JobTemplate.Spec.Template.Spec = nil
	require.Empty(t, checkNativeCronJobInputs(t, r))
}

func TestCronJobSavedReplacementParserPreflight(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       []string
		value      string
		diagnostic string
	}{
		{"cron", []string{"schedule"}, "malformed", "schedule"},
		{"scalar quantity", []string{"job-template", "spec", "template", "spec", "volume", "0",
			"empty-dir", "size-limit"}, "malformed", "sizeLimit"},
		{"map quantity", []string{"job-template", "spec", "template", "spec", "container", "0",
			"resources", "requests", "cpu"}, "malformed", "requests"},
		{"numeric", []string{"job-template", "spec", "template", "spec", "security-context",
			"run-as-user"}, "9223372036854775808", "runAsUser"},
		{"ttl", []string{"job-template", "spec", "ttl-seconds-after-finished"}, "2147483648", "TTL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stored *batchv1.CronJob
			var mutations []string
			c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "GET" {
					mutations = append(mutations, req.Method)
				}
				switch req.Method {
				case "POST":
					stored = &batchv1.CronJob{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
					stored.UID = "uid"
				case "DELETE":
					stored = nil
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
					return
				}
				if stored == nil {
					apiError(t, w, 404)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			lib := &runtime.Library{Name: "kubernetes-batch-v1",
				Configuration: config.LibraryConfiguration(),
				Resources:     map[string]runtime.ResourceRegistration{"cron-job": cronJobTestRegistration(t)}}
			addNativeCronJobConstraints(t, lib)
			store, err := local.NewStore(t.TempDir(), "cron-job", "test", encrypters.Noop{})
			require.NoError(t, err)
			input := cronJob()
			initial := nativeCronJobExecutor(t, "cron-job-state", lib, store,
				nativeCronJobInputs(t, input, *c.Server))
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
			require.NoError(t, err)
			desired := cronJob()
			desired.Metadata.Name = new("replacement")
			pod := desired.Spec.JobTemplate.Spec.Template.Spec
			pod.Volume =
				new([]Volume{{Name: new("scratch"), EmptyDir: &EmptyDirVolume{SizeLimit: new("1Gi")}}})
			(*pod.Container)[0].Resources = &Resources{Requests: new(map[string]string{"cpu": "1"})}
			pod.SecurityContext = &PodSecurityContext{RunAsUser: new("42")}
			desired.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = new("60")
			exec := nativeCronJobExecutor(t, "cron-job-state", lib, store,
				nativeCronJobInputs(t, desired, *c.Server))
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			file := savedCronJobPlan(t, plan)
			change := func(values map[string]any) {
				node := values["spec"]
				for _, key := range test.path[:len(test.path)-1] {
					if key == "0" {
						node = node.([]any)[0]
					} else {
						node = node.(map[string]any)[key]
					}
				}
				node.(map[string]any)[test.path[len(test.path)-1]] = test.value
			}
			change(exec.Inputs)
			change(file.Inputs)
			for i := range file.Steps {
				step := &file.Steps[i]
				if step.Address == "resource.workload" {
					require.Equal(t, runtime.DecisionReplace, step.Decision)
					change(step.Inputs)
				}
			}
			mutations = nil
			_, err = exec.ApplyPlan(t.Context(), file)
			require.ErrorContains(t, err, test.diagnostic)
			require.Empty(t, mutations)
			require.Equal(t, "example", stored.Name)
			_, err = exec.Plan(t.Context())
			require.ErrorContains(t, err, test.diagnostic)
			require.Empty(t, mutations)
		})
	}
}

func cronJobPlanJobSpec(values map[string]any) map[string]any {
	spec := values["spec"].(map[string]any)
	template := spec["job-template"].(map[string]any)
	return template["spec"].(map[string]any)
}

func TestCronJobMetadataRules(t *testing.T) {
	for _, level := range []string{"outer", "job", "pod"} {
		for _, test := range []struct {
			name  string
			edit  func(*CronJobMetadata)
			valid bool
		}{
			{"label empty", func(m *CronJobMetadata) { m.Labels = new(map[string]string{"key": ""}) }, true},
			{"qualified", func(m *CronJobMetadata) {
				m.Labels = new(map[string]string{"example.com/Key_1": "Value.1"})
			}, true},
			{"label length", func(m *CronJobMetadata) {
				m.Labels = new(map[string]string{"key": strings.Repeat("a", 64)})
			}, false},
			{"key length", func(m *CronJobMetadata) {
				m.Labels = new(map[string]string{strings.Repeat("a", 64): "v"})
			}, false},
			{"prefix case", func(m *CronJobMetadata) {
				m.Labels = new(map[string]string{"Example.com/key": "v"})
			}, false},
			{"annotation case", func(m *CronJobMetadata) {
				m.Annotations = new(map[string]string{"EXAMPLE.COM/Key": "v"})
			}, true},
			{"invalid name", func(m *CronJobMetadata) { m.Name = new("BAD") }, false},
			{"long name", func(m *CronJobMetadata) { m.Name = new(strings.Repeat("a", 254)) }, false},
			{"prefix", func(m *CronJobMetadata) { m.Name = nil; m.GenerateName = new("test-") }, true},
		} {
			t.Run(level+"/"+test.name, func(t *testing.T) {
				r := cronJob()
				target := &r.Metadata
				if level == "job" {
					target = &r.Spec.JobTemplate.Metadata
				}
				if level == "pod" {
					temp := CronJobMetadata(r.Spec.JobTemplate.Metadata)
					test.edit(&temp)
					r.Spec.JobTemplate.Spec.Template.Metadata = TemplateMetadata{
						Name: temp.Name, GenerateName: temp.GenerateName,
						Labels: temp.Labels, Annotations: temp.Annotations,
					}
				} else {
					test.edit(target)
				}
				messages := checkNativeCronJobInputs(t, r)
				if test.valid {
					require.Empty(t, messages)
				} else {
					require.NotEmpty(t, messages)
				}
			})
		}
	}
}

func TestCronJobEmbeddedIdentityRules(t *testing.T) {
	for _, level := range []string{"job", "pod"} {
		for _, state := range []string{"empty", "name", "prefix", "both"} {
			t.Run(level+"/"+state, func(t *testing.T) {
				r := cronJob()
				name, prefix := (*string)(nil), (*string)(nil)
				if state == "name" || state == "both" {
					name = new("template-name")
				}
				if state == "prefix" || state == "both" {
					prefix = new("template-prefix-")
				}
				if level == "job" {
					r.Spec.JobTemplate.Metadata.Name, r.Spec.JobTemplate.Metadata.GenerateName = name, prefix
				} else {
					metadata := &r.Spec.JobTemplate.Spec.Template.Metadata
					metadata.Name, metadata.GenerateName = name, prefix
				}
				diagnostics := checkNativeCronJobInputs(t, r)
				if state == "both" {
					require.NotEmpty(t, diagnostics)
					require.Contains(t, strings.Join(diagnostics, "\n"), "at-most-one-of")
					return
				}
				require.Empty(t, diagnostics)
				obj, err := r.prepare(nil)
				require.NoError(t, err)
				actualName, actualPrefix := obj.Spec.JobTemplate.Name, obj.Spec.JobTemplate.GenerateName
				if level == "pod" {
					actualName = obj.Spec.JobTemplate.Spec.Template.Name
					actualPrefix = obj.Spec.JobTemplate.Spec.Template.GenerateName
				}
				require.Equal(t, textDefault(name, ""), actualName)
				require.Equal(t, textDefault(prefix, ""), actualPrefix)
				baseline, err := r.output(obj, "server", r.Metadata)
				require.NoError(t, err)
				prior := cronJobPrior{Inputs: *r, Outputs: baseline}
				desired := *r
				if level == "job" {
					desired.Spec.JobTemplate.Metadata.Name = nil
					desired.Spec.JobTemplate.Metadata.GenerateName = new("changed-prefix-")
				} else {
					metadata := &desired.Spec.JobTemplate.Spec.Template.Metadata
					metadata.Name, metadata.GenerateName = nil, new("changed-prefix-")
				}
				require.Empty(t, checkNativeCronJobInputs(t, &desired))
				_, changed := desired.immutableChanges(prior)
				require.True(t, changed)
			})
		}
	}
}

func TestCronJobEmbeddedIdentityPlanAndSavedApply(t *testing.T) {
	for _, level := range []string{"job", "pod"} {
		t.Run(level, func(t *testing.T) {
			var stored *batchv1.CronJob
			var mutations []string
			c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					mutations = append(mutations, req.Method)
				}
				if req.Method == http.MethodPost {
					stored = &batchv1.CronJob{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
					stored.UID = "uid"
				}
				if stored == nil {
					apiError(t, w, 404)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			lib := &runtime.Library{
				Name: "kubernetes-batch-v1", Configuration: config.LibraryConfiguration(),
				Resources: map[string]runtime.ResourceRegistration{"cron-job": cronJobTestRegistration(t)},
			}
			addNativeCronJobConstraints(t, lib)
			store, err := local.NewStore(t.TempDir(), "cron-job", "test", encrypters.Noop{})
			require.NoError(t, err)
			input := cronJob()
			if level == "job" {
				input.Spec.JobTemplate.Metadata.Name = new("template-name")
			} else {
				input.Spec.JobTemplate.Spec.Template.Metadata.Name = new("template-name")
			}
			executor := func() *runtime.Executor {
				return nativeCronJobExecutor(t, "cron-job-state", lib, store,
					nativeCronJobInputs(t, input, *c.Server))
			}
			exec := executor()
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), savedCronJobPlan(t, plan))
			require.NoError(t, err)
			mutations = nil
			embedded := func(values map[string]any) map[string]any {
				template := values["spec"].(map[string]any)["job-template"].(map[string]any)
				if level == "pod" {
					template = template["spec"].(map[string]any)["template"].(map[string]any)
				}
				return template["metadata"].(map[string]any)
			}
			exec = executor()
			embedded(exec.Inputs)["generate-name"] = "template-prefix-"
			_, err = exec.Plan(t.Context())
			require.ErrorContains(t, err, "resource.workload: schema: constraints[")
			require.ErrorContains(t, err, "at-most-one-of")
			require.Empty(t, mutations)
			exec = executor()
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			file := savedCronJobPlan(t, plan)
			embedded(exec.Inputs)["generate-name"] = "template-prefix-"
			embedded(file.Inputs)["generate-name"] = "template-prefix-"
			for i := range file.Steps {
				step := &file.Steps[i]
				if step.Address == "resource.workload" {
					require.Equal(t, runtime.DecisionNoOp, step.Decision)
					embedded(step.Inputs)["generate-name"] = "template-prefix-"
				}
			}
			_, err = exec.ApplyPlan(t.Context(), file)
			require.ErrorContains(t, err, "resource.workload: schema: constraints[")
			require.ErrorContains(t, err, "at-most-one-of")
			require.Empty(t, mutations)
			require.Equal(t, "uid", string(stored.UID))
			require.Empty(t, stored.Spec.JobTemplate.GenerateName)
			require.Empty(t, stored.Spec.JobTemplate.Spec.Template.GenerateName)
		})
	}
}
