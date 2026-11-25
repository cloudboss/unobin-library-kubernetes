package v1

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func visitFields(
	v reflect.Value, prefix string, visit func(reflect.StructField, reflect.Value, string),
) {
	v = indirect(v)
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			field, value := v.Type().Field(i), v.Field(i)
			key := prefix + "." + field.Name
			visit(field, value, key)
			visitFields(value, key, visit)
		}
	case reflect.Slice:
		for i := range v.Len() {
			visitFields(v.Index(i), fmt.Sprintf("%s[%d]", prefix, i), visit)
		}
	}
}

func TestQuantityModeSetSemantics(t *testing.T) {
	for _, mode := range []string{"0", "00", "0644", "0777"} {
		r := deployment()
		r.Spec.Template.Spec.Volume = new([]Volume{{
			Name: new("data"), ConfigMap: &ConfigMapVolume{DefaultMode: &mode},
		}})
		obj, err := r.expand(nil)
		require.NoError(t, err)
		obj.UID = "uid"
		observed, err := r.output(obj, "server")
		require.NoError(t, err)
		want := mode
		if mode == "0" {
			want = "00"
		}
		require.Equal(t, want, *(*observed.Spec.Template.Spec.Volume)[0].ConfigMap.DefaultMode)
	}
	for _, mode := range []string{"644", "0788", "01000", "999999999999999999999"} {
		r := deployment()
		r.Spec.Template.Spec.Volume = new([]Volume{{ConfigMap: &ConfigMapVolume{DefaultMode: &mode}}})
		require.NotEmpty(t, checkNativeDeploymentInputs(t, r))
	}
	a, b := fullInput(t), fullInput(t)
	(*a.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{"cpu": "0.1"})
	(*b.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{"cpu": "100m"})
	a.Spec.Selector.MatchExpressions = new([]SelectorRequirement{{
		Values: new([]string{"b", "a", "a"}),
	}})
	b.Spec.Selector.MatchExpressions = new([]SelectorRequirement{{
		Values: new([]string{"a", "b"}),
	}})
	require.True(t, equivalentSpec(a.Spec, b.Spec))
	(*b.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{"cpu": "200m"})
	require.False(t, equivalentSpec(a.Spec, b.Spec))
	a, b = fullInput(t), fullInput(t)
	(*a.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Limits =
		new(map[string]string{"storage": "1Gi"})
	(*b.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Limits =
		new(map[string]string{"storage": "1024Mi"})
	require.True(t, equivalentSpec(a.Spec, b.Spec))
	require.Equal(t, replacementValues(reflect.ValueOf(a.Spec), ""),
		replacementValues(reflect.ValueOf(b.Spec), ""))
}
