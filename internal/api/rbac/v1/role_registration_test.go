package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestRoleRegistrationSavedName(t *testing.T) {
	base := roleResourceRegistration()
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	baseReasons := []string{"metadata", "server"}
	reg := RoleRegistration[RoleResource, *RoleResourceOutput](base,
		func(ctx context.Context, receiver, cfg any, p rolePrior) (any, error) {
			return receiver.(*RoleResource).Read(ctx, cfg.(*config.Configuration), p)
		}, func(receiver, cfg any, _ rolePrior, pending map[string][]string,
			configPending bool,
		) ([]string, error) {
			require.IsType(t, &RoleResource{}, receiver)
			require.Same(t, c, cfg)
			return slices.DeleteFunc(slices.Clone(baseReasons), func(reason string) bool {
				_, unknown := pending[reason]
				return unknown || configPending && reason == "server"
			}), nil
		})
	for _, tc := range []struct {
		name   string
		change func(*RoleResource, *rolePrior)
		want   []string
	}{
		{"saved identity", func(*RoleResource, *rolePrior) {},
			[]string{"server"}},
		{"saved identity before observation", func(_ *RoleResource, p *rolePrior) {
			p.Observed = &RoleResourceOutput{Name: "different-observation"}
		}, []string{"server"}},
		{"observed fallback", func(_ *RoleResource, p *rolePrior) {
			p.Observed, p.Outputs = p.Outputs, nil
		}, []string{"server"}},
		{"empty saved name fallback", func(_ *RoleResource, p *rolePrior) {
			p.Observed = &RoleResourceOutput{Name: p.Outputs.Name}
			p.Outputs.Name = ""
		}, []string{"server"}},
		{"no saved identity", func(_ *RoleResource, p *rolePrior) {
			p.Outputs = nil
		}, []string{"metadata", "server"}},
		{"actual rename", func(r *RoleResource, _ *rolePrior) {
			r.Metadata.Name = new("renamed")
		}, []string{"metadata", "server"}},
		{"configured name remains authoritative", func(_ *RoleResource, p *rolePrior) {
			p.Inputs.Metadata.Name = new("configured-before")
		}, []string{"metadata", "server"}},
		{"namespace change", func(r *RoleResource, _ *rolePrior) {
			r.Metadata.Namespace = new("team")
		}, []string{"metadata", "server"}},
		{"prefix change", func(r *RoleResource, _ *rolePrior) {
			r.Metadata.Name, r.Metadata.GenerateName = nil, new("changed-")
		}, []string{"metadata", "server"}},
		{"omitted name", func(r *RoleResource, _ *rolePrior) {
			r.Metadata.Name = nil
		}, []string{"server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validRole()
			r.Metadata = RoleMetadata{Name: new("system:reader")}
			p := rolePrior{Inputs: *r, Outputs: savedRole(*c.Server)}
			p.Inputs.Metadata.Name = nil
			p.Outputs.Name = "system:reader"
			tc.change(r, &p)
			before, err := json.Marshal([]any{r, p})
			require.NoError(t, err)
			reasons, err := reg.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			require.Equal(t, tc.want, reasons)
			for _, pending := range []map[string][]string{
				nil, {"metadata": {"unknown"}}, {"rule": {"unknown"}},
			} {
				for _, configPending := range []bool{false, true} {
					want := slices.DeleteFunc(slices.Clone(tc.want), func(reason string) bool {
						_, unknown := pending[reason]
						return unknown || configPending && reason == "server"
					})
					reasons, err := reg.KnownReplacementReasons(r, c, p, pending, configPending)
					require.NoError(t, err)
					require.Equal(t, want, reasons)
				}
			}
			after, err := json.Marshal([]any{r, p})
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
	require.Equal(t, []string{"metadata"}, reg.PendingReplacementReasons(
		map[string][]string{"metadata": {"unknown"}}, false))
	require.Equal(t, []string{"current-context", "kubeconfig", "server"},
		reg.PendingReplacementReasons(nil, true))
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			r := validRole()
			r.Metadata = RoleMetadata{Name: new(string(rune('a' + i)))}
			p := rolePrior{Inputs: *r, Outputs: &RoleResourceOutput{
				Name: *r.Metadata.Name,
			}}
			p.Inputs.Metadata.Name = nil
			reasons, err := reg.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			require.Equal(t, []string{"server"}, reasons)
		})
	}
	workers.Wait()
	require.Zero(t, calls)
	require.Equal(t, []string{"metadata", "server"}, baseReasons)
}

func TestRoleRegistrationForwardErrors(t *testing.T) {
	failure := errors.New("conversion failure")
	for _, readFails := range []bool{false, true} {
		reg := RoleRegistration[RoleResource, *RoleResourceOutput](
			roleResourceRegistration(),
			func(context.Context, any, any, rolePrior) (any, error) {
				if readFails {
					return nil, failure
				}
				t.Fatal("Read must not run after a base comparison error")
				return nil, nil
			}, func(any, any, rolePrior, map[string][]string, bool) ([]string, error) {
				if !readFails {
					return nil, failure
				}
				return nil, nil
			})
		_, err := reg.ReplacementReasons(validRole(), nil, rolePrior{})
		require.ErrorIs(t, err, failure)
	}
}

func TestRolePriorCapture(t *testing.T) {
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	p := rolePrior{
		Inputs: *validRole(), Outputs: savedRole(*c.Server), Configuration: c,
	}
	p.Observed = p.Outputs
	before, err := json.Marshal(p)
	require.NoError(t, err)
	var captured rolePrior
	r := &RoleResource{capturePrior: &captured, IgnoreLabels: &[]string{"["}}
	result, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Same(t, p.Outputs, result)
	require.Equal(t, p, captured)
	require.Zero(t, calls)
	after, err := json.Marshal(p)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

func TestRoleRestoredNameValidation(t *testing.T) {
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	reg := roleResourceRegistration()
	for _, name := range []string{".", "..", "bad/name", "bad%name", "system:reader-actual"} {
		t.Run(name, func(t *testing.T) {
			before := validRole()
			if name != "system:reader-actual" {
				before.Metadata.GenerateName = nil
			}
			after := *before
			after.Metadata.Name = &name
			body, err := ub.Marshal(before)
			require.NoError(t, err)
			var encoded map[string]any
			require.NoError(t, ub.Unmarshal(body, &encoded))
			equal, err := reg.InputsEqual(&after, encoded)
			require.NoError(t, err)
			require.False(t, equal)
			equal, err = reg.KnownInputsEqual(&after, encoded, nil)
			require.NoError(t, err)
			require.False(t, equal)
			out := savedRole(*c.Server)
			out.Name = name
			p := rolePrior{Inputs: *before, Outputs: out, Observed: out}
			err = reg.ValidateInputs(t.Context(), &after, c)
			if name == "system:reader-actual" {
				require.ErrorContains(t, err, "conflicts with")
			} else {
				require.ErrorContains(t, err, "invalid metadata.name")
			}
			_, err = after.Update(t.Context(), c, p)
			require.Error(t, err)
		})
	}
	require.Zero(t, calls)
}
