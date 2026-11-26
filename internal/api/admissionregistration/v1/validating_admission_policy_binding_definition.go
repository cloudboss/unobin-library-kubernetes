package v1

import (
	"fmt"
	"maps"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ValidatingAdmissionPolicyBindingResource) ResourceDefinition() runtime.ResourceDefinition[
	ValidatingAdmissionPolicyBindingResource, *ValidatingAdmissionPolicyBindingResourceOutput,
	*config.Configuration,
] {
	return runtime.ResourceDefinition[
		ValidatingAdmissionPolicyBindingResource, *ValidatingAdmissionPolicyBindingResourceOutput,
		*config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ ValidatingAdmissionPolicyBindingResource,
			out *ValidatingAdmissionPolicyBindingResourceOutput,
		) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing %s server or UID", bindingKind)
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[ValidatingAdmissionPolicyBindingResource]{
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingAdmissionPolicyBindingResource,
			) *BindingSpec {
				return &r.Spec
			}), func(a, b BindingSpec) bool {
				return reflect.DeepEqual(projectBindingSpec(nativeBindingSpec(a)),
					projectBindingSpec(nativeBindingSpec(b)))
			}),
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingAdmissionPolicyBindingResource,
			) **map[string]string {
				return &r.Metadata.Labels
			}), func(a, b *map[string]string) bool {
				return maps.Equal(policyValue(a), policyValue(b))
			}),
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingAdmissionPolicyBindingResource,
			) **map[string]string {
				return &r.Metadata.Annotations
			}), func(a, b *map[string]string) bool {
				return maps.Equal(policyValue(a), policyValue(b))
			}),
		},
		Replace: runtime.Replacement[
			ValidatingAdmissionPolicyBindingResource, *ValidatingAdmissionPolicyBindingResourceOutput,
			*config.Configuration,
		]{
			Fields: []runtime.AnyInputField[ValidatingAdmissionPolicyBindingResource]{
				runtime.InputField(func(r *ValidatingAdmissionPolicyBindingResource) **string {
					return &r.Metadata.Name
				}),
				runtime.InputField(func(r *ValidatingAdmissionPolicyBindingResource) **string {
					return &r.Metadata.GenerateName
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.CurrentContext }),
			},
		},
	}
}
