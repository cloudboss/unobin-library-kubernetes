package v1

import (
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *RoleBindingResource) ResourceDefinition() runtime.ResourceDefinition[
	RoleBindingResource, *RoleBindingResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *RoleBindingResource) *RoleMetadata { return &r.Metadata })
	return runtime.ResourceDefinition[RoleBindingResource, *RoleBindingResourceOutput,
		*config.Configuration]{
		SchemaVersion: 1,
		StableID: func(_ RoleBindingResource, output *RoleBindingResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.UID == "" {
				return "", fmt.Errorf("missing RoleBinding server or UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[RoleBindingResource]{
			runtime.EqualBy(metadata, func(before, after RoleMetadata) bool {
				left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
				if after.Name == nil {
					right.Name = left.Name
				}
				return reflect.DeepEqual(left, right)
			}),
			runtime.EqualBy(runtime.InputField(func(r *RoleBindingResource) *[]RoleBindingSubject {
				return &r.Subject
			}), equalBindingSubjects),
			runtime.EqualBy(runtime.InputField(func(r *RoleBindingResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalRoleLists),
			runtime.EqualBy(runtime.InputField(func(r *RoleBindingResource) **[]string {
				return &r.IgnoreLabels
			}), equalRoleLists),
		},
		Replace: runtime.Replacement[RoleBindingResource, *RoleBindingResourceOutput,
			*config.Configuration]{
			Fields: []runtime.AnyInputField[RoleBindingResource]{
				runtime.InputField(func(r *RoleBindingResource) *RoleBindingRoleRef { return &r.RoleRef }),
			},
			Rules: []runtime.ReplacementRule[RoleBindingResource]{
				runtime.ReplaceWhen(metadata, func(before, after RoleMetadata) bool {
					left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
					return (after.Name != nil && left.Name != right.Name) ||
						left.Namespace != right.Namespace || left.GenerateName != right.GenerateName
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
			Drift: []runtime.DriftRule[*RoleBindingResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(
					func(o *RoleBindingResourceOutput) *RoleBindingRoleRef { return &o.RoleRef },
				), func(before, after RoleBindingRoleRef) bool { return before != after }),
			},
		},
	}
}
