package dynamic

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/cfg"
	"github.com/cloudboss/unobin/pkg/typecheck"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func TestLibrary(t *testing.T) {
	lib := Library()
	require.Equal(t, "kubernetes-dynamic", lib.Name)
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	require.Equal(t, reflect.TypeFor[*svc.ObjectResourceOutput](),
		lib.Resources["object"].OutputType())
	require.True(t, lib.Resources["object"].HasStableID())
	require.Equal(t, 1, lib.Resources["object"].SchemaVersion())
	receiver := lib.Resources["object"].NewReceiver().(*svc.ObjectResource)
	require.Error(t, lib.Resources["object"].ValidateInputs(t.Context(), receiver, nil))
	require.NoError(t, runtime.Decode(receiver, map[string]any{
		"manifest": `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"example"}}`,
	}))
	require.NoError(t, lib.Resources["object"].ValidateInputs(t.Context(), receiver, nil))
	equal, err := lib.Resources["object"].InputsEqual(receiver, map[string]any{
		"manifest": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\n",
	})
	require.NoError(t, err)
	require.True(t, equal)
}

func TestLibrarySchema(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	view, err := cfg.View(config.LibraryConfiguration())
	require.NoError(t, err)
	require.Equal(t, view.SchemaDigest, schema.ConfigurationDigest)
	require.Equal(t, map[string]typecheck.Type{
		"manifest":        typecheck.TString(),
		"field-manager":   typecheck.TOptional(typecheck.TString()),
		"force-conflicts": typecheck.TOptional(typecheck.TBoolean()),
		"delete-timeout":  typecheck.TOptional(typecheck.TString()),
	}, schema.Resources["object"].Inputs)
	require.Equal(t, []string{"manifest"}, schema.Resources["object"].SensitiveInputs)
	require.Equal(t, typecheck.TString(), schema.Resources["object"].Outputs["api-version"])
	require.NotContains(t, schema.Resources["object"].Outputs, "apiVersion")
	require.ElementsMatch(t, []string{"fields", "values"},
		schema.Resources["object"].SensitiveOutputs)
}
