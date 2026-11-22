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

func TestClusterRoleRegistrationSavedName(t *testing.T) {
	base := clusterRoleResourceRegistration()
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	baseReasons := []string{"metadata", "server"}
	reg := ClusterRoleRegistration[ClusterRoleResource, *ClusterRoleResourceOutput](base,
		func(ctx context.Context, receiver, cfg any, p clusterRolePrior) (any, error) {
			return receiver.(*ClusterRoleResource).Read(ctx, cfg.(*config.Configuration), p)
		}, func(receiver, cfg any, _ clusterRolePrior, pending map[string][]string,
			configPending bool,
		) ([]string, error) {
			require.IsType(t, &ClusterRoleResource{}, receiver)
			require.Same(t, c, cfg)
			return slices.DeleteFunc(slices.Clone(baseReasons), func(reason string) bool {
				_, unknown := pending[reason]
				return unknown || configPending && reason == "server"
			}), nil
		})
	for _, tc := range []struct {
		name   string
		change func(*ClusterRoleResource, *clusterRolePrior)
		want   []string
	}{
		{"saved identity", func(*ClusterRoleResource, *clusterRolePrior) {},
			[]string{"server"}},
		{"saved identity before observation", func(_ *ClusterRoleResource, p *clusterRolePrior) {
			p.Observed = &ClusterRoleResourceOutput{Name: "different-observation"}
		}, []string{"server"}},
		{"observed fallback", func(_ *ClusterRoleResource, p *clusterRolePrior) {
			p.Observed, p.Outputs = p.Outputs, nil
		}, []string{"server"}},
		{"empty saved name fallback", func(_ *ClusterRoleResource, p *clusterRolePrior) {
			p.Observed = &ClusterRoleResourceOutput{Name: p.Outputs.Name}
			p.Outputs.Name = ""
		}, []string{"server"}},
		{"no saved identity", func(_ *ClusterRoleResource, p *clusterRolePrior) {
			p.Outputs = nil
		}, []string{"metadata", "server"}},
		{"actual rename", func(r *ClusterRoleResource, _ *clusterRolePrior) {
			r.Metadata.Name = new("renamed")
		}, []string{"metadata", "server"}},
		{"configured name remains authoritative", func(_ *ClusterRoleResource, p *clusterRolePrior) {
			p.Inputs.Metadata.Name = new("configured-before")
		}, []string{"metadata", "server"}},

		{"prefix change", func(r *ClusterRoleResource, _ *clusterRolePrior) {
			r.Metadata.Name, r.Metadata.GenerateName = nil, new("changed-")
		}, []string{"metadata", "server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validClusterRole()
			r.Metadata = ClusterRoleMetadata{Name: new("system:reader")}
			p := clusterRolePrior{Inputs: *r, Outputs: savedClusterRole(*c.Server)}
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
			r := validClusterRole()
			r.Metadata = ClusterRoleMetadata{Name: new(string(rune('a' + i)))}
			p := clusterRolePrior{Inputs: *r, Outputs: &ClusterRoleResourceOutput{
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

func TestClusterRoleRegistrationForwardErrors(t *testing.T) {
	failure := errors.New("conversion failure")
	for _, readFails := range []bool{false, true} {
		reg := ClusterRoleRegistration[ClusterRoleResource, *ClusterRoleResourceOutput](
			clusterRoleResourceRegistration(),
			func(context.Context, any, any, clusterRolePrior) (any, error) {
				if readFails {
					return nil, failure
				}
				t.Fatal("Read must not run after a base comparison error")
				return nil, nil
			}, func(any, any, clusterRolePrior, map[string][]string, bool) ([]string, error) {
				if !readFails {
					return nil, failure
				}
				return nil, nil
			})
		_, err := reg.ReplacementReasons(validClusterRole(), nil, clusterRolePrior{})
		require.ErrorIs(t, err, failure)
	}
}

func TestClusterRolePriorCapture(t *testing.T) {
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	p := clusterRolePrior{
		Inputs: *validClusterRole(), Outputs: savedClusterRole(*c.Server), Configuration: c,
	}
	p.Observed = p.Outputs
	before, err := json.Marshal(p)
	require.NoError(t, err)
	var captured clusterRolePrior
	r := &ClusterRoleResource{capturePrior: &captured, IgnoreLabels: &[]string{"["}}
	result, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Same(t, p.Outputs, result)
	require.Equal(t, p, captured)
	require.Zero(t, calls)
	after, err := json.Marshal(p)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

func TestClusterRoleInputEquality(t *testing.T) {
	before := validClusterRole()
	before.AggregationRule = &ClusterRoleAggregation{ClusterRoleSelectors: &[]ClusterRoleSelector{{
		MatchExpressions: &[]ClusterRoleExpression{{Values: &[]string{"b", "a", "b"}}},
	}}}
	body, err := ub.Marshal(before)
	require.NoError(t, err)
	var encoded map[string]any
	require.NoError(t, ub.Unmarshal(body, &encoded))
	after := *validClusterRole()
	after.AggregationRule = &ClusterRoleAggregation{ClusterRoleSelectors: &[]ClusterRoleSelector{{
		MatchLabels: &map[string]string{},
		MatchExpressions: &[]ClusterRoleExpression{{Key: new(""), Operator: new(""),
			Values: &[]string{"a", "b"}}},
	}}}
	after.Metadata.Labels = &map[string]string{}
	after.IgnoreLabels = &[]string{}
	reg := clusterRoleResourceRegistration()
	equal, err := reg.InputsEqual(&after, encoded)
	require.NoError(t, err)
	require.True(t, equal)
	(*after.Rule)[0].Verbs = []string{"list", "get"}
	equal, err = reg.InputsEqual(&after, encoded)
	require.NoError(t, err)
	require.False(t, equal)
	before.Rule = nil
	body, err = ub.Marshal(before)
	require.NoError(t, err)
	require.NoError(t, ub.Unmarshal(body, &encoded))
	after = *before
	after.Rule = &[]ClusterRoleRule{}
	equal, err = reg.InputsEqual(&after, encoded)
	require.NoError(t, err)
	require.True(t, equal)
}

func TestClusterRoleEqualityLeavesValidityToConstraints(t *testing.T) {
	reg := clusterRoleResourceRegistration()
	for _, metadata := range []ClusterRoleMetadata{{}, {Name: new("")}, {GenerateName: new("")},
		{Name: new("reader"), GenerateName: new("reader-")}, {Name: new(".")}, {Name: new("bad/name")}} {
		input := ClusterRoleResource{Metadata: metadata}
		body, err := ub.Marshal(input)
		require.NoError(t, err)
		var encoded map[string]any
		require.NoError(t, ub.Unmarshal(body, &encoded))
		equal, err := reg.InputsEqual(&input, encoded)
		require.NoError(t, err)
		require.True(t, equal)
		equal, err = reg.KnownInputsEqual(&input, encoded, nil)
		require.NoError(t, err)
		require.True(t, equal)
	}
}
