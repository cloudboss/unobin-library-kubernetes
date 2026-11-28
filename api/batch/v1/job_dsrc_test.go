package v1

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func jobLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	lib := runCronJobLibrary(t)
	schema := lib.Schema.DataSources["job"]
	schema.EvaluatedInputs = schema.Inputs
	lib.Constraints = map[string][]lang.ConstraintSpec{"data-source.job": schema.Constraints}
	lib.Defaults = map[string][]lang.DefaultSpec{"data-source.job": schema.Defaults}
	lib.Resources["anchor"] = runtime.MakeResource[
		jobLookupAnchor, *jobLookupAnchorOutput, *config.Configuration,
	](runtime.ResourceDefinition[jobLookupAnchor, *jobLookupAnchorOutput, *config.Configuration]{
		SchemaVersion: 1,
	})
	lib.Actions["seed"] = runtime.MakeAction[runCronJobSeed, *runCronJobSeedOutput, any]()
	return lib
}

func jobLookupFixture(t *testing.T, name string) syntax.FactoryBody {
	t.Helper()
	file := "testdata/ub/job-lookup/" + name + "/factory.ub"
	source, err := os.ReadFile(file)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(file, source)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func TestJobLookupPublicCompile(t *testing.T) {
	lib := jobLookupLibrary(t)
	for _, name := range []string{"valid", "boundary", "null-namespace", "missing-metadata",
		"null-metadata", "wrong-metadata", "missing-name", "null-name", "wrong-name", "empty-name",
		"upper-name", "slash-name", "long-name", "empty-namespace", "dotted-namespace",
		"upper-namespace", "long-namespace", "labels", "annotations", "generate-name", "spec"} {
		t.Run(name, func(t *testing.T) {
			checker := check.NewSyntax(jobLookupFixture(t, name),
				map[string]*runtime.Library{"batch": lib}, nil, "")
			err := checker.References(nil).Err()
			if err == nil {
				err = checker.LiteralConstraints().Err()
			}
			switch name {
			case "valid", "boundary", "null-namespace":
				require.NoError(t, err)
			case "missing-metadata":
				require.ErrorContains(t, err, "missing required input")
			case "missing-name":
				require.ErrorContains(t, err, "missing required field")
			case "null-metadata", "wrong-metadata", "null-name", "wrong-name":
				require.ErrorContains(t, err, "type mismatch")
			case "labels", "annotations", "generate-name", "spec":
				require.ErrorContains(t, err, "unknown field")
			default:
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestJobLookupAPIGate(t *testing.T) {
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

func jobLookupExecutor(t *testing.T, lib *runtime.Library, store *local.Store,
	fixture string, inputs map[string]any) *runtime.Executor {
	t.Helper()
	body := jobLookupFixture(t, fixture)
	libs := map[string]*runtime.Library{"batch": lib,
		"seed": {Name: "seed", Actions: map[string]runtime.ActionRegistration{
			"seed": runtime.MakeAction[runCronJobSeed, *runCronJobSeedOutput, any](),
		}},
	}
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "job-lookup", Version: "v0", ContentRevision: "c0"}}
}

func jobLookupStore(t *testing.T) *local.Store {
	t.Helper()
	store, err := local.NewStore(t.TempDir(), "job-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	return store
}

func jobLookupInputs(server string, metadata any) map[string]any {
	return map[string]any{"server": server, "metadata": metadata}
}

func jobLookupApply(t *testing.T, exec *runtime.Executor) {
	t.Helper()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
}

const jobLookupCall = "GET /apis/batch/v1/namespaces/default/jobs/example"

func TestJobLookupNativeSteadyAndInvalid(t *testing.T) {
	lib := jobLookupLibrary(t)
	s := runCronJobTestServer(t)
	s.jobs["example"] = &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
	}
	store := jobLookupStore(t)
	inputs := jobLookupInputs(s.server.URL, map[string]any{"name": "example"})
	executor := func() *runtime.Executor { return jobLookupExecutor(t, lib, store, "steady", inputs) }
	jobLookupApply(t, executor())
	s.calls = nil
	exec := executor()
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	decisions := map[string]runtime.Decision{}
	for _, step := range plan.Steps {
		if step.Kind == runtime.NodeResource || step.Kind == runtime.NodeDataSource {
			decisions[step.Address] = step.Decision
		}
	}
	require.Equal(t, map[string]runtime.Decision{"resource.anchor": runtime.DecisionNoOp,
		"data-source.lookup": runtime.DecisionRead}, decisions)
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{jobLookupCall, jobLookupCall}, s.calls)
	for _, tc := range []struct {
		name       string
		metadata   any
		diagnostic string
	}{
		{"missing metadata", nil, "required but is null"},
		{"wrong metadata", "bad", "expected object"},
		{"missing name", map[string]any{}, "required but not provided"},
		{"null name", map[string]any{"name": nil}, "required but is null"},
		{"wrong name", map[string]any{"name": 1}, "expected string"},
		{"empty name", map[string]any{"name": ""}, "constraints[0]"},
		{"upper name", map[string]any{"name": "BAD"}, "constraints[0]"},
		{"slash name", map[string]any{"name": "a/b"}, "constraints[0]"},
		{"long name", map[string]any{"name": strings.Repeat("a", 254)}, "constraints[0]"},
		{"empty namespace", map[string]any{"name": "example", "namespace": ""}, "constraints[1]"},
		{"dotted namespace", map[string]any{"name": "example", "namespace": "a.b"}, "constraints[1]"},
		{"upper namespace", map[string]any{"name": "example", "namespace": "BAD"}, "constraints[1]"},
		{"long namespace", map[string]any{"name": "example", "namespace": strings.Repeat("a", 64)},
			"constraints[1]"},
		{"unknown field", map[string]any{"name": "example", "labels": map[string]any{}}, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.calls = nil
			inputs["metadata"] = tc.metadata
			_, err := executor().Plan(t.Context())
			require.ErrorContains(t, err, "data-source.lookup:")
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, s.calls)
		})
	}
	for _, metadata := range []any{map[string]any{"name": "a.b"},
		map[string]any{"name": strings.Repeat("a", 253), "namespace": strings.Repeat("n", 63)},
		map[string]any{"name": "example", "namespace": nil}} {
		inputs["metadata"] = metadata
		jobLookupApply(t, executor())
	}
}

func TestJobLookupDeferredNative(t *testing.T) {
	for _, tc := range []struct {
		name, fixture string
		metadata      any
		values        map[string]any
		fail          string
	}{
		{name: "valid", fixture: "deferred",
			values: map[string]any{"metadata": map[string]any{"name": "example"}}},
		{name: "null namespace", fixture: "deferred", values: map[string]any{
			"metadata": map[string]any{"name": "example", "namespace": nil}}},
		{name: "invalid", fixture: "deferred", values: map[string]any{
			"metadata": map[string]any{"name": "BAD"}}, fail: "constraints[0]"},
		{name: "null name", fixture: "deferred", values: map[string]any{
			"metadata": map[string]any{"name": nil}}, fail: "required but is null"},
		{name: "null metadata", fixture: "deferred", values: map[string]any{
			"metadata": nil}, fail: "required but is null"},
		{name: "configuration", fixture: "pending-configuration",
			metadata: map[string]any{"name": "example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lib := jobLookupLibrary(t)
			s := runCronJobTestServer(t)
			values := tc.values
			if values == nil {
				values = map[string]any{"server": s.server.URL}
			}
			inputs := jobLookupInputs(s.server.URL, tc.metadata)
			inputs["values"] = values
			exec := jobLookupExecutor(t, lib, jobLookupStore(t), tc.fixture, inputs)
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, s.calls)
			pending := false
			for _, step := range plan.Steps {
				if step.Address == "data-source.lookup" {
					pending = len(step.UnresolvedInputs) != 0 || step.DeferredConfig != ""
				}
			}
			require.True(t, pending)
			_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
			if tc.fail != "" {
				require.ErrorContains(t, err, "data-source.lookup:")
				require.ErrorContains(t, err, tc.fail)
				require.Empty(t, s.calls)
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{jobLookupCall}, s.calls)
			}
		})
	}
	t.Run("known invalid before pending namespace", func(t *testing.T) {
		s := runCronJobTestServer(t)
		exec := jobLookupExecutor(t, jobLookupLibrary(t), jobLookupStore(t), "pending-namespace",
			map[string]any{"server": s.server.URL, "name": "BAD",
				"values": map[string]any{"namespace": "default"}})
		_, err := exec.Plan(t.Context())
		require.ErrorContains(t, err, "data-source.lookup: schema: constraints[0]")
		require.Empty(t, s.calls)
	})
}

