package v1

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

type replacementMethods[P any] struct {
	runtime.ResourceRegistration
	t        *testing.T
	ordinary func(any, any, P) ([]string, error)
	known    func(any, any, P, map[string][]string, bool) ([]string, error)
}

func checkReplacementMethods[P any](
	t *testing.T, base runtime.ResourceRegistration,
	ordinary func(any, any, P) ([]string, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) replacementMethods[P] {
	return replacementMethods[P]{ResourceRegistration: base, t: t, ordinary: ordinary, known: known}
}

func (r replacementMethods[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if len(pending) == 0 && !configPending {
		ordinary, ordinaryErr := r.ordinary(receiver, cfg, p)
		require.Equal(r.t, err, ordinaryErr)
		require.Equal(r.t, reasons, ordinary)
	}
	return reasons, err
}

func TestRegistrationCaptureAndPending(t *testing.T) {
	var calls atomic.Int64
	cfg := testAPI(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	input := job()
	obj, err := input.expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	saved, err := input.output(obj, *cfg.Server)
	require.NoError(t, err)
	p := prior{Inputs: *input, Outputs: saved, Configuration: cfg}
	desired := job()
	desired.Metadata.Name = new("other")
	desired.Spec.Completions = new(int64(2))
	base := testJobRegistration(t)
	reg := MakeJobRegistration[JobResource, *JobResourceOutput](base,
		func(ctx context.Context, receiver, c any, p prior) (any, error) {
			return receiver.(*JobResource).Read(ctx, cfg, p)
		},
		func(
			receiver, c any, got prior, pending map[string][]string, configPending bool,
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

func TestRegistrationErrors(t *testing.T) {
	base := testJobRegistration(t)
	want := errors.New("conversion failed")
	for _, readError := range []bool{false, true} {
		reg := MakeJobRegistration[JobResource, *JobResourceOutput](base,
			func(context.Context, any, any, prior) (any, error) { return nil, want },
			func(any, any, prior, map[string][]string, bool) ([]string, error) {
				if !readError {
					return nil, want
				}
				return nil, nil
			})
		_, err := reg.ReplacementReasons(job(), nil, prior{})
		require.ErrorIs(t, err, want)
	}
}

func TestRegistrationConcurrentBaselines(t *testing.T) {
	base := testJobRegistration(t)
	reg := MakeJobRegistration[JobResource, *JobResourceOutput](base,
		func(ctx context.Context, receiver, cfg any, p prior) (any, error) {
			return receiver.(*JobResource).Read(ctx, nil, p)
		},
		func(any, any, prior, map[string][]string, bool) ([]string, error) { return nil, nil })
	for i := range 40 {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			t.Parallel()
			input := job()
			input.Spec.CompletionMode = nil
			obj, err := input.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			saved, err := input.output(obj, "invalid server: no client permitted")
			require.NoError(t, err)
			saved.Spec.CompletionMode = new("NonIndexed")
			observed := *saved
			observed.Spec = saved.Spec
			observed.Spec.CompletionMode = new("Indexed")
			p := prior{Inputs: *input, Outputs: saved}
			if i%2 == 1 {
				p.Observed = &observed
			}
			desired := job()
			desired.Spec.CompletionMode = new("NonIndexed")
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

func TestRegistrationEffectiveValues(t *testing.T) {
	base := testJobRegistration(t)
	reg := MakeJobRegistration[JobResource, *JobResourceOutput](base,
		func(ctx context.Context, receiver, cfg any, p prior) (any, error) {
			return receiver.(*JobResource).Read(ctx, nil, p)
		},
		func(any, any, prior, map[string][]string, bool) ([]string, error) { return nil, nil })
	for _, tc := range computedCases() {
		for _, latest := range []bool{false, true} {
			for _, explicit := range []bool{false, true} {
				input := job()
				tc.omit(input)
				if explicit {
					tc.set(input, false)
				}
				obj, err := input.expand(nil)
				require.NoError(t, err)
				obj.UID = "uid"
				tc.observe(obj, false)
				saved, err := input.output(obj, "no client permitted")
				require.NoError(t, err)
				p := prior{Inputs: *input, Outputs: saved}
				if latest {
					tc.observe(obj, true)
					p.Observed, err = input.output(obj, saved.Server)
					require.NoError(t, err)
				}
				desired := job()
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

func TestRegistrationInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		root string
		edit func(*JobResource)
	}{
		{name: "conflicting identity", root: "metadata", edit: func(r *JobResource) {
			r.Metadata.GenerateName = new("example-")
		}},
		{name: "invalid name", root: "metadata", edit: func(r *JobResource) {
			r.Metadata.Name = new("INVALID")
		}},
		{name: "invalid completion mode", root: "spec", edit: func(r *JobResource) {
			r.Spec.CompletionMode = new("invalid")
		}},
		{name: "invalid container port", root: "spec", edit: func(r *JobResource) {
			(*r.Spec.Template.Spec.Container)[0].Port = new([]Port{{ContainerPort: 65536}})
		}},
		{name: "invalid template identity", root: "spec", edit: func(r *JobResource) {
			r.Spec.Template.Metadata.Name = new("INVALID")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := testJobRegistration(t)
			input := job()
			data, err := ub.Marshal(input)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, ub.Unmarshal(data, &fields))
			desired := job()
			tc.edit(desired)
			require.NotEmpty(t, checkNativeJobInputs(t, desired))
			require.NoError(t, reg.ValidateInputs(t.Context(), desired, nil))
			equal, err := reg.KnownInputsEqual(desired, fields,
				map[string][]string{tc.root: {"unresolved"}})
			require.NoError(t, err)
			require.True(t, equal)
		})
	}
}
