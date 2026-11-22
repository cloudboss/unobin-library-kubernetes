package v1

import (
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ClusterRoleResource) ResourceDefinition() runtime.ResourceDefinition[
	ClusterRoleResource, *ClusterRoleResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *ClusterRoleResource) *ClusterRoleMetadata {
		return &r.Metadata
	})
	return runtime.ResourceDefinition[
		ClusterRoleResource, *ClusterRoleResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ ClusterRoleResource, output *ClusterRoleResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.UID == "" {
				return "", fmt.Errorf("missing ClusterRole server or UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[ClusterRoleResource]{
			runtime.EqualBy(metadata, func(before, after ClusterRoleMetadata) bool {
				left, right := normalizeClusterRoleMetadata(before), normalizeClusterRoleMetadata(after)
				if after.Name == nil {
					right.Name = left.Name
				}
				return reflect.DeepEqual(left, right)
			}),
			runtime.EqualBy(runtime.InputField(func(r *ClusterRoleResource) **[]ClusterRoleRule {
				return &r.Rule
			}),
				func(before, after *[]ClusterRoleRule) bool {
					return reflect.DeepEqual(normalizeClusterRoleRules(before),
						normalizeClusterRoleRules(after))
				}),
			runtime.EqualBy(runtime.InputField(func(r *ClusterRoleResource) **ClusterRoleAggregation {
				return &r.AggregationRule
			}), func(before, after *ClusterRoleAggregation) bool {
				return reflect.DeepEqual(normalizeClusterRoleAggregation(before),
					normalizeClusterRoleAggregation(after))
			}),
			runtime.EqualBy(runtime.InputField(func(r *ClusterRoleResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalRoleLists),
			runtime.EqualBy(runtime.InputField(func(r *ClusterRoleResource) **[]string {
				return &r.IgnoreLabels
			}), equalRoleLists),
		},
		Replace: runtime.Replacement[
			ClusterRoleResource, *ClusterRoleResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ClusterRoleResource]{
				runtime.ReplaceWhen(metadata, func(before, after ClusterRoleMetadata) bool {
					left, right := normalizeClusterRoleMetadata(before), normalizeClusterRoleMetadata(after)
					return (after.Name != nil && left.Name != right.Name) ||
						left.GenerateName != right.GenerateName
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func normalizeClusterRoleMetadata(input ClusterRoleMetadata) ClusterRoleMetadataOutput {
	return ClusterRoleMetadataOutput{Name: roleValue(input.Name),
		GenerateName: roleValue(input.GenerateName), Annotations: roleMap(input.Annotations),
		Labels: roleMap(input.Labels)}
}
