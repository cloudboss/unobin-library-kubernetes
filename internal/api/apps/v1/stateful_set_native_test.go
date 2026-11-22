package v1

import (
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func checkNativeStatefulSetInputs(t *testing.T, input any) []string {
	t.Helper()
	schema, err := nativeDeploymentSchema()
	require.NoError(t, err)
	specs := schema.Resources["stateful-set"].Constraints
	require.NotEmpty(t, specs)
	entries, problems := lang.ParseSpecs(specs)
	require.NoError(t, problems.Err())
	encoded, err := ub.Marshal(input)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, ub.Unmarshal(encoded, &values))
	eval := func(expr lang.Expr, binds []lang.EachBinding) (any, error) {
		ctx := &runtime.EvalContext{Inputs: values, MissingAsNull: true}
		runtime.ApplyBindings(ctx, binds)
		return runtime.Eval(expr, ctx)
	}
	return lang.CheckConstraintEntries(entries, values, eval, lang.DisplayNodeRelative).Messages()
}

func addNativeStatefulSetConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativeDeploymentSchema()
	require.NoError(t, err)
	library.Constraints = map[string][]lang.ConstraintSpec{
		"resource.stateful-set": schema.Resources["stateful-set"].Constraints,
	}
}

func TestNativeStatefulSetRules(t *testing.T) {
	require.Empty(t, checkNativeStatefulSetInputs(t, statefulSet()))
	for _, change := range []struct {
		name string
		set  func(*StatefulSetResource)
	}{
		{"missing identity", func(r *StatefulSetResource) { r.Metadata.Name = nil }},
		{"empty name", func(r *StatefulSetResource) { r.Metadata.Name = new("") }},
		{"conflicting identity", func(r *StatefulSetResource) {
			r.Metadata.GenerateName = new("example-")
		}},
		{"zero revision", func(r *StatefulSetResource) {
			r.Spec.RevisionHistoryLimit = new(int64(0))
		}},
		{"negative minimum", func(r *StatefulSetResource) {
			r.Spec.MinReadySeconds = new(int64(-4294967295))
		}},
		{"invalid policy", func(r *StatefulSetResource) {
			r.Spec.PodManagementPolicy = new("Other")
		}},
		{"claim metadata", func(r *StatefulSetResource) {
			claim := statefulSetClaim("BAD")
			r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{claim})
		}},
		{"claim mode", func(r *StatefulSetResource) {
			claim := statefulSetClaim("data")
			claim.Spec.AccessModes = []string{"Other"}
			r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{claim})
		}},
		{"read timeout", func(r *StatefulSetResource) { r.ReadTimeout = new("0") }},
	} {
		t.Run(change.name, func(t *testing.T) {
			r := statefulSet()
			change.set(r)
			require.NotEmpty(t, checkNativeStatefulSetInputs(t, r))
		})
	}
	for _, policy := range []string{"Always", "Never", "OnFailure"} {
		r := statefulSet()
		r.Spec.Template.Spec.RestartPolicy = &policy
		r.Spec.RevisionHistoryLimit = new(int64(4294967297))
		r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{
			Type: new("OnDelete"), RollingUpdate: &StatefulSetRollingUpdate{
				Partition: new(int64(-4294967295)),
			},
		}
		require.Empty(t, checkNativeStatefulSetInputs(t, r))
	}
}
