package v1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

var nativeJobSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/batch/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

var nativeJobRules = sync.OnceValues(func() ([]lang.ConstraintEntry, error) {
	schema, err := nativeJobSchema()
	if err != nil {
		return nil, err
	}
	entries, errors := lang.ParseSpecs(schema.Resources["job"].Constraints)
	return entries, errors.Err()
})

func checkNativeJobInputs(t *testing.T, input any) []string {
	t.Helper()
	entries, err := nativeJobRules()
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

func nativeJobExecutor(
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

func nativeJobInputs(t *testing.T, input *JobResource, server string) map[string]any {
	t.Helper()
	data, err := ub.Marshal(input)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, ub.Unmarshal(data, &values))
	values["server"] = server
	values["token"] = "first"
	return values
}

func savedJobPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	data, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(data)
	require.NoError(t, err)
	return file
}

func TestNativeJobPlanAndSavedApply(t *testing.T) {
	mutations := 0
	var stored *batchv1.Job
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			mutations++
		}
		switch req.Method {
		case http.MethodPost:
			stored = &batchv1.Job{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.Name, stored.UID = "example", "uid"
		case http.MethodPatch:
			patchJob(t, req, stored)
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
		Resources: map[string]runtime.ResourceRegistration{"job": testJobRegistration(t)},
	}
	addNativeJobConstraints(t, lib)
	store, err := local.NewStore(t.TempDir(), "job", "test", encrypters.Noop{})
	require.NoError(t, err)
	executor := func(input *JobResource) *runtime.Executor {
		return nativeJobExecutor(t, "state", lib, store, nativeJobInputs(t, input, *c.Server))
	}
	input := job()
	exec := executor(input)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), savedJobPlan(t, plan))
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
	_, err = exec.ApplyPlan(t.Context(), savedJobPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, before, mutations)
	t.Run("otherwise no-op", func(t *testing.T) {
		invalid := job()
		invalid.Metadata.Name = nil
		_, err := executor(invalid).Plan(t.Context())
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		require.Equal(t, before, mutations)
	})
	t.Run("saved no-op", func(t *testing.T) {
		exec := executor(job())
		plan, err := exec.Plan(t.Context())
		require.NoError(t, err)
		file := savedJobPlan(t, plan)
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
		desired := job()
		desired.Metadata.Name = new("replacement")
		desired.Spec.TTLSecondsAfterFinished = new("2147483648")
		exec := executor(desired)
		plan, err := exec.Plan(t.Context())
		if err == nil {
			_, err = exec.ApplyPlan(t.Context(), savedJobPlan(t, plan))
		}
		require.ErrorContains(t, err, "TTL")
		require.Equal(t, before, mutations)
	})
	for _, value := range []string{"2147483648", "malformed"} {
		t.Run("saved replacement "+value, func(t *testing.T) {
			desired := job()
			desired.Metadata.Name = new("replacement")
			exec := executor(desired)
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			file := savedJobPlan(t, plan)
			exec.Inputs["spec"].(map[string]any)["ttl-seconds-after-finished"] = value
			file.Inputs["spec"].(map[string]any)["ttl-seconds-after-finished"] = value
			for i := range file.Steps {
				if step := &file.Steps[i]; step.Address == "resource.workload" {
					require.Equal(t, runtime.DecisionReplace, step.Decision)
					step.Inputs["spec"].(map[string]any)["ttl-seconds-after-finished"] = value
				}
			}
			_, err = exec.ApplyPlan(t.Context(), file)
			require.ErrorContains(t, err, "TTL")
			require.Equal(t, before, mutations)
		})
	}
	t.Run("destroy historical identity", func(t *testing.T) {
		lib.Constraints = nil
		historical := job()
		historical.Metadata.Name = nil
		exec := executor(historical)
		plan, err := exec.Plan(t.Context())
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), savedJobPlan(t, plan))
		require.NoError(t, err)
		addNativeJobConstraints(t, lib)
		_, err = exec.Plan(t.Context())
		require.ErrorContains(t, err, "resource.workload: schema: constraints[")
		exec.Destroy = true
		plan, err = exec.Plan(t.Context())
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), savedJobPlan(t, plan))
		require.NoError(t, err)
		require.Nil(t, stored)
	})
}

