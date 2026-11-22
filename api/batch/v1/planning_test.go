package v1

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/batch/v1"
)

type publicRegistration[P any] struct {
	runtime.ResourceRegistration
	t        *testing.T
	output   *svc.JobResourceOutput
	calls    *atomic.Int64
	ordinary func(any, any, P) ([]string, error)
	known    func(any, any, P, map[string][]string, bool) ([]string, error)
}

func checkPublicRegistration[P any](
	t *testing.T, base runtime.ResourceRegistration, output *svc.JobResourceOutput,
	calls *atomic.Int64, ordinary func(any, any, P) ([]string, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) publicRegistration[P] {
	return publicRegistration[P]{base, t, output, calls, ordinary, known}
}

func (r publicRegistration[P]) Create(context.Context, any, any) (any, error) {
	return r.output, nil
}

func (r publicRegistration[P]) Read(context.Context, any, any, P) (any, error) {
	return r.output, nil
}

func (r publicRegistration[P]) Update(context.Context, any, any, P) (any, error) {
	return nil, fmt.Errorf("equal computed values must not update or wait")
}

func (r publicRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if len(pending) == 0 && !configPending {
		ordinary, ordinaryErr := r.ordinary(receiver, cfg, p)
		require.Equal(r.t, err, ordinaryErr)
		require.Equal(r.t, reasons, ordinary)
		r.calls.Add(1)
	}
	return reasons, err
}

func TestPublicReplacementPlanning(t *testing.T) {
	source, err := os.ReadFile("testdata/ub/planning/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	store, err := local.NewStore(t.TempDir(), "job", "test", encrypters.Noop{})
	require.NoError(t, err)
	input := svc.JobResource{Metadata: svc.Metadata{Name: new("example")},
		Spec: svc.JobSpec{Template: svc.PodTemplate{
			Metadata: svc.TemplateMetadata{},
			Spec:     &svc.PodSpec{Container: new([]svc.Container{{Name: "app"}})},
		}}, DeleteTimeout: new("2s")}
	output := &svc.JobResourceOutput{
		Server: "https://no-io.invalid", APIVersion: "batch/v1", Kind: "Job",
		Name: "example", Namespace: "default", UID: "uid", Metadata: input.Metadata, Spec: input.Spec,
	}
	output.Spec.CompletionMode = new("NonIndexed")
	var calls atomic.Int64
	server := output.Server
	var wantReasons []string
	plan := func(want runtime.Decision, apply bool) {
		lib := Library()
		base := lib.Resources["job"]
		lib.Resources["job"] = checkPublicRegistration(t, base, output, &calls,
			base.ReplacementReasons, base.KnownReplacementReasons)
		libs := map[string]*runtime.Library{"batch": lib}
		inputs := map[string]any{"server": server, "completion-mode": nil}
		if input.Spec.CompletionMode != nil {
			inputs["completion-mode"] = *input.Spec.CompletionMode
		}
		exec := &runtime.Executor{
			DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs, Store: store,
			Factory: state.FactoryInfo{Name: "job", Version: "v0", ContentRevision: "c0"},
		}
		planned, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range planned.Steps {
			if step.Address == "resource.workload" {
				require.Equal(t, want, step.Decision)
				require.Equal(t, wantReasons, step.ReplacementReasons)
				found = true
			}
		}
		require.True(t, found)
		if apply {
			data, err := runtime.EncodePlan(planned)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(data)
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), file)
			require.NoError(t, err)
		}
	}
	plan(runtime.DecisionCreate, true)
	input.Spec.CompletionMode = new("NonIndexed")
	plan(runtime.DecisionNoOp, true)
	input.Spec.CompletionMode = nil
	plan(runtime.DecisionNoOp, true)
	input.Spec.CompletionMode = new("NonIndexed")
	plan(runtime.DecisionNoOp, true)
	input.Spec.CompletionMode = new("Indexed")
	wantReasons = []string{"spec"}
	plan(runtime.DecisionReplace, false)
	input.Spec.CompletionMode = new("NonIndexed")
	server = "https://new.invalid"
	wantReasons = []string{"server"}
	plan(runtime.DecisionReplace, false)
	require.Positive(t, calls.Load())
}
