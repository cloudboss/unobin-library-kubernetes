package v1

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

var nativeDeploymentSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/apps/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

var nativeDeploymentRules = sync.OnceValues(func() ([]lang.ConstraintEntry, error) {
	schema, err := nativeDeploymentSchema()
	if err != nil {
		return nil, err
	}
	entries, errors := lang.ParseSpecs(schema.Resources["deployment"].Constraints)
	return entries, errors.Err()
})

func checkNativeDeploymentInputs(t *testing.T, input any) []string {
	t.Helper()
	entries, err := nativeDeploymentRules()
	require.NoError(t, err)
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

func TestNativeDeploymentNullableIntegers(t *testing.T) {
	values := []string{
		"", "0", "-0", "+0", "00", "0000000000000000000000000000000000000000001",
		"1", "-1", "+1", "2147483648", "-2147483649", "9223372036854775807",
		"-9223372036854775808", "+9223372036854775807", "0009223372036854775807",
		"-0009223372036854775808", "9223372036854775808", "-9223372036854775809",
		"+9223372036854775808", "18446744073709551616", "-18446744073709551616",
		"malformed", "1.0", "1e1", "1_0", " 1", "1 ", "+", "-", "１２", "0x10",
	}
	random := rand.New(rand.NewPCG(11, 19))
	for range 30 {
		value := strconv.FormatUint(random.Uint64(), 10)
		values = append(values, value, "-"+value)
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			r := deployment()
			r.Spec.Replicas = &value
			_, err := strconv.ParseInt(value, 10, 64)
			valid := value == "" || err == nil
			messages := checkNativeDeploymentInputs(t, r)
			require.Equal(t, valid, len(messages) == 0, "%v", messages)
		})
	}
	for _, field := range []string{
		"pod.fs-group", "pod.run-as-group", "pod.run-as-user", "container.run-as-group",
		"container.run-as-user", "init-container.run-as-group", "init-container.run-as-user",
		"toleration-seconds",
	} {
		for _, value := range []string{"", "-0001", "9223372036854775808", "malformed"} {
			t.Run(field+"="+value, func(t *testing.T) {
				r := deployment()
				setDeploymentInteger(r, field, &value)
				_, err := strconv.ParseInt(value, 10, 64)
				valid := value == "" || err == nil
				require.Equal(t, valid, len(checkNativeDeploymentInputs(t, r)) == 0)
			})
		}
	}
}

func setDeploymentInteger(r *DeploymentResource, field string, value *string) {
	pod := &r.Spec.Template.Spec
	if strings.HasPrefix(field, "pod.") {
		pod.SecurityContext = &PodSecurityContext{}
		switch field {
		case "pod.fs-group":
			pod.SecurityContext.FSGroup = value
		case "pod.run-as-group":
			pod.SecurityContext.RunAsGroup = value
		case "pod.run-as-user":
			pod.SecurityContext.RunAsUser = value
		}
		return
	}
	if field == "toleration-seconds" {
		pod.Toleration = new([]Toleration{{TolerationSeconds: value}})
		return
	}
	container := Container{Name: "test", SecurityContext: &ContainerSecurityContext{}}
	if strings.HasSuffix(field, "run-as-group") {
		container.SecurityContext.RunAsGroup = value
	} else {
		container.SecurityContext.RunAsUser = value
	}
	if strings.HasPrefix(field, "init-container.") {
		pod.InitContainer = new([]Container{container})
	} else {
		pod.Container = new([]Container{container})
	}
}

func TestQuantityParserMapping(t *testing.T) {
	var paths []string
	visitFields(reflect.ValueOf(fullInput(t)), "", func(
		field reflect.StructField, _ reflect.Value, path string,
	) {
		if field.Tag.Get("format") == "quantity" {
			paths = append(paths, path)
		}
	})
	require.NotEmpty(t, paths)
	for _, test := range []struct {
		text  string
		valid bool
	}{
		{"+", true}, {"-", true}, {".", true}, {"n", true}, {"Ki", true},
		{"Pi", false}, {"Ei", false}, {"e-9", true}, {"e-10", false},
		{"e-4294967305", true}, {"e-4294967306", false},
		{"e9223372036854775807", true}, {"e9223372036854775808", false},
		{"e-9223372036854775808", true}, {"e-9223372036854775809", false},
		{"", false}, {"0", true}, {"1.5Gi", true}, {".5m", true},
		{"1.1.1", false}, {"1K", false}, {"1e", false},
	} {
		t.Run(test.text, func(t *testing.T) {
			for _, path := range paths {
				t.Run(path, func(t *testing.T) {
					r := fullInput(t)
					visitFields(reflect.ValueOf(r), "", func(
						_ reflect.StructField, value reflect.Value, key string,
					) {
						if key == path {
							value.Set(reflect.ValueOf(&test.text))
						}
					})
					_, err := r.expand(nil)
					require.Equal(t, test.valid, err == nil, "%v", err)
				})
			}
		})
	}
}

func addNativeDeploymentConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativeDeploymentSchema()
	require.NoError(t, err)
	library.Constraints = map[string][]lang.ConstraintSpec{
		"resource.deployment": schema.Resources["deployment"].Constraints,
	}
}

func TestNativeDeploymentSyntax(t *testing.T) {
	for _, change := range []struct {
		name string
		set  func(*DeploymentResource)
	}{
		{"name", func(r *DeploymentResource) { r.Metadata.Name = new("BAD") }},
		{"label", func(r *DeploymentResource) {
			r.Spec.Template.Metadata.Labels = new(map[string]string{"bad key": "value"})
		}},
		{"IP", func(r *DeploymentResource) {
			r.Spec.Template.Spec.HostAliases = new([]HostAlias{{IP: "bad", Hostnames: []string{}}})
		}},
		{"ignore regex", func(r *DeploymentResource) { r.IgnoreLabels = new([]string{"["}) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			r := deployment()
			change.set(r)
			require.NotEmpty(t, checkNativeDeploymentInputs(t, r))
		})
	}
}

func TestNativeDeploymentIdentityAndMetadata(t *testing.T) {
	for _, test := range []struct {
		prefix string
		valid  bool
	}{
		{"example--", true}, {"A-", true}, {"example.-", true},
		{strings.Repeat("a", 63) + "-", true},
		{"", false}, {"-", false}, {"example.ü-", false},
		{strings.Repeat("a", 64) + "-", false},
	} {
		t.Run("prefix="+test.prefix, func(t *testing.T) {
			r := deployment()
			r.Metadata.Name = nil
			r.Metadata.GenerateName = &test.prefix
			messages := checkNativeDeploymentInputs(t, r)
			if test.valid {
				require.Empty(t, messages)
			} else {
				require.NotEmpty(t, messages)
			}
		})
	}
	r := deployment()
	r.Metadata.Annotations = new(map[string]string{"Kubernetes.İO/key": "unchanged"})
	require.Empty(t, checkNativeDeploymentInputs(t, r))
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Kubernetes.İO/key": "unchanged"}, obj.Annotations)
	r.Metadata.Name = nil
	require.NotEmpty(t, checkNativeDeploymentInputs(t, r))
	r.Metadata.Name, r.Metadata.GenerateName = new("name"), new("prefix-")
	require.NotEmpty(t, checkNativeDeploymentInputs(t, r))
}
