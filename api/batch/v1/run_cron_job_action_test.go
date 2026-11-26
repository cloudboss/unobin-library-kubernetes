package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
)

var runCronJobSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		return nil, err
	}
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	if err != nil {
		return nil, err
	}
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("extract batch schema: %v", warnings)
	}
	return schema, nil
})

func runCronJobLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := runCronJobSchema()
	require.NoError(t, err)
	lib := Library()
	lib.Schema = schema
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"action.run-cron-job": schema.Actions["run-cron-job"].Constraints,
	}
	lib.Defaults = map[string][]lang.DefaultSpec{
		"action.run-cron-job": schema.Actions["run-cron-job"].Defaults,
	}
	return lib
}

func runCronJobFixture(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	data, err := os.ReadFile("testdata/ub/run-cron-job/" + fixture + "/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", data)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func TestRunCronJobPublicCompileChecks(t *testing.T) {
	lib := runCronJobLibrary(t)
	for _, name := range []string{"valid", "invalid-source", "invalid-job", "invalid-namespace",
		"invalid-uid", "invalid-timeout", "invalid-type", "invalid-required", "invalid-excluded"} {
		t.Run(name, func(t *testing.T) {
			checker := check.NewSyntax(runCronJobFixture(t, name),
				map[string]*runtime.Library{"batch": lib}, nil, "")
			err := checker.References(nil).Err()
			if err == nil {
				err = checker.LiteralConstraints().Err()
			}
			if name == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				switch name {
				case "invalid-type":
					require.ErrorContains(t, err, "expected optional(boolean), got string")
				case "invalid-required":
					require.ErrorContains(t, err, `missing required input "job-name"`)
				case "invalid-excluded":
					require.ErrorContains(t, err, `unknown field "schedule"`)
				default:
					require.ErrorContains(t, err, "constraints[")
				}
			}
		})
	}
}

func runCronJobExecutor(
	t *testing.T, lib *runtime.Library, store *local.Store, fixture string, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body := runCronJobFixture(t, fixture)
	libs := map[string]*runtime.Library{"batch": lib}
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "run-cron-job", Version: "v0", ContentRevision: "c0"}}
}

func runCronJobPlanFile(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	data, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(data)
	require.NoError(t, err)
	return file
}

type runCronJobServer struct {
	server *httptest.Server
	calls  []string
	jobs   map[string]*batchv1.Job
}

func runCronJobTestServer(t *testing.T) *runCronJobServer {
	t.Helper()
	s := &runCronJobServer{jobs: map[string]*batchv1.Job{}}
	s.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.calls = append(s.calls, req.Method+" "+req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "/cronjobs/") {
			require.NoError(t, json.NewEncoder(w).Encode(&batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "default", UID: "cron-uid"},
				Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{
					Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "busybox:1.37"}},
					}}},
				}},
			}))
			return
		}
		name := filepath.Base(req.URL.Path)
		if req.Method == http.MethodPost {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			job := &batchv1.Job{}
			_, _, err = scheme.Codecs.UniversalDeserializer().Decode(body, nil, job)
			require.NoError(t, err)
			job.UID = "uid-" + types.UID(job.Name)
			s.jobs[job.Name] = job
			name = job.Name
		}
		if job := s.jobs[name]; job != nil {
			require.NoError(t, json.NewEncoder(w).Encode(job))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
			Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404,
		}))
	}))
	t.Cleanup(s.server.Close)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	return s
}

func runCronJobValues(server string) map[string]any {
	return map[string]any{"server": server, "cron-job-name": "source", "job-name": "run",
		"namespace": "default", "expected-cron-job-uid": nil, "wait-for-completion": false,
		"timeout": nil, "trigger": "stable"}
}

func TestRunCronJobNativePlanSkipSavedApplyAndDestroy(t *testing.T) {
	lib := runCronJobLibrary(t)
	s := runCronJobTestServer(t)
	store, err := local.NewStore(t.TempDir(), "run-cron-job", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := runCronJobValues(s.server.URL)
	executor := func() *runtime.Executor {
		return runCronJobExecutor(t, lib, store, "inputs", inputs)
	}
	exec := executor()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	require.Empty(t, s.calls)
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET /apis/batch/v1/namespaces/default/cronjobs/source",
		"GET /apis/batch/v1/namespaces/default/jobs/run",
		"POST /apis/batch/v1/namespaces/default/jobs",
	}, s.calls)
	s.calls = nil
	exec = executor()
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range plan.Steps {
		if step.Address == "action.run" {
			require.Equal(t, runtime.DecisionSkip, step.Decision)
		}
	}
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
	require.Empty(t, s.calls)
	inputs["trigger"] = "observe"
	exec = executor()
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET /apis/batch/v1/namespaces/default/cronjobs/source",
		"GET /apis/batch/v1/namespaces/default/jobs/run",
	}, s.calls)
	s.calls = nil
	inputs["job-name"] = "another"
	inputs["trigger"] = "another"
	exec = executor()
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET /apis/batch/v1/namespaces/default/cronjobs/source",
		"GET /apis/batch/v1/namespaces/default/jobs/another",
		"POST /apis/batch/v1/namespaces/default/jobs",
	}, s.calls)
	s.calls = nil
	snapshot, err := store.Current()
	require.NoError(t, err)
	for _, entry := range snapshot.Entries {
		if entry.Address == "action.run" {
			entry.Inputs["job-name"] = "BAD historical name"
		}
	}
	revision, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(revision))
	inputs["job-name"] = "BAD historical name"
	exec = executor()
	exec.Destroy = true
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
	require.Empty(t, s.calls)
	require.Equal(t, "uid-run", string(s.jobs["run"].UID))
	require.Equal(t, "uid-another", string(s.jobs["another"].UID))
	snapshot, err = store.Current()
	require.NoError(t, err)
	for _, entry := range snapshot.Entries {
		require.NotEqual(t, "action.run", entry.Address)
	}
}

