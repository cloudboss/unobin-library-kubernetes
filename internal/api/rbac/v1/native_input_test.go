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

var nativeRBACSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/rbac/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

func checkNativeRBACInputs(t *testing.T, resource string, input any) []string {
	t.Helper()
	schema, err := nativeRBACSchema()
	require.NoError(t, err)
	entries, errors := lang.ParseSpecs(schema.Resources[resource].Constraints)
	require.Empty(t, errors.Errors())
	values, ok := input.(map[string]any)
	if !ok {
		encoded, err := ub.Marshal(input)
		require.NoError(t, err)
		require.NoError(t, ub.Unmarshal(encoded, &values))
	}
	eval := func(expr lang.Expr, binds []lang.EachBinding) (any, error) {
		ctx := &runtime.EvalContext{Inputs: values, MissingAsNull: true}
		runtime.ApplyBindings(ctx, binds)
		return runtime.Eval(expr, ctx)
	}
	return lang.CheckConstraintEntries(entries, values, eval, lang.DisplayNodeRelative).Messages()
}

func addNativeRBACConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativeRBACSchema()
	require.NoError(t, err)
	library.Constraints = make(map[string][]lang.ConstraintSpec, len(library.Resources))
	for name := range library.Resources {
		library.Constraints["resource."+name] = schema.Resources[name].Constraints
	}
}

func TestNativeRBACMetadataSyntax(t *testing.T) {
	for _, resource := range []string{"role", "role-binding"} {
		for _, test := range []struct {
			name     string
			metadata RoleMetadata
			valid    bool
		}{
			{"missing", RoleMetadata{}, false},
			{"empty name", RoleMetadata{Name: new("")}, false},
			{"empty prefix", RoleMetadata{GenerateName: new("")}, false},
			{"qualified", RoleMetadata{Name: new("system:reader")}, true},
			{"prefix", RoleMetadata{GenerateName: new("system:reader-")}, true},
		} {
			t.Run(resource+"/"+test.name, func(t *testing.T) {
				var input any
				if resource == "role" {
					r := validRole()
					r.Metadata = test.metadata
					input = r
				} else {
					r := validRoleBinding()
					r.Metadata = test.metadata
					input = r
				}
				messages := checkNativeRBACInputs(t, resource, input)
				if test.valid {
					require.Empty(t, messages)
				} else {
					require.NotEmpty(t, messages)
				}
			})
		}
	}
}

func TestNativeRBACMetadataKeysAndValues(t *testing.T) {
	for _, resource := range []string{"role", "role-binding"} {
		for _, test := range []struct {
			name       string
			key        string
			value      string
			annotation bool
			valid      bool
		}{
			{"plain", "app", "", false, true},
			{"qualified", "example.com/Upper_Key.1", "Value_1", false, true},
			{"uppercase label domain", "EXAMPLE.COM/key", "v", false, false},
			{"uppercase annotation domain", "EXAMPLE.COM/KEY", "any value", true, true},
			{"name boundary", strings.Repeat("a", 63), "v", false, true},
			{"long name", strings.Repeat("a", 64), "v", false, false},
			{"domain boundary", strings.Repeat("a", 253) + "/key", "v", false, true},
			{"long domain", strings.Repeat("a", 254) + "/key", "v", false, false},
			{"leading domain punctuation", "-example/key", "v", false, false},
			{"trailing domain punctuation", "example-/key", "v", false, false},
			{"empty domain", "/key", "v", false, false},
			{"empty name", "example.com/", "v", false, false},
			{"second slash", "example.com/key/part", "v", false, false},
			{"leading name punctuation", "_key", "v", false, false},
			{"trailing name punctuation", "key_", "v", false, false},
			{"value boundary", "key", strings.Repeat("a", 63), false, true},
			{"long value", "key", strings.Repeat("a", 64), false, false},
			{"bad value", "key", "bad value", false, false},
			{"empty key", "", "v", true, false},
			{"annotation Kelvin", "K.EXAMPLE/K", "v", true, true},
			{"annotation dotted I", "İ.EXAMPLE/İ", "v", true, true},
			{"annotation long s", "example/ſ", "v", true, false},
			{"annotation lowercase length", "example/" + strings.Repeat("K", 63), "v", true, true},
		} {
			t.Run(resource+"/"+test.name, func(t *testing.T) {
				metadata := RoleMetadata{Name: new("reader")}
				values := map[string]string{test.key: test.value}
				if test.annotation {
					metadata.Annotations = &values
				} else {
					metadata.Labels = &values
				}
				var input any
				if resource == "role" {
					r := validRole()
					r.Metadata = metadata
					input = r
				} else {
					r := validRoleBinding()
					r.Metadata = metadata
					input = r
				}
				messages := checkNativeRBACInputs(t, resource, input)
				if test.valid {
					require.Empty(t, messages)
				} else {
					require.NotEmpty(t, messages)
					require.Contains(t, strings.Join(messages, " "), "metadata.")
				}
			})
		}
	}
}