func TestNativeJobDeferredInputs(t *testing.T) {
	for _, test := range []struct {
		fixture string
		value   string
		replace bool
		valid   bool
	}{
		{"deferred-native", "deferred", false, true},
		{"deferred-native", "BAD", false, false},
		{"deferred-native", "BAD", true, false},
		{"deferred-quantity", "1Gi", false, true},
		{"deferred-quantity", "malformed", false, false},
		{"deferred-quantity", "malformed", true, false},
		{"deferred-ttl", "60", false, true},
		{"deferred-ttl", "2147483648", false, false},
		{"deferred-ttl", "2147483648", true, false},
	} {
		t.Run(fmt.Sprintf("%s/%s/replace=%t", test.fixture, test.value, test.replace),
			func(t *testing.T) {
				stored := map[string]*batchv1.Job{}
				var mutations []string
				c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method == http.MethodPost {
						obj := &batchv1.Job{}
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
					name := req.URL.Path[len("/apis/batch/v1/namespaces/default/jobs/"):]
					if obj := stored[name]; obj != nil {
						require.NoError(t, json.NewEncoder(w).Encode(obj))
					} else {
						apiError(t, w, 404)
					}
				})
				lib := &runtime.Library{
					Name: "kubernetes-batch-v1", Configuration: config.LibraryConfiguration(),
					Resources: map[string]runtime.ResourceRegistration{"job": testJobRegistration(t)},
				}
				addNativeJobConstraints(t, lib)
				store, err := local.NewStore(t.TempDir(), "job", "test", encrypters.Noop{})
				require.NoError(t, err)
				if test.replace {
					exec := nativeJobExecutor(t, "state", lib, store,
						nativeJobInputs(t, job(), *c.Server))
					plan, err := exec.Plan(t.Context())
					require.NoError(t, err)
					_, err = exec.ApplyPlan(t.Context(), savedJobPlan(t, plan))
					require.NoError(t, err)
					mutations = nil
				}
				exec := nativeJobExecutor(t, test.fixture, lib, store,
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
				_, err = exec.ApplyPlan(t.Context(), savedJobPlan(t, plan))
				if test.valid {
					require.NoError(t, err)
					name := "replacement"
					if test.fixture == "deferred-native" {
						name = test.value
					}
					require.Equal(t, []string{"POST seed", "POST " + name}, mutations)
				} else {
					require.Error(t, err)
					switch test.fixture {
					case "deferred-native":
						require.ErrorContains(t, err, "resource.workload: schema: constraints[")
					case "deferred-ttl":
						require.ErrorContains(t, err, "TTL")
					default:
						require.ErrorContains(t, err, "sizeLimit")
					}
					require.Equal(t, []string{"POST seed"}, mutations)
				}
			})
	}
}

func addNativeJobConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativeJobSchema()
	require.NoError(t, err)
	library.Constraints = map[string][]lang.ConstraintSpec{
		"resource.job": schema.Resources["job"].Constraints,
	}
}

func TestNativeJobInputs(t *testing.T) {
	require.Empty(t, checkNativeJobInputs(t, job()))
	require.Empty(t, checkNativeJobInputs(t, fullInput(t)))
	for _, test := range []struct {
		name string
		set  func(*JobResource)
	}{
		{"missing identity", func(r *JobResource) { r.Metadata.Name = nil }},
		{"both identities", func(r *JobResource) { r.Metadata.GenerateName = new("prefix-") }},
		{"empty name", func(r *JobResource) { r.Metadata.Name = new("") }},
		{"invalid name", func(r *JobResource) { r.Metadata.Name = new("BAD") }},
		{"label", func(r *JobResource) {
			r.Metadata.Labels = new(map[string]string{"bad key": "value"})
		}},
		{"completion mode", func(r *JobResource) { r.Spec.CompletionMode = new("bad") }},
		{"parallelism", func(r *JobResource) { r.Spec.Parallelism = new(int64(-1)) }},
		{"deadline", func(r *JobResource) { r.Spec.ActiveDeadlineSeconds = new(int64(0)) }},
		{"timeout", func(r *JobResource) { r.CreateTimeout = new("0s") }},
		{"regex", func(r *JobResource) { r.IgnoreLabels = new([]string{"["}) }},
		{"exit zero", func(r *JobResource) {
			r.Spec.PodFailurePolicy = &PodFailurePolicy{Rule: []PodFailureRule{{
				OnExitCodes: &ExitCodes{Values: []int64{0}},
			}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := job()
			test.set(r)
			require.NotEmpty(t, checkNativeJobInputs(t, r))
		})
	}
	for _, policy := range []string{"Always", "OnFailure", "Never"} {
		r := job()
		r.Spec.Template.Spec.RestartPolicy = &policy
		require.Empty(t, checkNativeJobInputs(t, r))
	}
	r := job()
	r.Metadata.Name, r.Metadata.GenerateName = nil, new("generated-")
	r.Spec.Template.Spec = nil
	require.Empty(t, checkNativeJobInputs(t, r))
}