func TestRunCronJobNativeInvalidInputs(t *testing.T) {
	lib := runCronJobLibrary(t)
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"source syntax", "cron-job-name", "BAD"},
		{"source bound", "cron-job-name", strings.Repeat("a", 254)},
		{"source empty", "cron-job-name", ""},
		{"job syntax", "job-name", "a..b"},
		{"job empty", "job-name", ""},
		{"job bound", "job-name", strings.Repeat("a", 64)},
		{"namespace syntax", "namespace", "a.b"},
		{"namespace empty", "namespace", ""},
		{"namespace bound", "namespace", strings.Repeat("a", 64)},
		{"UID empty", "expected-cron-job-uid", ""},
		{"timeout zero", "timeout", "0s"},
		{"timeout negative", "timeout", "-1s"},
		{"timeout syntax", "timeout", "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := runCronJobTestServer(t)
			store, err := local.NewStore(t.TempDir(), "run-cron-job", "test", encrypters.Noop{})
			require.NoError(t, err)
			inputs := runCronJobValues(s.server.URL)
			exec := runCronJobExecutor(t, lib, store, "inputs", inputs)
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, s.calls)
			_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
			require.NoError(t, err)
			s.calls = nil
			inputs[tc.field] = tc.value
			exec = runCronJobExecutor(t, lib, store, "inputs", inputs)
			_, err = exec.Plan(t.Context())
			require.ErrorContains(t, err, "action.run: schema: constraints[")
			require.Empty(t, s.calls)
			inputs[tc.field] = runCronJobValues(s.server.URL)[tc.field]
			exec = runCronJobExecutor(t, lib, store, "inputs", inputs)
			plan, err = exec.Plan(t.Context())
			require.NoError(t, err)
			file := runCronJobPlanFile(t, plan)
			exec.Inputs[tc.field] = tc.value
			file.Inputs[tc.field] = tc.value
			found := false
			for i := range file.Steps {
				step := &file.Steps[i]
				if step.Address == "action.run" {
					require.Equal(t, runtime.DecisionSkip, step.Decision)
					step.Inputs[tc.field] = tc.value
					found = true
				}
			}
			require.True(t, found)
			_, err = exec.ApplyPlan(t.Context(), file)
			require.ErrorContains(t, err, "action.run: schema: constraints[")
			require.Empty(t, s.calls)
		})
	}
}

type runCronJobSeed struct{ Values map[string]any }
type runCronJobSeedOutput struct{ Resolved map[string]any }

func (s *runCronJobSeed) Run(context.Context, any) (*runCronJobSeedOutput, error) {
	return &runCronJobSeedOutput{Resolved: s.Values}, nil
}

func TestRunCronJobDeferredNativeInputs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value any
		valid bool
	}{
		{"valid", "job-name", "deferred", true},
		{"source", "cron-job-name", "BAD", false},
		{"job", "job-name", "BAD", false},
		{"namespace", "namespace", "BAD", false},
		{"UID", "expected-cron-job-uid", "", false},
		{"timeout", "timeout", "0s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lib := runCronJobLibrary(t)
			lib.Actions["seed"] = runtime.MakeAction[
				runCronJobSeed, *runCronJobSeedOutput, any,
			]()
			s := runCronJobTestServer(t)
			store, err := local.NewStore(t.TempDir(), "run-cron-job", "test", encrypters.Noop{})
			require.NoError(t, err)
			values := runCronJobValues(s.server.URL)
			values[tc.field] = tc.value
			exec := runCronJobExecutor(t, lib, store, "deferred", map[string]any{
				"server": s.server.URL, "values": values,
			})
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, s.calls)
			pending := false
			for _, step := range plan.Steps {
				if step.Address == "action.run" {
					pending = len(step.UnresolvedInputs) != 0
				}
			}
			require.True(t, pending)
			_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, []string{
					"GET /apis/batch/v1/namespaces/default/cronjobs/source",
					"GET /apis/batch/v1/namespaces/default/jobs/deferred",
					"POST /apis/batch/v1/namespaces/default/jobs",
				}, s.calls)
			} else {
				require.ErrorContains(t, err, "action.run: schema: constraints[")
				require.Empty(t, s.calls)
			}
		})
	}
}

func TestRunCronJobPublicDefaultsAndBounds(t *testing.T) {
	lib := runCronJobLibrary(t)
	s := runCronJobTestServer(t)
	store, err := local.NewStore(t.TempDir(), "run-cron-job", "test", encrypters.Noop{})
	require.NoError(t, err)
	inputs := runCronJobValues(s.server.URL)
	inputs["wait-for-completion"] = nil
	inputs["namespace"] = nil
	inputs["cron-job-name"] = strings.Repeat("a", 253)
	inputs["job-name"] = strings.Repeat("a", 63)
	exec := runCronJobExecutor(t, lib, store, "defaults", inputs)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	found := false
	for _, step := range plan.Steps {
		if step.Address == "action.run" {
			require.Equal(t, "10m", step.Inputs["timeout"])
			require.Equal(t, true, step.Inputs["wait-for-completion"])
			found = true
		}
	}
	require.True(t, found)
	require.Empty(t, s.calls)
}
