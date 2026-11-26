package v1

import (
	"context"
	"maps"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *CustomResourceDefinitionResource) ResourceDefinition() runtime.ResourceDefinition[
	CustomResourceDefinitionResource, *CustomResourceDefinitionResourceOutput, *config.Configuration,
] {
	group := runtime.InputField(func(r *CustomResourceDefinitionResource) *string {
		return &r.Spec.Group
	})
	plural := runtime.InputField(func(r *CustomResourceDefinitionResource) *string {
		return &r.Spec.Names.Plural
	})
	kind := runtime.InputField(func(r *CustomResourceDefinitionResource) *string {
		return &r.Spec.Names.Kind
	})
	scope := runtime.InputField(func(r *CustomResourceDefinitionResource) *string {
		return &r.Spec.Scope
	})
	return runtime.ResourceDefinition[
		CustomResourceDefinitionResource, *CustomResourceDefinitionResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(
			_ context.Context, input CustomResourceDefinitionResource, _ *config.Configuration,
		) error {
			_, err := prepareCRD(input)
			return err
		},
		StableID: func(
			_ CustomResourceDefinitionResource, out *CustomResourceDefinitionResourceOutput,
		) (string, error) {
			if err := validCRDIdentity(out); err != nil {
				return "", err
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[CustomResourceDefinitionResource]{
			runtime.EqualBy(runtime.InputField(func(
				r *CustomResourceDefinitionResource,
			) **map[string]string {
				return &r.Metadata.Labels
			}), equalCRDMaps),
			runtime.EqualBy(runtime.InputField(func(
				r *CustomResourceDefinitionResource,
			) **map[string]string {
				return &r.Metadata.Annotations
			}), equalCRDMaps),
			runtime.EqualBy(runtime.InputField(func(
				r *CustomResourceDefinitionResource,
			) **bool {
				return &r.WaitForEstablished
			}), func(a, b *bool) bool {
				return ptr.Deref(a, true) == ptr.Deref(b, true)
			}),
		},
		Replace: runtime.Replacement[
			CustomResourceDefinitionResource, *CustomResourceDefinitionResourceOutput,
			*config.Configuration,
		]{
			Fields: []runtime.AnyInputField[CustomResourceDefinitionResource]{
				group, plural, kind, scope,
				runtime.InputField(func(r *CustomResourceDefinitionResource) *string {
					return &r.Metadata.Name
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
			Drift: []runtime.DriftRule[*CustomResourceDefinitionResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(func(
					out *CustomResourceDefinitionResourceOutput,
				) *string {
					return &out.Spec.Group
				}), runtime.Changed[string]),
				runtime.ReplaceOnDrift(runtime.OutputField(func(
					out *CustomResourceDefinitionResourceOutput,
				) *string {
					return &out.Spec.Names.Plural
				}), runtime.Changed[string]),
				runtime.ReplaceOnDrift(runtime.OutputField(func(
					out *CustomResourceDefinitionResourceOutput,
				) *string {
					return &out.Spec.Names.Kind
				}), runtime.Changed[string]),
				runtime.ReplaceOnDrift(runtime.OutputField(func(
					out *CustomResourceDefinitionResourceOutput,
				) *string {
					return &out.Spec.Scope
				}), runtime.Changed[string]),
			},
		},
	}
}

func crdImmutableChanged(a, b CRDSpec) bool {
	return a.Group != b.Group || a.Names.Plural != b.Names.Plural ||
		a.Names.Kind != b.Names.Kind || a.Scope != b.Scope
}

func equalCRDMaps(a, b *map[string]string) bool {
	return maps.Equal(ptr.Deref(a, nil), ptr.Deref(b, nil))
}
