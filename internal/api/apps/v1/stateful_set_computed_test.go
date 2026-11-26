package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestStatefulSetComputedDirectUpdate(t *testing.T) {
	for _, field := range []string{"policy", "revision", "name", "claim", "ephemeral"} {
		t.Run(field, func(t *testing.T) {
			before, after := fullStatefulSet(t), fullStatefulSet(t)
			after.WaitForRollout = new(false)
			if field == "claim" {
				(*before.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = new("Filesystem")
				(*after.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = new("Filesystem")
			}
			obj, err := before.expand(nil)
			require.NoError(t, err)
			defaultStatefulSet(obj)
			var calls []string
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "PATCH" {
					var operations []patchOperation
					require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
					obj = applyStatefulSetPatch(t, obj, operations)
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := before.statefulSetOutput(obj, *c.Server, nil)
			require.NoError(t, err)
			switch field {
			case "policy":
				before.Spec.PodManagementPolicy = nil
			case "revision":
				before.Spec.RevisionHistoryLimit = nil
			case "name":
				before.Metadata.Name = nil
			case "claim":
				(*before.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = nil
			case "ephemeral":
				(*before.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.VolumeName = nil
			}
			out, err = after.Update(t.Context(), c, statefulSetPrior{Inputs: *before, Outputs: out})
			require.NoError(t, err)
			require.Equal(t, "uid", out.UID)
			require.Equal(t, []string{"GET", "PATCH", "GET"}, calls)
			switch field {
			case "policy":
				after.Spec.PodManagementPolicy = new("OrderedReady")
			case "revision":
				after.Spec.RevisionHistoryLimit = new(int64(3))
			case "name":
				after.Metadata.Name = new("changed")
			case "claim":
				(*after.Spec.VolumeClaimTemplate)[0].Spec.VolumeMode = new("Block")
			case "ephemeral":
				claim := &(*after.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate
				claim.Spec.VolumeName = new("changed")
			}
			_, err = after.Update(t.Context(), c, statefulSetPrior{Inputs: *before, Outputs: out})
			require.ErrorContains(t, err, "replacement is required")
			require.Equal(t, []string{"GET", "PATCH", "GET"}, calls)
		})
	}
}

func TestStatefulSetRegistrationComparisons(t *testing.T) {
	base := runtime.MakeResource[
		StatefulSetResource, *StatefulSetResourceOutput, *config.Configuration,
	]((&StatefulSetResource{}).ResourceDefinition())
	c := &config.Configuration{Server: new("https://unused.invalid")}
	registration := makeStatefulSetRegistration(base,
		func(ctx context.Context, receiver, cfg any, p statefulSetPrior) (any, error) {
			return receiver.(*StatefulSetResource).Read(ctx, cfg.(*config.Configuration), p)
		}, func(receiver, cfg any, _ statefulSetPrior, pending map[string][]string,
			configPending bool,
		) ([]string, error) {
			require.IsType(t, &StatefulSetResource{}, receiver)
			require.Same(t, c, cfg)
			if configPending {
				return []string{"spec", "metadata", "other"}, nil
			}
			return []string{"spec", "metadata", "other", "server"}, nil
		})
	for _, tc := range []struct {
		name   string
		change func(*StatefulSetResource, *statefulSetPrior)
		want   []string
	}{
		{"saved computed values", func(r *StatefulSetResource, p *statefulSetPrior) {
			r.Spec.PodManagementPolicy = new("OrderedReady")
		}, []string{"other", "server"}},
		{"latest computed values", func(r *StatefulSetResource, p *statefulSetPrior) {
			p.Observed = &StatefulSetResourceOutput{Name: "observed", Spec: StatefulSetSpec{
				PodManagementPolicy: new("Parallel"), RevisionHistoryLimit: new(int64(2))}}
			r.Spec.PodManagementPolicy = new("Parallel")
			r.Metadata.Name = new("observed")
		}, []string{"other", "server"}},
		{"real immutable edits", func(r *StatefulSetResource, p *statefulSetPrior) {
			r.Spec.PodManagementPolicy = new("Parallel")
			r.Metadata.Name = new("changed")
		}, []string{"metadata", "other", "server", "spec"}},
		{"explicit revision width", func(r *StatefulSetResource, p *statefulSetPrior) {
			p.Inputs.Spec.RevisionHistoryLimit = new(int64(1))
			r.Spec.RevisionHistoryLimit = new(int64(4294967297))
		}, []string{"other", "server", "spec"}},
		{"computed negative revision", func(r *StatefulSetResource, p *statefulSetPrior) {
			p.Outputs.Spec.RevisionHistoryLimit = new(int64(-1))
			r.Spec.RevisionHistoryLimit = nil
		}, []string{"other", "server"}},
		{"computed zero revision", func(r *StatefulSetResource, p *statefulSetPrior) {
			p.Outputs.Spec.RevisionHistoryLimit = new(int64(0))
			r.Spec.RevisionHistoryLimit = nil
		}, []string{"other", "server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := statefulSet()
			p := statefulSetPrior{Inputs: *statefulSet(), Outputs: &StatefulSetResourceOutput{
				Name: "example", Spec: StatefulSetSpec{PodManagementPolicy: new("OrderedReady"),
					RevisionHistoryLimit: new(int64(2))}}}
			p.Inputs.Metadata.Name = nil
			tc.change(r, &p)
			before, err := json.Marshal([]any{r, p})
			require.NoError(t, err)
			reasons, err := registration.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			require.Equal(t, tc.want, reasons)
			for _, pending := range []map[string][]string{
				nil, {"metadata": {"unresolved"}}, {"spec": {"unresolved"}},
				{"metadata": {"unresolved"}, "spec": {"unresolved"}},
			} {
				for _, configPending := range []bool{false, true} {
					want := []string{}
					for _, reason := range tc.want {
						if _, unknown := pending[reason]; unknown || configPending && reason == "server" {
							continue
						}
						want = append(want, reason)
					}
					reasons, err := registration.KnownReplacementReasons(r, c, p, pending, configPending)
					require.NoError(t, err)
					require.Equal(t, want, reasons)
				}
			}
			after, err := json.Marshal([]any{r, p})
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
	require.Equal(t, []string{"metadata", "spec"}, registration.PendingReplacementReasons(
		map[string][]string{"metadata": {"unknown"}, "spec": {"unknown"}}, false))
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			r := statefulSet()
			r.Spec.RevisionHistoryLimit = new(int64(i + 1))
			p := statefulSetPrior{Inputs: *statefulSet(), Outputs: &StatefulSetResourceOutput{
				Name: "example", Spec: StatefulSetSpec{RevisionHistoryLimit: new(int64(i + 1))}}}
			reasons, err := registration.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			require.Equal(t, []string{"other", "server"}, reasons)
		})
	}
	workers.Wait()
}

func TestStatefulSetRegistrationForwardErrors(t *testing.T) {
	base := statefulSetResourceRegistration()
	failure := errors.New("conversion failure")
	for _, readFails := range []bool{false, true} {
		registration := makeStatefulSetRegistration(base,
			func(context.Context, any, any, statefulSetPrior) (any, error) {
				if readFails {
					return nil, failure
				}
				t.Fatal("Read must not run after a base comparison error")
				return nil, nil
			}, func(any, any, statefulSetPrior, map[string][]string, bool) ([]string, error) {
				if !readFails {
					return nil, failure
				}
				return nil, nil
			})
		_, err := registration.ReplacementReasons(statefulSet(), nil, statefulSetPrior{})
		require.ErrorIs(t, err, failure)
	}
}

func TestStatefulSetPriorCapture(t *testing.T) {
	calls := 0
	c := statefulSetAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	input := statefulSet()
	obj, err := input.expand(nil)
	require.NoError(t, err)
	defaultStatefulSet(obj)
	out, err := input.statefulSetOutput(obj, *c.Server, nil)
	require.NoError(t, err)
	p := statefulSetPrior{Inputs: *input, Outputs: out, Observed: out, Configuration: c}
	before, err := json.Marshal(p)
	require.NoError(t, err)
	var captured statefulSetPrior
	r := &StatefulSetResource{capturePrior: &captured, ReadTimeout: new("invalid")}
	result, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Same(t, out, result)
	require.Equal(t, p, captured)
	require.Zero(t, calls)
	after, err := json.Marshal(p)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}
