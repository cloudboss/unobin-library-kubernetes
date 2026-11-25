package v1

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

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
