package main

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestVerifyNoOpPlan(t *testing.T) {
	resource := func(address string, decision runtime.Decision) runtime.PlanStep {
		return runtime.PlanStep{Address: address, Kind: runtime.NodeResource, Decision: decision}
	}
	for _, tt := range []struct {
		name    string
		steps   []runtime.PlanStep
		wantErr string
	}{
		{name: "unchanged", steps: []runtime.PlanStep{
			resource("resource.ingress-routing", runtime.DecisionNoOp),
			resource("resource.ingress-replaced", runtime.DecisionNoOp),
		}},
		{name: "writable drift", steps: []runtime.PlanStep{
			resource("resource.ingress-routing", runtime.DecisionUpdate),
			resource("resource.ingress-replaced", runtime.DecisionNoOp),
		}, wantErr: "resource.ingress-routing"},
		{name: "replacement", steps: []runtime.PlanStep{
			resource("resource.ingress-routing", runtime.DecisionNoOp),
			resource("resource.ingress-replaced", runtime.DecisionReplace),
		}, wantErr: "resource.ingress-replaced"},
		{name: "missing", steps: []runtime.PlanStep{
			resource("resource.ingress-routing", runtime.DecisionNoOp),
		}, wantErr: "resource.ingress-replaced"},
		{name: "another construct", steps: []runtime.PlanStep{
			resource("resource.ingress-routing", runtime.DecisionNoOp),
			resource("resource.ingress-replaced", runtime.DecisionNoOp),
			resource("resource.network-policy-stable", runtime.DecisionUpdate),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyNoOpPlan(&runtime.PlanFile{Steps: tt.steps})
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}
