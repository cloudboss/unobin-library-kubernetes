package v1

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *RoleResource) ResourceDefinition() runtime.ResourceDefinition[
	RoleResource, *RoleResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *RoleResource) *RoleMetadata { return &r.Metadata })
	return runtime.ResourceDefinition[RoleResource, *RoleResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		StableID: func(_ RoleResource, output *RoleResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.UID == "" {
				return "", fmt.Errorf("missing Role server or UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[RoleResource]{
			runtime.EqualBy(metadata, func(before, after RoleMetadata) bool {
				left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
				if after.Name == nil {
					right.Name = left.Name
				}
				return reflect.DeepEqual(left, right)
			}),
			runtime.EqualBy(runtime.InputField(func(r *RoleResource) *[]RoleRule {
				return &r.Rule
			}),
				func(before, after []RoleRule) bool {
					return reflect.DeepEqual(normalizeRoleRules(before), normalizeRoleRules(after))
				}),
			runtime.EqualBy(runtime.InputField(func(r *RoleResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalRoleLists),
			runtime.EqualBy(runtime.InputField(func(r *RoleResource) **[]string {
				return &r.IgnoreLabels
			}), equalRoleLists),
		},
		Replace: runtime.Replacement[RoleResource, *RoleResourceOutput, *config.Configuration]{
			Rules: []runtime.ReplacementRule[RoleResource]{
				runtime.ReplaceWhen(metadata, func(before, after RoleMetadata) bool {
					left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
					return (after.Name != nil && left.Name != right.Name) ||
						left.Namespace != right.Namespace || left.GenerateName != right.GenerateName
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

func normalizeRoleMetadata(input RoleMetadata) RoleMetadataOutput {
	namespace := "default"
	if input.Namespace != nil {
		namespace = *input.Namespace
	}
	return RoleMetadataOutput{
		Name: roleValue(input.Name), GenerateName: roleValue(input.GenerateName),
		Namespace:   namespace,
		Annotations: roleMap(input.Annotations), Labels: roleMap(input.Labels),
	}
}

func roleValue[T any](input *T) T {
	if input == nil {
		var zero T
		return zero
	}
	return *input
}

func roleMap(input *map[string]string) map[string]string {
	values := roleValue(input)
	if len(values) == 0 {
		return map[string]string{}
	}
	return maps.Clone(values)
}

func equalRoleLists(before, after *[]string) bool {
	return slices.Equal(roleValue(before), roleValue(after))
}
