package v1

import (
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ClusterRoleBindingResource) ResourceDefinition() runtime.ResourceDefinition[
	ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *ClusterRoleBindingResource) *ClusterRoleMetadata {
		return &r.Metadata
	})
	return runtime.ResourceDefinition[ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput,
		*config.Configuration]{
		SchemaVersion: 1,
		StableID: func(
			_ ClusterRoleBindingResource, output *ClusterRoleBindingResourceOutput,
		) (string, error) {
			if output == nil || output.Server == "" || output.UID == "" {
				return "", fmt.Errorf("missing ClusterRoleBinding server or UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[ClusterRoleBindingResource]{
			runtime.EqualBy(metadata, func(before, after ClusterRoleMetadata) bool {
				left, right := normalizeClusterRoleMetadata(before), normalizeClusterRoleMetadata(after)
				if after.Name == nil {
					right.Name = left.Name
				}
				return reflect.DeepEqual(left, right)
			}),
			runtime.EqualBy(runtime.InputField(func(
				r *ClusterRoleBindingResource,
			) *[]ClusterRoleBindingSubject {
				return &r.Subject
			}), equalClusterBindingSubjects),
			runtime.EqualBy(runtime.InputField(func(r *ClusterRoleBindingResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalRoleLists),
			runtime.EqualBy(runtime.InputField(func(r *ClusterRoleBindingResource) **[]string {
				return &r.IgnoreLabels
			}), equalRoleLists),
		},
		Replace: runtime.Replacement[ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput,
			*config.Configuration]{
			Fields: []runtime.AnyInputField[ClusterRoleBindingResource]{
				runtime.InputField(func(r *ClusterRoleBindingResource) *ClusterRoleBindingRoleRef {
					return &r.RoleRef
				}),
			},
			Rules: []runtime.ReplacementRule[ClusterRoleBindingResource]{
				runtime.ReplaceWhen(metadata, func(before, after ClusterRoleMetadata) bool {
					left, right := normalizeClusterRoleMetadata(before), normalizeClusterRoleMetadata(after)
					return (after.Name != nil && left.Name != right.Name) ||
						left.GenerateName != right.GenerateName
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
			Drift: []runtime.DriftRule[*ClusterRoleBindingResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(
					func(o *ClusterRoleBindingResourceOutput) *ClusterRoleBindingRoleRef { return &o.RoleRef },
				), func(before, after ClusterRoleBindingRoleRef) bool { return before != after }),
			},
		},
	}
}
