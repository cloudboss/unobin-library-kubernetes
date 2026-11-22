package v1

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDaemonSetValidationMatrix(t *testing.T) {
	type invalid struct {
		path       string
		value      any
		conversion bool
	}
	var cases []invalid
	visitFields(reflect.ValueOf(fullDaemonSetInput(t)).Elem(), "", func(
		field reflect.StructField, value reflect.Value, key string,
	) {
		if choices := field.Tag.Get("enum"); choices != "" {
			cases = append(cases, invalid{path: key, value: "invalid"})
			if !strings.HasPrefix(choices, ",") {
				cases = append(cases, invalid{path: key, value: ""})
			}
		}
		if field.Tag.Get("min") != "" {
			cases = append(cases, invalid{path: key, value: int64(-1)})
		}
		switch field.Tag.Get("format") {
		case "int32", "int64", "mode", "threshold", "quantity", "quantities":
			conversion := field.Tag.Get("format") == "quantity" ||
				field.Tag.Get("format") == "quantities"
			cases = append(cases, invalid{key, "malformed", conversion})
			if field.Tag.Get("format") == "mode" || field.Tag.Get("format") == "quantity" {
				cases = append(cases, invalid{key, "", conversion})
			}
		case "relative-path":
			for _, text := range []string{"/absolute", "a/../b", "..prefix"} {
				cases = append(cases, invalid{path: key, value: text})
			}
		case "ip", "ips":
			cases = append(cases, invalid{path: key, value: "invalid-IP"})
		case "names":
			cases = append(cases, invalid{path: key, value: "INVALID_DNS"})
		case "port-number":
			cases = append(cases, invalid{path: key, value: int64(65536)},
				invalid{path: key, value: int64(0)})
		case "port", "port-name":
			cases = append(cases, invalid{path: key, value: "invalid_port"},
				invalid{path: key, value: ""})
		}
	})
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s=%v", tc.path, tc.value), func(t *testing.T) {
			r := fullDaemonSetInput(t)
			visitFields(reflect.ValueOf(r).Elem(), "", func(
				field reflect.StructField, value reflect.Value, key string,
			) {
				if key != tc.path {
					return
				}
				value = indirect(value)
				switch value.Kind() {
				case reflect.String:
					value.SetString(tc.value.(string))
				case reflect.Int64:
					value.SetInt(tc.value.(int64))
				case reflect.Slice:
					if value.Type().Elem().Kind() == reflect.String {
						value.Index(0).SetString(tc.value.(string))
					} else {
						value.SetLen(0)
					}
				case reflect.Map:
					value.SetMapIndex(reflect.ValueOf("cpu"), reflect.ValueOf(tc.value.(string)))
				}
			})
			if tc.conversion {
				_, err := r.expand(nil)
				require.Error(t, err)
			} else {
				require.NotEmpty(t, checkNativeDaemonSetInputs(t, r))
			}
		})
	}
}

func TestDaemonSetQuantityModeSetSemantics(t *testing.T) {
	for _, mode := range []string{"0", "00", "0644", "0777"} {
		r := daemonSetWithPod()
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
		r := daemonSetWithPod()
		r.Spec.Template.Spec.Volume = new([]Volume{{ConfigMap: &ConfigMapVolume{DefaultMode: &mode}}})
		require.NotEmpty(t, checkNativeDaemonSetInputs(t, r))
	}
	a, b := fullDaemonSetInput(t), fullDaemonSetInput(t)
	(*a.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{"cpu": "0.1"})
	(*b.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{"cpu": "100m"})
	a.Spec.Selector.MatchExpressions = new([]SelectorRequirement{{
		Values: new([]string{"b", "a", "a"}),
	}})
	b.Spec.Selector.MatchExpressions = new([]SelectorRequirement{{
		Values: new([]string{"a", "b"}),
	}})
	require.True(t, equivalentDaemonSetSpec(a.Spec, b.Spec))
	(*b.Spec.Template.Spec.Container)[0].Resources.Requests = new(map[string]string{"cpu": "200m"})
	require.False(t, equivalentDaemonSetSpec(a.Spec, b.Spec))
	a, b = fullDaemonSetInput(t), fullDaemonSetInput(t)
	(*a.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Limits =
		new(map[string]string{"storage": "1Gi"})
	(*b.Spec.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Limits =
		new(map[string]string{"storage": "1024Mi"})
	require.True(t, equivalentDaemonSetSpec(a.Spec, b.Spec))
	require.Equal(t, replacementValues(reflect.ValueOf(a.Spec), ""),
		replacementValues(reflect.ValueOf(b.Spec), ""))
}
