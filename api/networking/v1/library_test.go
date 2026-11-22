package v1

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/typecheck"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/networking/v1"
)

func fields(t typecheck.Type) map[string]typecheck.Type {
	out := map[string]typecheck.Type{}
	for _, f := range t.Fields {
		out[f.Name] = f.Type
		if f.Optional {
			out[f.Name] = typecheck.TOptional(f.Type)
		}
	}
	return out
}

func TestIngressRegistrationAndSchema(t *testing.T) {
	lib := Library()
	require.Equal(t, "kubernetes-networking-v1", lib.Name)
	require.Len(t, lib.Resources, 1)
	require.Equal(t, "1.1", lib.Compatibility.RequiredAPI)
	require.Empty(t, lib.DataSources)
	require.Empty(t, lib.Actions)
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	reg := lib.Resources["ingress"]
	require.NotNil(t, reg)
	require.IsType(t, &svc.IngressResource{}, reg.NewReceiver())
	require.Equal(t, reflect.TypeFor[*svc.IngressResourceOutput](), reg.OutputType())
	require.Equal(t, 1, reg.SchemaVersion())
	require.True(t, reg.HasStableID())
	id, err := reg.StableID(map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}},
		map[string]any{"server": "https://cluster", "api-version": "networking.k8s.io/v1",
			"kind": "Ingress", "name": "example", "namespace": "default", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://cluster/uid", *id)
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	r := schema.Resources["ingress"]
	require.Len(t, r.Constraints, 12)
	require.Equal(t, typecheck.Object, r.Inputs["metadata"].Kind)
	require.Equal(t, typecheck.Object, r.Inputs["spec"].Kind)
	require.Len(t, r.Inputs, 7)
	require.Equal(t, typecheck.TOptional(typecheck.TBoolean()), r.Inputs["wait-for-load-balancer"])
	for _, key := range []string{"create-timeout", "delete-timeout"} {
		require.Equal(t, typecheck.TOptional(typecheck.TString()), r.Inputs[key])
	}
	for _, key := range []string{"ignore-labels", "ignore-annotations"} {
		require.Equal(t, typecheck.TOptional(typecheck.TList(typecheck.TString())), r.Inputs[key])
	}
	require.Equal(t, map[string]typecheck.Type{
		"name":          typecheck.TOptional(typecheck.TString()),
		"generate-name": typecheck.TOptional(typecheck.TString()),
		"namespace":     typecheck.TOptional(typecheck.TString()),
		"labels":        typecheck.TOptional(typecheck.TMap(typecheck.TString())),
		"annotations":   typecheck.TOptional(typecheck.TMap(typecheck.TString())),
	}, fields(r.Inputs["metadata"]))
	require.Len(t, fields(r.Inputs["spec"]), 4)
	require.Contains(t, fields(r.Inputs["spec"]), "ingress-class-name")
	require.Contains(t, fields(r.Inputs["spec"]), "rule")
	require.Contains(t, fields(r.Inputs["spec"]), "tls")
	require.Contains(t, fields(r.Inputs["spec"]), "default-backend")
	require.Len(t, r.Outputs, 9)
	for _, key := range []string{"server", "api-version", "kind", "name", "namespace", "uid"} {
		require.Equal(t, typecheck.TString(), r.Outputs[key])
	}
	status := fields(r.Outputs["status"])
	require.Equal(t, map[string]typecheck.Type{"ingress": typecheck.TList(typecheck.TObject(
		[]typecheck.ObjectField{{Name: "ip", Type: typecheck.TString()},
			{Name: "hostname", Type: typecheck.TString()}}))}, fields(status["load-balancer"]))
	require.Empty(t, r.SensitiveInputs)
	require.Empty(t, r.SensitiveOutputs)
}
