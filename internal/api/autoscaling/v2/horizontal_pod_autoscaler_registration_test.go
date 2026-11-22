package v2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestHPACaptureComparison(t *testing.T) {
	calls := 0
	c := hpaAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	base := hpaResourceRegistration()
	reg := HorizontalPodAutoscalerRegistration[HorizontalPodAutoscalerResource,
		*HorizontalPodAutoscalerResourceOutput](base,
		func(ctx context.Context, r, c any, p hpaPrior) (any, error) {
			return r.(*HorizontalPodAutoscalerResource).Read(ctx, c.(*config.Configuration), p)
		},
		func(any, any, hpaPrior, map[string][]string, bool) ([]string, error) {
			return []string{"object", "server", "unrelated", "server"}, nil
		})
	for _, observed := range []bool{false, true} {
		for _, name := range []string{"test", "different"} {
			r := validHPA()
			r.Object.Metadata.Name = &name
			p := hpaPrior{Inputs: *validHPA(), Outputs: savedHPA(*c.Server)}
			p.Inputs.Object.Metadata.Name = nil
			if observed {
				p.Observed = p.Outputs
			}
			before, err := json.Marshal([]any{r, p})
			require.NoError(t, err)
			reasons, err := reg.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			want := []string{"server", "unrelated"}
			if name != "test" {
				want = append([]string{"object"}, want...)
			}
			require.Equal(t, want, reasons)
			reasons, err = reg.KnownReplacementReasons(r, c, p, map[string][]string{"object": {
				"pending"}}, true)
			require.NoError(t, err)
			require.Equal(t, []string{"server", "unrelated"}, reasons)
			after, err := json.Marshal([]any{r, p})
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		}
	}
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			r := validHPA()
			r.Object.Metadata.Name = new("same")
			p := hpaPrior{Inputs: *validHPA(), Outputs: savedHPA(*c.Server)}
			p.Inputs.Object.Metadata.Name = nil
			p.Outputs.Object.Metadata.Name = "same"
			want := []string{"server", "unrelated"}
			if i%2 == 1 {
				p.Outputs.Object.Metadata.Name = "other"
				want = slices.Insert(want, 0, "object")
			}
			reasons, err := reg.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			require.Equal(t, want, reasons)
		})
	}
	workers.Wait()
	require.Zero(t, calls)
}

func TestHPAIdentityDirectUpdate(t *testing.T) {
	calls := 0
	c := hpaAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	r := validHPA()
	p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
	p.Inputs.Object.Metadata.Name = nil
	require.False(t, hpaIdentityChanged(r.Object.Metadata, p))
	observed := *p.Outputs
	observed.Object.Metadata.Name = "observed"
	p.Observed = &observed
	require.True(t, hpaIdentityChanged(r.Object.Metadata, p))
	r.Object.Metadata.Name = new("observed")
	require.False(t, hpaIdentityChanged(r.Object.Metadata, p))
	for _, change := range []func(*HorizontalPodAutoscalerResource){
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.Name = new("different") },
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.Namespace = new("other") },
		func(r *HorizontalPodAutoscalerResource) {
			r.Object.Metadata.Name = nil
			r.Object.Metadata.GenerateName = new("other-")
		},
	} {
		desired := *r
		change(&desired)
		_, err := desired.Update(t.Context(), c, p)
		require.ErrorContains(t, err, "replacement is required")
	}
	p.Inputs.Object.Metadata.Name = nil
	p.Inputs.Object.Metadata.GenerateName = new("hpa-")
	require.True(t, hpaIdentityChanged(HPAMetadata{}, p))
	p.Inputs.Object.Metadata.GenerateName = nil
	p.Observed = nil
	require.False(t, hpaIdentityChanged(HPAMetadata{Namespace: new("default")}, p))
	require.Zero(t, calls)
}

func TestHPAInputEqualityAndNativeValidity(t *testing.T) {
	reg := hpaResourceRegistration()
	before := validHPA()
	body, err := ub.Marshal(before)
	require.NoError(t, err)
	var encoded map[string]any
	require.NoError(t, ub.Unmarshal(body, &encoded))
	for _, change := range []func(*HorizontalPodAutoscalerResource){
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.Name = new("Bad/name") },
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.GenerateName = new("hpa-") },
	} {
		after := *before
		change(&after)
		equal, err := reg.InputsEqual(&after, encoded)
		require.NoError(t, err)
		require.False(t, equal)
		equal, err = reg.KnownInputsEqual(&after, encoded, nil)
		require.NoError(t, err)
		require.False(t, equal)
		require.NoError(t, reg.ValidateInputs(t.Context(), &after, &config.Configuration{}))
		require.NotEmpty(t, checkHPANative(t, &after))
	}
	after := *before
	after.Object.Metadata.Name = nil
	after.Object.Metadata.Namespace = new("default")
	after.Object.Spec.MinReplicas = new(1)
	equal, err := reg.InputsEqual(&after, encoded)
	require.NoError(t, err)
	require.True(t, equal)
	require.NoError(t, reg.ValidateInputs(t.Context(), &after, &config.Configuration{}))
	require.NotEmpty(t, checkHPANative(t, &after))
}