func TestJobLookupSavedRulesAndHistoricalDestroy(t *testing.T) {
	lib := jobLookupLibrary(t)
	rules := lib.Constraints
	lib.Constraints = nil
	s := runCronJobTestServer(t)
	store := jobLookupStore(t)
	inputs := jobLookupInputs(s.server.URL, map[string]any{"name": "BAD"})
	exec := jobLookupExecutor(t, lib, store, "steady", inputs)
	jobLookupApply(t, exec)
	plan, err := exec.Plan(t.Context())
	require.NoError(t, err)
	file := runCronJobPlanFile(t, plan)
	s.calls = nil
	lib.Constraints = rules
	_, err = exec.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err, "data-source.lookup: schema: constraints[0]")
	require.Empty(t, s.calls)
	exec.Destroy = true
	plan, err = exec.Plan(t.Context())
	require.NoError(t, err)
	_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
	require.NoError(t, err)
	require.Empty(t, s.calls)
	snapshot, err := store.Current()
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
}

func TestJobLookupConsistencyAndRemoval(t *testing.T) {
	for _, change := range []string{"unchanged", "version", "UID", "metadata", "spec", "status",
		"disappear", "appear"} {
		t.Run(change, func(t *testing.T) {
			s := runCronJobTestServer(t)
			obj := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", UID: "old"},
			}
			if change != "appear" {
				s.jobs["example"] = obj
			}
			lib, store := jobLookupLibrary(t), jobLookupStore(t)
			inputs := jobLookupInputs(s.server.URL, map[string]any{"name": "example"})
			exec := jobLookupExecutor(t, lib, store, "steady", inputs)
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			switch change {
			case "version":
				obj.ResourceVersion = "next"
			case "UID":
				obj.UID = "replacement"
			case "metadata":
				obj.Labels = map[string]string{"new": "value"}
			case "spec":
				obj.Spec.Parallelism = new(int32(0))
			case "status":
				obj.Status.Ready = new(int32(0))
			case "disappear":
				delete(s.jobs, "example")
			case "appear":
				s.jobs["example"] = obj
			}
			_, err = exec.ApplyPlan(t.Context(), runCronJobPlanFile(t, plan))
			if change == "unchanged" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "data-source.lookup changed since the plan was computed")
			}
			require.Equal(t, []string{jobLookupCall, jobLookupCall}, s.calls)
			exec = jobLookupExecutor(t, lib, store, "steady", inputs)
			jobLookupApply(t, exec)
			snapshot, err := store.Current()
			require.NoError(t, err)
			entry := snapshot.Find("data-source.lookup")
			require.NotNil(t, entry)
			metadata := entry.Outputs["metadata"].(map[string]any)
			if change == "UID" {
				require.Equal(t, "replacement", metadata["uid"])
			}
			s.calls = nil
			exec = jobLookupExecutor(t, lib, store, "removed", inputs)
			jobLookupApply(t, exec)
			require.Empty(t, s.calls)
			require.Equal(t, change != "disappear", s.jobs["example"] != nil)
			snapshot, err = store.Current()
			require.NoError(t, err)
			require.Nil(t, snapshot.Find("data-source.lookup"))
		})
	}
}

type jobLookupAnchor struct{ Value string }
type jobLookupAnchorOutput struct{ Value string }
type jobLookupAnchorPrior = runtime.Prior[
	jobLookupAnchor, *jobLookupAnchorOutput, *config.Configuration,
]

func (r *jobLookupAnchor) Create(
	context.Context, *config.Configuration,
) (*jobLookupAnchorOutput, error) {
	return &jobLookupAnchorOutput{Value: r.Value}, nil
}
func (r *jobLookupAnchor) Read(_ context.Context, _ *config.Configuration,
	p jobLookupAnchorPrior) (*jobLookupAnchorOutput, error) {
	return p.Outputs, nil
}
func (r *jobLookupAnchor) Update(context.Context, *config.Configuration,
	jobLookupAnchorPrior) (*jobLookupAnchorOutput, error) {
	return &jobLookupAnchorOutput{Value: r.Value}, nil
}
func (r *jobLookupAnchor) Delete(
	context.Context, *config.Configuration, jobLookupAnchorPrior,
) error {
	return nil
}
