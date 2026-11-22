package v1

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

var nativeCoreSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/core/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

func checkNativeCoreInputs(t *testing.T, resource string, input any) []string {
	t.Helper()
	schema, err := nativeCoreSchema()
	require.NoError(t, err)
	entries, errors := lang.ParseSpecs(schema.Resources[resource].Constraints)
	require.Empty(t, errors.Errors())
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

func addNativeCoreConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativeCoreSchema()
	require.NoError(t, err)
	library.Constraints = make(map[string][]lang.ConstraintSpec, len(library.Resources))
	for name := range library.Resources {
		library.Constraints["resource."+name] = schema.Resources[name].Constraints
	}
}

func TestNativeCoreMetadataSyntax(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata ConfigMapMetadata
	}{
		{"name", ConfigMapMetadata{Name: new("BAD")}},
		{"prefix", ConfigMapMetadata{GenerateName: new("bad.prefix-")}},
		{"label", ConfigMapMetadata{Name: new("test"), Labels: &map[string]string{"bad key": "v"}}},
		{"annotation", ConfigMapMetadata{Name: new("test"),
			Annotations: &map[string]string{"bad key": "v"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := ConfigMapResource{Object: ConfigMapObject{Metadata: test.metadata}}
			require.NotEmpty(t, checkNativeCoreInputs(t, "config-map", input))
		})
	}
}

func TestNativeCoreIdentityAndMetadataBoundaries(t *testing.T) {
	for _, resource := range []string{
		"config-map", "namespace", "secret", "service-account", "service",
	} {
		t.Run(resource, func(t *testing.T) {
			for _, test := range []struct {
				name     string
				metadata map[string]any
				valid    bool
			}{
				{"missing", map[string]any{}, false},
				{"empty name", map[string]any{"name": ""}, false},
				{"empty prefix", map[string]any{"generate-name": ""}, false},
				{"both", map[string]any{"name": "valid", "generate-name": "valid-"}, false},
				{"explicit", map[string]any{"name": "dotted.name"}, true},
				{"generated", map[string]any{"generate-name": "prefix-"}, true},
				{"long name", map[string]any{"name": strings.Repeat("a", 254)}, false},
				{"name boundary", map[string]any{"name": strings.Repeat("a", 253)}, true},
				{"long prefix", map[string]any{"generate-name": strings.Repeat("a", 64)}, false},
				{"prefix boundary", map[string]any{"generate-name": strings.Repeat("a", 63)}, true},
				{"masked boundary", map[string]any{
					"generate-name": strings.Repeat("a", 62) + "A-"}, true},
				{"masked uppercase", map[string]any{"generate-name": "A-"}, true},
				{"masked hyphen", map[string]any{"generate-name": "--"}, true},
				{"masked newline", map[string]any{"generate-name": "a\n-"}, true},
				{"unmasked uppercase", map[string]any{"generate-name": "Aa-"}, false},
				{"unmasked unicode", map[string]any{"generate-name": "é-"}, false},
				{"unmasked multibyte", map[string]any{"generate-name": "€-"}, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					input := map[string]any{"object": map[string]any{"metadata": test.metadata}}
					if resource == "service" {
						input = map[string]any{"metadata": test.metadata, "spec": map[string]any{}}
					}
					diagnostics := checkNativeCoreInputs(t, resource, input)
					if test.valid {
						require.Empty(t, diagnostics)
					} else {
						require.NotEmpty(t, diagnostics)
					}
				})
			}
			for _, test := range []struct {
				name, field, key, value string
				valid                   bool
			}{
				{"label empty value", "labels", "example.com/KEY", "", true},
				{"label empty key", "labels", "", "", false},
				{"label uppercase prefix", "labels", "EXAMPLE.COM/Key", "", false},
				{"label uppercase suffix", "labels", "example.com/Key", "", true},
				{"label unicode", "labels", "Key", "", false},
				{"label prefix boundary", "labels", strings.Repeat("a", 253) + "/key", "", true},
				{"label prefix overflow", "labels", strings.Repeat("a", 254) + "/key", "", false},
				{"label suffix boundary", "labels", strings.Repeat("a", 63), "", true},
				{"label suffix overflow", "labels", strings.Repeat("a", 64), "", false},
				{"label value boundary", "labels", "key", strings.Repeat("a", 63), true},
				{"label value overflow", "labels", "key", strings.Repeat("a", 64), false},
				{"annotation uppercase", "annotations", "EXAMPLE.COM/Key", "", true},
				{"annotation Kelvin", "annotations", "K.example/Key", "", true},
				{"annotation dotted I", "annotations", "İ.example/İey", "", true},
				{"annotation long s", "annotations", "ſ.example/key", "", false},
				{"annotation Unicode boundary", "annotations", strings.Repeat("İ", 63), "", true},
				{"annotation Unicode overflow", "annotations", strings.Repeat("İ", 64), "", false},
				{"annotation arbitrary value", "annotations", "key", "any value / 😀", true},
			} {
				t.Run(test.name, func(t *testing.T) {
					metadata := map[string]any{"name": "valid", test.field: map[string]string{
						test.key: test.value,
					}}
					input := map[string]any{"object": map[string]any{"metadata": metadata}}
					if resource == "service" {
						input = map[string]any{"metadata": metadata, "spec": map[string]any{}}
					}
					diagnostics := checkNativeCoreInputs(t, resource, input)
					if test.valid {
						require.Empty(t, diagnostics)
					} else {
						require.NotEmpty(t, diagnostics)
					}
				})
			}
		})
	}
}
