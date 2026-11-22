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

func TestRoleBindingRegistrationSavedName(t *testing.T) {
	base := roleBindingResourceRegistration()
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	baseReasons := []string{"metadata", "role-ref", "server"}
	reg := RoleBindingRegistration[RoleBindingResource, *RoleBindingResourceOutput](base,
		func(ctx context.Context, receiver, cfg any, p roleBindingPrior) (any, error) {
			return receiver.(*RoleBindingResource).Read(ctx, cfg.(*config.Configuration), p)
		}, func(receiver, cfg any, _ roleBindingPrior, pending map[string][]string,
			configPending bool,
		) ([]string, error) {
			require.IsType(t, &RoleBindingResource{}, receiver)
			require.Same(t, c, cfg)
			return slices.DeleteFunc(slices.Clone(baseReasons), func(reason string) bool {
				_, unknown := pending[reason]
				return unknown || configPending && (reason == "server" || reason == "role-ref")
			}), nil
		})
	for _, tc := range []struct {
		name   string
		change func(*RoleBindingResource, *roleBindingPrior)
		want   []string
	}{
		{"saved identity", func(*RoleBindingResource, *roleBindingPrior) {},
			[]string{"role-ref", "server"}},
		{"saved identity before observation", func(_ *RoleBindingResource, p *roleBindingPrior) {
			p.Observed = &RoleBindingResourceOutput{Name: "different-observation"}
		}, []string{"role-ref", "server"}},
		{"observed fallback", func(_ *RoleBindingResource, p *roleBindingPrior) {
			p.Observed, p.Outputs = p.Outputs, nil
		}, []string{"role-ref", "server"}},
		{"empty saved name fallback", func(_ *RoleBindingResource, p *roleBindingPrior) {
			p.Observed = &RoleBindingResourceOutput{Name: p.Outputs.Name}
			p.Outputs.Name = ""
		}, []string{"role-ref", "server"}},
		{"no saved identity", func(_ *RoleBindingResource, p *roleBindingPrior) {
			p.Outputs = nil
		}, []string{"metadata", "role-ref", "server"}},
		{"actual rename", func(r *RoleBindingResource, _ *roleBindingPrior) {
			r.Metadata.Name = new("renamed")
		}, []string{"metadata", "role-ref", "server"}},
		{"configured name remains authoritative", func(_ *RoleBindingResource, p *roleBindingPrior) {
			p.Inputs.Metadata.Name = new("configured-before")
		}, []string{"metadata", "role-ref", "server"}},
		{"namespace change", func(r *RoleBindingResource, _ *roleBindingPrior) {
			r.Metadata.Namespace = new("team")
		}, []string{"metadata", "role-ref", "server"}},
		{"prefix change", func(r *RoleBindingResource, _ *roleBindingPrior) {
			r.Metadata.Name, r.Metadata.GenerateName = nil, new("changed-")
		}, []string{"metadata", "role-ref", "server"}},
		{"omitted name", func(r *RoleBindingResource, _ *roleBindingPrior) {
			r.Metadata.Name = nil
		}, []string{"role-ref", "server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validRoleBinding()
			r.Metadata = RoleMetadata{Name: new("system:reader")}
			p := roleBindingPrior{Inputs: *r, Outputs: savedRoleBinding(*c.Server)}
			p.Inputs.Metadata.Name = nil
			p.Outputs.Name = "system:reader"
			tc.change(r, &p)
			before, err := json.Marshal([]any{r, p})
			require.NoError(t, err)
			reasons, err := reg.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			require.Equal(t, tc.want, reasons)
			for _, pending := range []map[string][]string{
				nil, {"metadata": {"unknown"}}, {"subject": {"unknown"}},
				{"role-ref": {"unknown"}},
			} {
				for _, configPending := range []bool{false, true} {
					want := slices.DeleteFunc(slices.Clone(tc.want), func(reason string) bool {
						_, unknown := pending[reason]
						return unknown || configPending && (reason == "server" || reason == "role-ref")
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
	require.Equal(t, []string{"metadata", "role-ref"}, reg.PendingReplacementReasons(
		map[string][]string{"metadata": {"unknown"}, "role-ref": {"unknown"}}, false))
	require.Equal(t, []string{"current-context", "kubeconfig", "role-ref", "server"},
		reg.PendingReplacementReasons(nil, true))
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			r := validRoleBinding()
			r.Metadata = RoleMetadata{Name: new(string(rune('a' + i)))}
			p := roleBindingPrior{Inputs: *r, Outputs: &RoleBindingResourceOutput{
				Name: *r.Metadata.Name,
			}}
			p.Inputs.Metadata.Name = nil
			reasons, err := reg.ReplacementReasons(r, c, p)
			require.NoError(t, err)
			require.Equal(t, []string{"role-ref", "server"}, reasons)
		})
	}
	workers.Wait()
	require.Zero(t, calls)
	require.Equal(t, []string{"metadata", "role-ref", "server"}, baseReasons)
}

func TestRoleBindingRegistrationForwardErrors(t *testing.T) {
	failure := errors.New("conversion failure")
	for _, readFails := range []bool{false, true} {
		reg := RoleBindingRegistration[RoleBindingResource, *RoleBindingResourceOutput](
			roleBindingResourceRegistration(),
			func(context.Context, any, any, roleBindingPrior) (any, error) {
				if readFails {
					return nil, failure
				}
				t.Fatal("Read must not run after a base comparison error")
				return nil, nil
			}, func(any, any, roleBindingPrior, map[string][]string, bool) ([]string, error) {
				if !readFails {
					return nil, failure
				}
				return nil, nil
			})
		_, err := reg.ReplacementReasons(validRoleBinding(), nil, roleBindingPrior{})
		require.ErrorIs(t, err, failure)
	}
}

func TestRoleBindingPriorCapture(t *testing.T) {
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	p := roleBindingPrior{
		Inputs: *validRoleBinding(), Outputs: savedRoleBinding(*c.Server), Configuration: c,
	}
	p.Observed = p.Outputs
	before, err := json.Marshal(p)
	require.NoError(t, err)
	var captured roleBindingPrior
	r := &RoleBindingResource{capturePrior: &captured, IgnoreLabels: &[]string{"["}}
	result, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Same(t, p.Outputs, result)
	require.Equal(t, p, captured)
	require.Zero(t, calls)
	after, err := json.Marshal(p)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

func TestRoleBindingRestoredNameValidation(t *testing.T) {
	calls := 0
	reg := roleBindingResourceRegistration()
	for _, name := range []string{".", "..", "bad/name", "bad%name", "system:reader-actual"} {
		t.Run(name, func(t *testing.T) {
			before := validRoleBinding()
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
			require.NotEmpty(t, checkNativeRBACInputs(t, "role-binding", &after))
		})
	}
	require.Zero(t, calls)
}
