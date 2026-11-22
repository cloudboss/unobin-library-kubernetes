package v1

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCronJobValidationMatrix(t *testing.T) {
	type invalid struct {
		path   string
		value  any
		parser bool
	}
	var cases []invalid
	visitFields(reflect.ValueOf(cronJobFullInput(t)).Elem(), "", func(
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
			r := cronJobFullInput(t)
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
				_, err := r.prepare(nil)
				require.Error(t, err)
			} else {
				require.NotEmpty(t, checkNativeCronJobInputs(t, r))
			}
		})
	}
}

func TestCronJobNativeEnumAlternatives(t *testing.T) {
	values := map[string][]string{
		"PodSpec.DNSPolicy": {
			"ClusterFirst", "ClusterFirstWithHostNet", "Default", "None",
		},
		"PodSpec.RestartPolicy":                  {"Always", "OnFailure", "Never"},
		"Container.RestartPolicy":                {"Always"},
		"Container.TerminationMessagePolicy":     {"File", "FallbackToLogsOnError"},
		"Port.Protocol":                          {"TCP", "UDP"},
		"HTTPGet.Scheme":                         {"HTTP", "HTTPS"},
		"PodOS.Name":                             {"linux", "windows"},
		"SeccompProfile.Type":                    {"Localhost", "RuntimeDefault", "Unconfined"},
		"PodSecurityContext.FSGroupChangePolicy": {"Always", "OnRootMismatch"},
		"NodeExpression.Operator": {
			"In", "NotIn", "Exists", "DoesNotExist", "Gt", "Lt",
		},
		"Toleration.Effect":                           {"NoSchedule", "PreferNoSchedule", "NoExecute"},
		"Toleration.Operator":                         {"Equal", "Exists"},
		"TopologySpreadConstraint.NodeAffinityPolicy": {"Honor", "Ignore"},
		"TopologySpreadConstraint.NodeTaintsPolicy":   {"Honor", "Ignore"},
		"TopologySpreadConstraint.WhenUnsatisfiable":  {"DoNotSchedule", "ScheduleAnyway"},
		"Mount.MountPropagation":                      {"None", "HostToContainer", "Bidirectional"},
		"EmptyDirVolume.Medium": {
			"", "Memory", "HugePages", "HugePages-2Mi", "HugePages-1Gi",
		},
		"HostPathVolume.Type": {"", "DirectoryOrCreate", "Directory", "FileOrCreate", "File", "Socket",
			"CharDevice", "BlockDevice"},
		"ClaimSpec.VolumeMode":   {"Block", "Filesystem"},
		"JobSpec.CompletionMode": {"Indexed", "NonIndexed"},
	}
	for model, alternatives := range values {
		for _, alternative := range alternatives {
			t.Run(model+"/"+alternative, func(t *testing.T) {
				r := cronJobFullInput(t)
				found := false
				visitFields(reflect.ValueOf(r).Elem(), "", func(
					_ reflect.StructField, value reflect.Value, _ string, name string,
				) {
					if name == model {
						indirect(value).SetString(alternative)
						found = true
					}
				})
				require.True(t, found)
				require.Empty(t, checkNativeCronJobInputs(t, r))
			})
		}
	}
	for _, policy := range []string{"Allow", "Forbid", "Replace"} {
		r := cronJob()
		r.Spec.ConcurrencyPolicy = &policy
		require.Empty(t, checkNativeCronJobInputs(t, r))
	}
	r := cronJob()
	r.Spec.ConcurrencyPolicy = new("allow")
	require.NotEmpty(t, checkNativeCronJobInputs(t, r))
}
