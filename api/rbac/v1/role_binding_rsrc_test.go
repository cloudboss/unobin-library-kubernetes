package v1

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/typecheck"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
)

func TestRoleBindingRegistrationAndSchema(t *testing.T) {
	lib := Library()
	reg := lib.Resources["role-binding"]
	require.IsType(t, &svc.RoleBindingResource{}, reg.NewReceiver())
	require.Equal(t, reflect.TypeFor[*svc.RoleBindingResourceOutput](), reg.OutputType())
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
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	r := schema.Resources["role-binding"]
	require.ElementsMatch(t, []string{"metadata", "role-ref", "subject",
		"ignore-annotations", "ignore-labels"}, mapKeys(r.Inputs))
	require.ElementsMatch(t, []string{"server", "api-version", "kind", "name", "namespace", "uid",
		"metadata", "role-ref", "subject"}, mapKeys(r.Outputs))
	metadata := map[string]typecheck.Type{
		"name":          typecheck.TOptional(typecheck.TString()),
		"namespace":     typecheck.TOptional(typecheck.TString()),
		"generate-name": typecheck.TOptional(typecheck.TString()),
		"annotations":   typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		"labels":        typecheck.TOptional(typecheck.TMap(typecheck.TString())),
	}
	require.Equal(t, typecheck.Object, r.Inputs["metadata"].Kind)
	require.Equal(t, metadata, objectFields(r.Inputs["metadata"]))
	reference := map[string]typecheck.Type{"api-group": typecheck.TString(),
		"kind": typecheck.TString(), "name": typecheck.TString()}
	require.Equal(t, reference, objectFields(r.Inputs["role-ref"]))
	require.Equal(t, reference, objectFields(r.Outputs["role-ref"]))
	require.Equal(t, typecheck.List, r.Inputs["subject"].Kind)
	require.Equal(t, map[string]typecheck.Type{
		"kind": typecheck.TString(), "name": typecheck.TString(),
		"api-group": typecheck.TOptional(typecheck.TString()),
		"namespace": typecheck.TOptional(typecheck.TString()),
	}, objectFields(*r.Inputs["subject"].Elem))
	require.Equal(t, map[string]typecheck.Type{
		"kind": typecheck.TString(), "name": typecheck.TString(),
		"api-group": typecheck.TString(), "namespace": typecheck.TString(),
	}, objectFields(*r.Outputs["subject"].Elem))
	for _, key := range []string{"ignore-annotations", "ignore-labels"} {
		require.Equal(t, typecheck.TOptional(typecheck.TList(typecheck.TString())), r.Inputs[key])
	}
	for key := range metadata {
		metadata[key] = typecheck.TString()
	}
	metadata["annotations"], metadata["labels"] = typecheck.TMap(typecheck.TString()),
		typecheck.TMap(typecheck.TString())
	require.Equal(t, metadata, objectFields(r.Outputs["metadata"]))
	require.Empty(t, r.SensitiveInputs)
	require.Empty(t, r.SensitiveOutputs)
}

func TestRoleBindingCompiledFixtures(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"valid", ""}, {"missing-metadata", "metadata"}, {"missing-ref", "role-ref"},
		{"missing-ref-member", "api-group"}, {"missing-subject", "subject"},
		{"missing-subject-kind", "kind"}, {"missing-subject-name", "name"},
		{"null-map", "expected optional(map(string))"},
		{"null-subject-name", "expected string, got null"}, {"unsupported", "finalizers"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			compileBindingFixture(t, filepath.Join("testdata/ub/role-binding", tt.name), tt.want)
		})
	}
}

func TestRoleBindingCompiledScenario(t *testing.T) {
	compileBindingFixture(t, "../../../tests/integration/scenarios/rbac-v1-role-binding", "")
}

