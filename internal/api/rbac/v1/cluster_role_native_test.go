package v1

import (
	"os"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestClusterRoleNativeMetadataConstraints(t *testing.T) {
	constraints := extractedClusterRoleConstraints(t)
	for _, tc := range []struct {
		name  string
		input ClusterRoleResource
		valid bool
	}{
		{"missing identity", ClusterRoleResource{}, false},
		{"empty name", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("")}}, false},
		{"empty prefix", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			GenerateName: new("")}}, false},
		{"both identities", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			Name: new("reader"), GenerateName: new("reader-")}}, false},
		{"dot name", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new(".")}}, false},
		{"dot prefix", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			GenerateName: new("..")}}, false},
		{"empty api-groups", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader")},
			Rule: &[]ClusterRoleRule{{Verbs: []string{}, APIGroups: &[]string{}}}}, false},
		{"long annotation name", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader"),
			Annotations: &map[string]string{strings.Repeat("a", 64): "value"}}}, false},
		{"long label prefix", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader"),
			Labels: &map[string]string{strings.Repeat("a", 254) + "/key": "value"}}}, false},
		{"empty label value", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader"),
			Labels: &map[string]string{"key": ""}}}, true},
		{"optional rule members", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			Name: new("reader")}, Rule: &[]ClusterRoleRule{{Verbs: []string{},
			APIGroups: &[]string{""}, Resources: &[]string{"*"},
			NonResourceUrls: &[]string{"not a URL"}}},
			AggregationRule: &ClusterRoleAggregation{ClusterRoleSelectors: &[]ClusterRoleSelector{{
				MatchLabels:      &map[string]string{"bad key": "any value"},
				MatchExpressions: &[]ClusterRoleExpression{{Operator: new("arbitrary")}},
			}}}}, true},
		{"slash name", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("bad/name")}}, false},
		{"percent prefix", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			GenerateName: new("bad%prefix")}}, false},
		{"label key", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader"),
			Labels: &map[string]string{"bad key": "value"}}}, false},
		{"label value", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader"),
			Labels: &map[string]string{"key": "bad value"}}}, false},
		{"annotation key", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader"),
			Annotations: &map[string]string{"bad key": "value"}}}, false},
		{"annotation regex", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader")},
			IgnoreAnnotations: &[]string{"["}}, false},
		{"label regex", ClusterRoleResource{Metadata: ClusterRoleMetadata{Name: new("reader")},
			IgnoreLabels: &[]string{"["}}, false},
		{"computed rules", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			Name: new("system:reader")}}, true},
		{"empty rules", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			GenerateName: new("system:reader-")}, Rule: &[]ClusterRoleRule{}}, true},
		{"unicode annotation", ClusterRoleResource{Metadata: ClusterRoleMetadata{
			Name: new("UPPER case"), Annotations: &map[string]string{
				"İK.EXAMPLE/İK": "any value"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := ub.Marshal(tc.input)
			require.NoError(t, err)
			inputs := map[string]any{}
			require.NoError(t, ub.Unmarshal(body, &inputs))
			inputs["aggregation"] = inputs["aggregation-rule"]
			for _, key := range []string{"rule", "ignore-annotations", "ignore-labels"} {
				if _, ok := inputs[key]; !ok {
					inputs[key] = nil
				}
			}
			executor := clusterRoleInputExecutor(t, constraints, inputs)
			_, err = executor.Plan(t.Context())
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "schema: constraints[")
				require.ErrorContains(t, err, "resource.reader")
			}
		})
	}
}

func clusterRoleInputExecutor(
	t *testing.T, constraints []lang.ConstraintSpec, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/cluster-role-state/inputs.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"rbac": {Name: "kubernetes-rbac-v1",
		Configuration: config.LibraryConfiguration(),
		Constraints:   map[string][]lang.ConstraintSpec{"resource.cluster-role": constraints},
		Resources: map[string]runtime.ResourceRegistration{
			"cluster-role": clusterRoleResourceRegistration()},
	}}
	store, err := local.NewStore(t.TempDir(), "inputs", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "inputs", Version: "v0", ContentRevision: "c0"}}
}
