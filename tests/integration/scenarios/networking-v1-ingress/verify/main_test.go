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
			resource("resource.routing", runtime.DecisionNoOp),
			resource("resource.replaced", runtime.DecisionNoOp),
		}},
		{name: "writable drift", steps: []runtime.PlanStep{
			resource("resource.routing", runtime.DecisionUpdate),
			resource("resource.replaced", runtime.DecisionNoOp),
		}, wantErr: "resource.routing"},
		{name: "replacement", steps: []runtime.PlanStep{
			resource("resource.routing", runtime.DecisionNoOp),
			resource("resource.replaced", runtime.DecisionReplace),
		}, wantErr: "resource.replaced"},
		{name: "missing", steps: []runtime.PlanStep{
			resource("resource.routing", runtime.DecisionNoOp),
		}, wantErr: "resource.replaced"},
		{name: "unexpected", steps: []runtime.PlanStep{
			resource("resource.routing", runtime.DecisionNoOp),
			resource("resource.replaced", runtime.DecisionNoOp),
			resource("resource.extra", runtime.DecisionNoOp),
		}, wantErr: "resource.extra"},
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