func compileBindingFixture(t *testing.T, fixture, wantError string) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	fixture, err = filepath.Abs(fixture)
	require.NoError(t, err)
	work := t.TempDir()
	var log bytes.Buffer
	sdk, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	result, err := compile.RunResult(compile.Options{
		ReplaceUnobin: strings.TrimSpace(string(sdk)),
		FactoryPath:   filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "role-binding-test", Version: "v0.0.0", CLIVersion: "dev",
		GoVersion: "1.26.2", Build: wantError == "",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	if wantError != "" {
		require.ErrorContains(t, err, wantError, "%s", log.String())
		return
	}
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	configs, err := filepath.Glob(filepath.Join(fixture, "config*.ub"))
	require.NoError(t, err)
	require.NotEmpty(t, configs)
	for _, path := range configs {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		name := filepath.Base(path)
		require.NoError(t, os.WriteFile(filepath.Join(work, name), body, 0o600))
		for _, args := range [][]string{{"pin", "-c", name}, {"validate", "-c", name}} {
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
			cmd.Dir = work
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
		}
	}
}

func bindingReasons(
	t *testing.T, before, after svc.RoleBindingResource,
	saved, observed *svc.RoleBindingResourceOutput, current *config.Configuration,
) []string {
	t.Helper()
	reg := Library().Resources["role-binding"]
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

func TestRoleBindingReplacementAndEquality(t *testing.T) {
	before := svc.RoleBindingResource{
		Metadata: svc.RoleMetadata{Name: new("system:reader")},
		RoleRef: svc.RoleBindingRoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader",
		},
		Subject: []svc.RoleBindingSubject{{Kind: "User", Name: "alice", APIGroup: new("known")}},
	}
	after := before
	after.Metadata.Name, after.Metadata.Namespace = nil, new("default")
	after.Metadata.Annotations, after.Metadata.Labels = &map[string]string{}, &map[string]string{}
	after.Subject = []svc.RoleBindingSubject{{Kind: "User", Name: "alice", Namespace: new("default")}}
	reg := Library().Resources["role-binding"]
	body, err := ub.Marshal(before)
	require.NoError(t, err)
	var encoded map[string]any
	require.NoError(t, ub.Unmarshal(body, &encoded))
	equal, err := reg.InputsEqual(&after, encoded)
	require.NoError(t, err)
	require.True(t, equal)
	omitted := before
	omitted.Subject = []svc.RoleBindingSubject{{Kind: "User", Name: "alice"}}
	explicitEmpty := omitted
	explicitEmpty.Subject = []svc.RoleBindingSubject{{Kind: "User", Name: "alice", APIGroup: new("")}}
	body, err = ub.Marshal(omitted)
	require.NoError(t, err)
	encoded = nil
	require.NoError(t, ub.Unmarshal(body, &encoded))
	equal, err = reg.InputsEqual(&explicitEmpty, encoded)
	require.NoError(t, err)
	require.False(t, equal, "a new explicit empty group must replace a computed value")
	saved := &svc.RoleBindingResourceOutput{RoleRef: before.RoleRef}
	require.Empty(t, bindingReasons(t, before, after, saved, saved, &config.Configuration{}))
	for _, field := range []string{
		"name", "namespace", "generate-name", "api-group", "kind", "ref-name",
		"server", "kubeconfig", "current-context",
	} {
		t.Run(field, func(t *testing.T) {
			changed := before
			current := &config.Configuration{}
			want := "metadata"
			switch field {
			case "name":
				changed.Metadata.Name = new("other")
			case "namespace":
				changed.Metadata.Namespace = new("team")
			case "generate-name":
				changed.Metadata.Name, changed.Metadata.GenerateName = nil, new("other-")
			case "api-group":
				changed.RoleRef.APIGroup, want = "other", "role-ref"
			case "kind":
				changed.RoleRef.Kind, want = "ClusterRole", "role-ref"
			case "ref-name":
				changed.RoleRef.Name, want = "other", "role-ref"
			default:
				require.NoError(t, runtime.Decode(current, map[string]any{field: "changed"}))
				want = field
			}
			require.Equal(t, []string{want}, bindingReasons(t, before, changed, saved, saved, current))
		})
	}
	observed := *saved
	observed.RoleRef.Name = "drift"
	require.Equal(t, []string{"role-ref"}, bindingReasons(t, before, before, saved, &observed,
		&config.Configuration{}))
	require.Empty(t, bindingReasons(t, before, before, saved, saved,
		&config.Configuration{Token: new("new"), Namespace: new("configured")}))
}

func TestRoleBindingRestoredNameReplacement(t *testing.T) {
	before := svc.RoleBindingResource{
		Metadata: svc.RoleMetadata{},
		RoleRef: svc.RoleBindingRoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "reader",
		},
		Subject: []svc.RoleBindingSubject{{Kind: "User", Name: "alice"}},
	}
	after := before
	after.Metadata.Name = new("system:reader")
	saved := &svc.RoleBindingResourceOutput{Name: "system:reader", RoleRef: before.RoleRef}
	require.Empty(t, bindingReasons(t, before, after, saved, saved, &config.Configuration{}))
	observed := *saved
	observed.RoleRef.Name = "drift"
	require.Equal(t, []string{"role-ref"}, bindingReasons(t, before, after, saved, &observed,
		&config.Configuration{}))
	require.Equal(t, []string{"server"}, bindingReasons(t, before, after, saved, saved,
		&config.Configuration{Server: new("https://other")}))
	after.RoleRef.Name = "changed"
	require.Equal(t, []string{"role-ref"}, bindingReasons(t, before, after, saved, saved,
		&config.Configuration{}))
	after.RoleRef = before.RoleRef
	after.Metadata.Name = new("system:renamed")
	require.Equal(t, []string{"metadata"}, bindingReasons(t, before, after, saved, saved,
		&config.Configuration{}))
}
