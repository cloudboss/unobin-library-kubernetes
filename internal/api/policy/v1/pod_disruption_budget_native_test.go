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

var nativePDBSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/policy/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

func checkNativePDBInputs(t *testing.T, resource string, input any) []string {
	t.Helper()
	schema, err := nativePDBSchema()
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

func addNativePDBConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativePDBSchema()
	require.NoError(t, err)
	library.Constraints = make(map[string][]lang.ConstraintSpec, len(library.Resources))
	for name := range library.Resources {
		library.Constraints["resource."+name] = schema.Resources[name].Constraints
	}
}

func TestPDBNativeIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata map[string]any
		valid    bool
	}{
		{"absent", map[string]any{}, false},
		{"empty", map[string]any{"name": ""}, false},
		{"empty prefix", map[string]any{"generate-name": ""}, false},
		{"conflict", map[string]any{"name": "a", "generate-name": "a-"}, false},
		{"name", map[string]any{"name": "dotted.name"}, true},
		{"name boundary", map[string]any{"name": strings.Repeat("a", 253)}, true},
		{"name overflow", map[string]any{"name": strings.Repeat("a", 254)}, false},
		{"prefix", map[string]any{"generate-name": "pdb-"}, true},
		{"dotted prefix", map[string]any{"generate-name": "a.b-"}, false},
		{"masked uppercase", map[string]any{"generate-name": "A-"}, true},
		{"masked hyphen", map[string]any{"generate-name": "--"}, true},
		{"masked newline", map[string]any{"generate-name": "a\n-"}, true},
		{"lone hyphen", map[string]any{"generate-name": "-"}, false},
		{"unmasked uppercase", map[string]any{"generate-name": "Aa-"}, false},
		{"unmasked unicode", map[string]any{"generate-name": "é-"}, false},
		{"prefix boundary", map[string]any{"generate-name": strings.Repeat("a", 63)}, true},
		{"prefix overflow", map[string]any{"generate-name": strings.Repeat("a", 64)}, false},
		{"masked boundary", map[string]any{
			"generate-name": strings.Repeat("a", 62) + "A-"}, true},
		{"masked overflow", map[string]any{
			"generate-name": strings.Repeat("a", 63) + "A-"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := checkNativePDBInputs(t, "pod-disruption-budget", map[string]any{
				"metadata": test.metadata, "spec": map[string]any{"selector": map[string]any{}},
			})
			if test.valid {
				require.Empty(t, messages)
			} else {
				require.NotEmpty(t, messages)
			}
		})
	}
}

func TestPDBNativeAvailability(t *testing.T) {
	valid := []string{"", "-1", "+01", "2147483647", "-2147483648", "0%", "100%",
		"+01%", "-0%", "-000%", "01%", "0002147483647", "-0002147483648"}
	invalid := []string{"2147483648", "-2147483649", " 1", "1 ", "1.0", "1.5%", "101%",
		"-1%", "++1", "--0%", "%", "+%", "1%%", "99999999999999999999999"}
	for _, field := range []string{"min-available", "max-unavailable"} {
		for _, group := range []struct {
			values []string
			valid  bool
		}{{valid, true}, {invalid, false}} {
			for _, value := range group.values {
				t.Run(field+"/"+value, func(t *testing.T) {
					messages := checkNativePDBInputs(t, "pod-disruption-budget", map[string]any{
						"metadata": map[string]any{"name": "budget"},
						"spec":     map[string]any{"selector": map[string]any{}, field: value},
					})
					if group.valid {
						require.Empty(t, messages)
					} else {
						require.NotEmpty(t, messages)
						require.Contains(t, strings.Join(messages, " "), field)
					}
				})
			}
		}
	}
}

func TestPDBNativeMetadata(t *testing.T) {
	for _, test := range []struct {
		field, key, value string
		valid             bool
	}{
		{"labels", "example.com/KEY", "", true},
		{"labels", "", "", false},
		{"labels", "EXAMPLE.COM/Key", "", false},
		{"labels", "Key", "", false},
		{"labels", strings.Repeat("a", 253) + "/key", "", true},
		{"labels", strings.Repeat("a", 254) + "/key", "", false},
		{"labels", strings.Repeat("a", 63), strings.Repeat("a", 63), true},
		{"labels", strings.Repeat("a", 64), "", false},
		{"labels", "key", strings.Repeat("a", 64), false},
		{"labels", "key", "bad value", false},
		{"annotations", "EXAMPLE.COM/Key", "", true},
		{"annotations", "K.example/Key", "", true},
		{"annotations", "İ.example/İey", "", true},
		{"annotations", "ſ.example/key", "", false},
		{"annotations", strings.Repeat("İ", 63), "", true},
		{"annotations", strings.Repeat("İ", 64), "", false},
		{"annotations", "key", "any value / 😀", true},
	} {
		t.Run(test.field+"/"+test.key+"/"+test.value, func(t *testing.T) {
			messages := checkNativePDBInputs(t, "pod-disruption-budget", map[string]any{
				"metadata": map[string]any{"name": "budget", test.field: map[string]string{
					test.key: test.value,
				}}, "spec": map[string]any{"selector": map[string]any{}},
			})
			if test.valid {
				require.Empty(t, messages)
			} else {
				require.NotEmpty(t, messages)
			}
		})
	}
	for _, field := range []string{"ignore-annotations", "ignore-labels"} {
		messages := checkNativePDBInputs(t, "pod-disruption-budget", map[string]any{
			"metadata": map[string]any{"name": "budget"}, field: []string{"["},
			"spec": map[string]any{"selector": map[string]any{}},
		})
		require.NotEmpty(t, messages)
	}
}
