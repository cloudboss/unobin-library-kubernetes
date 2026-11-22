package v1

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func visitFields(
	v reflect.Value, prefix string, visit func(reflect.StructField, reflect.Value, string, string),
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
			visit(field, value, key, v.Type().Name()+"."+field.Name)
			visitFields(value, key, visit)
		}
	case reflect.Slice:
		for i := range v.Len() {
			visitFields(v.Index(i), fmt.Sprintf("%s[%d]", prefix, i), visit)
		}
	}
}

var invalidJobFields = map[string][]any{
	"PodSpec.ActiveDeadlineSeconds":                   {int64(-1)},
	"PodSpec.DNSPolicy":                               {"invalid", ""},
	"PodSpec.RestartPolicy":                           {"invalid", ""},
	"PodSpec.TerminationGracePeriodSeconds":           {int64(-1)},
	"PodSecurityContext.FSGroupChangePolicy":          {"invalid", ""},
	"SeccompProfile.Type":                             {"invalid", ""},
	"PodOS.Name":                                      {"invalid", ""},
	"Toleration.Effect":                               {"invalid", ""},
	"Toleration.Operator":                             {"invalid", ""},
	"TopologySpreadConstraint.MaxSkew":                {int64(-1)},
	"TopologySpreadConstraint.MinDomains":             {int64(-1)},
	"TopologySpreadConstraint.NodeAffinityPolicy":     {"invalid", ""},
	"TopologySpreadConstraint.NodeTaintsPolicy":       {"invalid", ""},
	"TopologySpreadConstraint.WhenUnsatisfiable":      {"invalid", ""},
	"NodeExpression.Operator":                         {"invalid", ""},
	"Container.RestartPolicy":                         {"invalid", ""},
	"Container.TerminationMessagePolicy":              {"invalid", ""},
	"Port.Protocol":                                   {"invalid", ""},
	"HTTPGet.Scheme":                                  {"invalid", ""},
	"Probe.FailureThreshold":                          {int64(-1)},
	"Probe.PeriodSeconds":                             {int64(-1)},
	"Probe.SuccessThreshold":                          {int64(-1)},
	"Probe.TimeoutSeconds":                            {int64(-1)},
	"Mount.MountPropagation":                          {"invalid", ""},
	"EmptyDirVolume.Medium":                           {"invalid"},
	"ProjectedVolume.Sources":                         {int64(-1)},
	"ServiceAccountTokenProjection.ExpirationSeconds": {int64(-1)},
	"ClaimSpec.AccessModes":                           {"invalid", ""},
	"ClaimSpec.VolumeMode":                            {"invalid", ""},
	"HostPathVolume.Type":                             {"invalid"},
	"JobSpec.ActiveDeadlineSeconds":                   {int64(-1)},
	"JobSpec.BackoffLimit":                            {int64(-1)},
	"JobSpec.BackoffLimitPerIndex":                    {int64(-1)},
	"JobSpec.Completions":                             {int64(-1)},
	"JobSpec.CompletionMode":                          {"invalid", ""},
	"JobSpec.MaxFailedIndexes":                        {int64(-1)},
	"JobSpec.Parallelism":                             {int64(-1)},
	"PodFailurePolicy.Rule":                           {int64(-1)},
	"ExitCodes.Values":                                {int64(-1)},
}

func TestValidationMatrix(t *testing.T) {
	type invalid struct {
		path   string
		value  any
		parser bool
	}
	var cases []invalid
	visitFields(reflect.ValueOf(fullInput(t)).Elem(), "", func(
		field reflect.StructField, value reflect.Value, key, model string,
	) {
		for _, value := range invalidJobFields[model] {
			cases = append(cases, invalid{path: key, value: value})
		}
		switch field.Tag.Get("format") {
		case "int32", "int64", "mode", "threshold", "quantity", "quantities":
			cases = append(cases, invalid{
				path: key, value: "malformed", parser: strings.HasPrefix(field.Tag.Get("format"),
					"quantit") || strings.HasPrefix(field.Tag.Get("format"), "int")})
			if field.Tag.Get("format") == "mode" || field.Tag.Get("format") == "quantity" {
				if field.Tag.Get("omit") != "true" {
					cases = append(cases, invalid{
						path: key, value: "", parser: field.Tag.Get("format") == "quantity"})
				}
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
			cases = append(cases, invalid{path: key, value: int64(65536)}, invalid{path: key,
				value: int64(0)})
		case "port", "port-name":
			cases = append(cases, invalid{path: key, value: "invalid_port"}, invalid{path: key, value: ""})
		}
	})
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s=%v", tc.path, tc.value), func(t *testing.T) {
			r := fullInput(t)
			visitFields(reflect.ValueOf(r).Elem(), "", func(
				field reflect.StructField, value reflect.Value, key, model string,
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
			if tc.parser {
				_, err := r.expand(nil)
				require.Error(t, err)
			} else {
				require.NotEmpty(t, checkNativeJobInputs(t, r))
			}
		})
	}
}

func TestQuantityModeSetSemantics(t *testing.T) {
	for _, mode := range []string{"0", "00", "0644", "0777"} {
		r := job()
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
		r := job()
		r.Spec.Template.Spec.Volume = new([]Volume{{ConfigMap: &ConfigMapVolume{DefaultMode: &mode}}})
		require.NotEmpty(t, checkNativeJobInputs(t, r))
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
	require.False(t, immutableChanged(a.Spec, b.Spec))
}
