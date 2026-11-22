package v1

import (
	"fmt"
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

var nativeDaemonSetSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/apps/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

var nativeDaemonSetRules = sync.OnceValues(func() ([]lang.ConstraintEntry, error) {
	schema, err := nativeDaemonSetSchema()
	if err != nil {
		return nil, err
	}
	entries, errors := lang.ParseSpecs(schema.Resources["daemon-set"].Constraints)
	return entries, errors.Err()
})

func checkNativeDaemonSetInputs(t *testing.T, input any) []string {
	t.Helper()
	entries, err := nativeDaemonSetRules()
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

func addNativeDaemonSetConstraints(t *testing.T, library *runtime.Library) {
	t.Helper()
	schema, err := nativeDaemonSetSchema()
	require.NoError(t, err)
	library.Constraints = map[string][]lang.ConstraintSpec{
		"resource.daemon-set": schema.Resources["daemon-set"].Constraints,
	}
}

func TestDaemonSetNullableIntegers(t *testing.T) {
	for _, field := range []string{
		"pod.fs-group", "pod.run-as-group", "pod.run-as-user", "container.run-as-group",
		"container.run-as-user", "init-container.run-as-group", "init-container.run-as-user",
		"toleration-seconds",
	} {
		for _, value := range []string{"", "-0001", "9223372036854775808", "malformed"} {
			t.Run(field+"="+value, func(t *testing.T) {
				r := daemonSetWithPod()
				setDaemonSetInteger(r, field, &value)
				_, err := strconv.ParseInt(value, 10, 64)
				valid := value == "" || err == nil
				require.Equal(t, valid, len(checkNativeDaemonSetInputs(t, r)) == 0)
			})
		}
	}
}

func setDaemonSetInteger(r *DaemonSetResource, field string, value *string) {
	pod := r.Spec.Template.Spec
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

func TestDaemonSetNativeSyntax(t *testing.T) {
	for _, change := range []struct {
		name string
		set  func(*DaemonSetResource)
	}{
		{"name", func(r *DaemonSetResource) { r.Metadata.Name = new("BAD") }},
		{"label", func(r *DaemonSetResource) {
			r.Spec.Template.Metadata.Labels = new(map[string]string{"bad key": "value"})
		}},
		{"IP", func(r *DaemonSetResource) {
			r.Spec.Template.Spec.HostAliases = new([]HostAlias{{IP: "bad", Hostnames: []string{}}})
		}},
		{"ignore regex", func(r *DaemonSetResource) { r.IgnoreLabels = new([]string{"["}) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			r := daemonSetWithPod()
			change.set(r)
			require.NotEmpty(t, checkNativeDaemonSetInputs(t, r))
		})
	}
}

func TestDaemonSetNativeIdentityAndMetadata(t *testing.T) {
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
			r := daemonSetWithPod()
			r.Metadata.Name = nil
			r.Metadata.GenerateName = &test.prefix
			messages := checkNativeDaemonSetInputs(t, r)
			if test.valid {
				require.Empty(t, messages)
			} else {
				require.NotEmpty(t, messages)
			}
		})
	}
	r := daemonSetWithPod()
	r.Metadata.Annotations = new(map[string]string{"Kubernetes.İO/key": "unchanged"})
	require.Empty(t, checkNativeDaemonSetInputs(t, r))
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Kubernetes.İO/key": "unchanged"}, obj.Annotations)
	r.Metadata.Name = nil
	require.NotEmpty(t, checkNativeDaemonSetInputs(t, r))
	r.Metadata.Name, r.Metadata.GenerateName = new("name"), new("prefix-")
	require.NotEmpty(t, checkNativeDaemonSetInputs(t, r))
}
