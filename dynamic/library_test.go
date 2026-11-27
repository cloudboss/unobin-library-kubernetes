package dynamic

import (
	"os"
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
	require.Equal(t, 2, lib.Resources["object"].SchemaVersion())
	receiver := lib.Resources["object"].NewReceiver().(*svc.ObjectResource)
	require.Error(t, lib.Resources["object"].ValidateInputs(t.Context(), receiver, nil))
	require.NoError(t, runtime.Decode(receiver, map[string]any{
		"manifest": map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "example"}},
	}))
	require.NoError(t, lib.Resources["object"].ValidateInputs(t.Context(), receiver, nil))
	equal, err := lib.Resources["object"].InputsEqual(receiver, map[string]any{
		"manifest": map[string]any{"kind": "ConfigMap", "apiVersion": "v1",
			"metadata": map[string]any{"name": "example"}},
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
		"manifest":        typecheck.TOpaque(),
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

func dynamicCompileDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/dynamic")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	return work
}

func TestObjectManifestComparison(t *testing.T) {
	reg := Library().Resources["object"]
	before := map[string]any{
		"apiVersion": "example.test/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "example", "namespace": "default"},
		"spec":     map[string]any{"replicas": int64(2)},
	}
	receiver := reg.NewReceiver().(*svc.ObjectResource)
	require.NoError(t, runtime.Decode(receiver, map[string]any{"manifest": before}))
	after := map[string]any{
		"kind": "Widget", "apiVersion": "example.test/v1",
		"spec":     map[string]any{"replicas": float64(2)},
		"metadata": map[string]any{"namespace": "default", "name": "example"},
	}
	equal, err := reg.InputsEqual(receiver, map[string]any{"manifest": after})
	require.NoError(t, err)
	require.True(t, equal)
	after["spec"] = map[string]any{"replicas": 3}
	equal, err = reg.InputsEqual(receiver, map[string]any{"manifest": after})
	require.NoError(t, err)
	require.False(t, equal)
	pending := map[string][]string{"manifest": {"input.manifest"}}
	equal, err = reg.KnownInputsEqual(receiver, map[string]any{}, pending)
	require.NoError(t, err)
	require.True(t, equal)
	require.Contains(t, reg.PendingReplacementReasons(pending, false), "manifest")

	// The runtime's prior argument is private; construct its public maps by reflection.
	method := reflect.ValueOf(reg).MethodByName("ReplacementReasons")
	p := reflect.New(method.Type().In(2)).Elem()
	p.FieldByName("Inputs").Set(reflect.ValueOf(map[string]any{"manifest": before}))
	p.FieldByName("Outputs").Set(reflect.ValueOf(map[string]any{"uid": "saved"}))
	call := func() []string {
		result := method.Call([]reflect.Value{
			reflect.ValueOf(receiver), reflect.ValueOf(&config.Configuration{}), p,
		})
		require.True(t, result[1].IsNil(), "%v", result[1].Interface())
		return result[0].Interface().([]string)
	}
	require.Empty(t, call())
	for _, key := range []string{"apiVersion", "kind", "name", "namespace"} {
		t.Run(key, func(t *testing.T) {
			changed := map[string]any{
				"apiVersion": "example.test/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "example", "namespace": "default"},
			}
			if key == "name" || key == "namespace" {
				changed["metadata"].(map[string]any)[key] = "other"
			} else {
				changed[key] = "other"
			}
			receiver.Manifest = changed
			require.Contains(t, call(), "manifest")
		})
	}
}
