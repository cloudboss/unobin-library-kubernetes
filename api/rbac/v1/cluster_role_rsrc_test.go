package v1

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/typecheck"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
)

func TestClusterRoleRegistrationAndSchema(t *testing.T) {
	lib := Library()
	reg := lib.Resources["cluster-role"]
	require.IsType(t, &svc.ClusterRoleResource{}, reg.NewReceiver())
	require.Equal(t, reflect.TypeFor[*svc.ClusterRoleResourceOutput](), reg.OutputType())
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	require.Equal(t, 1, reg.SchemaVersion())
	id, err := reg.StableID(nil, map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
	_, err = reg.StableID(nil, map[string]any{})
	require.Error(t, err)
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	r := schema.Resources["cluster-role"]
	require.ElementsMatch(t, []string{"metadata", "rule", "aggregation-rule", "ignore-annotations",
		"ignore-labels"}, mapKeys(r.Inputs))
	require.ElementsMatch(t, []string{"server", "api-version", "kind", "name", "uid", "metadata",
		"rule", "aggregation-rule"}, mapKeys(r.Outputs))
	require.Equal(t, typecheck.Object, r.Inputs["metadata"].Kind)
	metadata := map[string]typecheck.Type{"name": typecheck.TOptional(typecheck.TString()),
		"generate-name": typecheck.TOptional(typecheck.TString()),
		"annotations":   typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		"labels":        typecheck.TOptional(typecheck.TMap(typecheck.TString()))}
	require.Equal(t, metadata, objectFields(r.Inputs["metadata"]))
	require.Equal(t, typecheck.Optional, r.Inputs["rule"].Kind)
	rules := r.Inputs["rule"].Elem
	require.Equal(t, typecheck.List, rules.Kind)
	members := map[string]typecheck.Type{"verbs": typecheck.TList(typecheck.TString())}
	for _, name := range []string{"api-groups", "resources", "resource-names", "non-resource-urls"} {
		members[name] = typecheck.TOptional(typecheck.TList(typecheck.TString()))
	}
	require.Equal(t, members, objectFields(*rules.Elem))
	aggregation := r.Inputs["aggregation-rule"]
	require.Equal(t, typecheck.Optional, aggregation.Kind)
	fields := objectFields(*aggregation.Elem)
	require.ElementsMatch(t, []string{"cluster-role-selectors"}, mapKeys(fields))
	selectors := fields["cluster-role-selectors"]
	require.Equal(t, typecheck.TOptional(typecheck.TList(typecheck.TString())).Kind, selectors.Kind)
	selectorFields := objectFields(*selectors.Elem.Elem)
	require.ElementsMatch(t, []string{"match-labels", "match-expressions"}, mapKeys(selectorFields))
	require.Equal(t, typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		selectorFields["match-labels"])
	expression := objectFields(*selectorFields["match-expressions"].Elem.Elem)
	require.Equal(t, map[string]typecheck.Type{"key": typecheck.TOptional(typecheck.TString()),
		"operator": typecheck.TOptional(typecheck.TString()),
		"values":   typecheck.TOptional(typecheck.TList(typecheck.TString()))}, expression)
	require.Empty(t, r.SensitiveInputs)
	require.Empty(t, r.SensitiveOutputs)
	require.Empty(t, lib.Actions)
	require.Empty(t, lib.DataSources)
}

func clusterRoleReasons(
	t *testing.T, before, after svc.ClusterRoleResource,
	saved, observed *svc.ClusterRoleResourceOutput, current *config.Configuration,
) []string {
	t.Helper()
	reg := Library().Resources["cluster-role"]
	method := reflect.ValueOf(reg).MethodByName("ReplacementReasons")
	prior := reflect.New(method.Type().In(2)).Elem()
	encode := func(value any) map[string]any {
		body, err := ub.Marshal(value)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, ub.Unmarshal(body, &out))
		return out
	}
	for key, value := range map[string]map[string]any{
		"Inputs": encode(before), "Outputs": encode(saved), "Observed": encode(observed),
		"Configuration": {},
	} {
		prior.FieldByName(key).Set(reflect.ValueOf(value))
	}
	result := method.Call([]reflect.Value{
		reflect.ValueOf(&after), reflect.ValueOf(current), prior,
	})
	require.True(t, result[1].IsNil(), "%v", result[1])
	return result[0].Interface().([]string)
}

func TestClusterRoleReplacementSelectors(t *testing.T) {
	before := svc.ClusterRoleResource{Metadata: svc.ClusterRoleMetadata{Name: new("system:reader")}}
	saved := &svc.ClusterRoleResourceOutput{Name: "system:reader"}
	for _, field := range []string{"name", "generate-name", "server", "kubeconfig",
		"current-context"} {
		t.Run(field, func(t *testing.T) {
			after := before
			current := &config.Configuration{}
			want := "metadata"
			switch field {
			case "name":
				after.Metadata.Name = new("other")
			case "generate-name":
				after.Metadata.Name = nil
				after.Metadata.GenerateName = new("other-")
			default:
				require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
				want = field
			}
			require.Equal(t, []string{want}, clusterRoleReasons(t, before, after, saved, saved, current))
		})
	}
	require.Empty(t, clusterRoleReasons(t, before, before, saved, saved,
		&config.Configuration{Namespace: new("irrelevant"), Token: new("current")}))
	after := before
	after.Metadata.Name = nil
	require.Empty(t, clusterRoleReasons(t, after, before, saved, saved, &config.Configuration{}))
}
