package main

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestVerifySteadyServiceAccountPlan(t *testing.T) {
	want := map[string]map[string]any{
		serviceAccountExisting: {"automount-service-account-token": true},
		serviceAccountMissing:  {"automount-service-account-token": false},
	}
	plan := &runtime.PlanFile{Steps: []runtime.PlanStep{
		{Address: "resource.node-taint", Decision: runtime.DecisionUpdate},
		{Address: serviceAccountResource, Decision: runtime.DecisionNoOp},
		{Address: serviceAccountExisting, Decision: runtime.DecisionRead,
			ObservedOutputs: want[serviceAccountExisting]},
		{Address: serviceAccountMissing, Decision: runtime.DecisionRead,
			ObservedOutputs: want[serviceAccountMissing]},
	}}
	require.NoError(t, verifySteadyServiceAccountPlan(plan, want))

	plan.Steps[1].Decision = runtime.DecisionUpdate
	require.ErrorContains(t, verifySteadyServiceAccountPlan(plan, want), serviceAccountResource)
}
