package v1

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"
)

func TestCronJobRegistrationCaptureAndPending(t *testing.T) {
	var calls atomic.Int64
	cfg := cronJobAPI(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	input := cronJob()
	obj, err := input.prepare(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	saved, err := input.output(obj, *cfg.Server, input.Metadata)
	require.NoError(t, err)
	p := cronJobPrior{Inputs: *input, Outputs: saved, Configuration: cfg}
	desired := cronJob()
	desired.Metadata.Name = new("other")
	desired.Spec.JobTemplate.Spec.Completions = new(int64(2))
	base := cronJobTestRegistration(t)
	reg := MakeCronJobRegistration[CronJobResource, *CronJobResourceOutput](base,
		func(ctx context.Context, receiver, c any, p cronJobPrior) (any, error) {
			return receiver.(*CronJobResource).Read(ctx, cfg, p)
		},
		func(
			receiver, c any, got cronJobPrior, pending map[string][]string, configPending bool,
		) ([]string, error) {
			require.Same(t, desired, receiver)
			require.Same(t, cfg, c)
			require.Equal(t, p, got)
			reasons := []string{"spec", "metadata", "other", "other"}
			if !configPending {
				reasons = append(reasons, "server")
			}
			return reasons, nil
		})
	before, err := ub.Marshal([]any{desired, p.Inputs, p.Outputs})
	require.NoError(t, err)
	for _, tc := range []struct {
		name          string
		pending       map[string][]string
		configPending bool
		want          []string
	}{
		{name: "known", want: []string{"metadata", "other", "server", "spec"}},
		{name: "metadata pending", pending: map[string][]string{"metadata": {"name"}},
			want: []string{"other", "server", "spec"}},
		{name: "spec pending", pending: map[string][]string{"spec": {"completions"}},
			want: []string{"metadata", "other", "server"}},
		{name: "both pending", pending: map[string][]string{"metadata": {}, "spec": {}},
			want: []string{"other", "server"}},
		{name: "config pending", configPending: true, want: []string{"metadata", "other", "spec"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasons, err := reg.KnownReplacementReasons(desired, cfg, p, tc.pending, tc.configPending)
			require.NoError(t, err)
			require.Equal(t, tc.want, reasons)
		})
	}
	reasons, err := reg.ReplacementReasons(desired, cfg, p)
	require.NoError(t, err)
	require.Equal(t, []string{"metadata", "other", "server", "spec"}, reasons)
	require.Equal(t,
		[]string{"current-context", "kubeconfig", "metadata", "namespace", "server", "spec"},
		reg.PendingReplacementReasons(map[string][]string{"metadata": {}, "spec": {}}, true))
	after, err := ub.Marshal([]any{desired, p.Inputs, p.Outputs})
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	require.Zero(t, calls.Load())
}

func TestCronJobRegistrationConcurrentBaselines(t *testing.T) {
	base := cronJobTestRegistration(t)
	reg := MakeCronJobRegistration[CronJobResource, *CronJobResourceOutput](base,
		func(ctx context.Context, receiver, cfg any, p cronJobPrior) (any, error) {
			return receiver.(*CronJobResource).Read(ctx, nil, p)
		},
		func(any, any, cronJobPrior, map[string][]string, bool) ([]string, error) { return nil, nil })
	for i := range 40 {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			t.Parallel()
			input := cronJob()
			input.Spec.JobTemplate.Spec.CompletionMode = nil
			obj, err := input.prepare(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			saved, err := input.output(obj, "invalid server: no client permitted", input.Metadata)
			require.NoError(t, err)
			saved.Spec.JobTemplate.Spec.CompletionMode = new("NonIndexed")
			observed := *saved
			observed.Spec = saved.Spec
			observed.Spec.JobTemplate.Spec.CompletionMode = new("Indexed")
			p := cronJobPrior{Inputs: *input, Outputs: saved}
			if i%2 == 1 {
				p.Observed = &observed
			}
			desired := cronJob()
			desired.Spec.JobTemplate.Spec.CompletionMode = new("NonIndexed")
			reasons, err := reg.ReplacementReasons(desired, nil, p)
			require.NoError(t, err)
			if i%2 == 1 {
				require.Equal(t, []string{"spec"}, reasons)
			} else {
				require.Empty(t, reasons)
			}
		})
	}
}

func TestCronJobRegistrationEffectiveValues(t *testing.T) {
	base := cronJobTestRegistration(t)
	reg := MakeCronJobRegistration[CronJobResource, *CronJobResourceOutput](base,
		func(ctx context.Context, receiver, cfg any, p cronJobPrior) (any, error) {
			return receiver.(*CronJobResource).Read(ctx, nil, p)
		},
		func(any, any, cronJobPrior, map[string][]string, bool) ([]string, error) { return nil, nil })
	for _, tc := range cronJobComputedCases() {
		for _, latest := range []bool{false, true} {
			for _, explicit := range []bool{false, true} {
				input := cronJob()
				tc.omit(input)
				if explicit {
					tc.set(input, false)
				}
				obj, err := input.prepare(nil)
				require.NoError(t, err)
				obj.UID = "uid"
				tc.observe(obj, false)
				saved, err := input.output(obj, "no client permitted", input.Metadata)
				require.NoError(t, err)
				p := cronJobPrior{Inputs: *input, Outputs: saved}
				if latest {
					tc.observe(obj, true)
					p.Observed, err = input.output(obj, saved.Server, input.Metadata)
					require.NoError(t, err)
				}
				desired := cronJob()
				tc.set(desired, latest)
				before, err := ub.Marshal([]any{desired, p.Inputs, p.Outputs, p.Observed})
				require.NoError(t, err)
				reasons, err := reg.ReplacementReasons(desired, nil, p)
				require.NoError(t, err)
				require.Empty(t, reasons, "%s latest=%t explicit=%t", tc.name, latest, explicit)
				after, err := ub.Marshal([]any{desired, p.Inputs, p.Outputs, p.Observed})
				require.NoError(t, err)
				require.Equal(t, string(before), string(after))
				tc.set(desired, !latest)
				reasons, err = reg.ReplacementReasons(desired, nil, p)
				require.NoError(t, err)
				root := "spec"
				if tc.name == "name" {
					root = "metadata"
				}
				require.Equal(t, []string{root}, reasons, "%s", tc.name)
			}
		}
	}
}
