package v1

import (
	"maps"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *IngressClassResource) ResourceDefinition() runtime.ResourceDefinition[
	IngressClassResource, *IngressClassResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		IngressClassResource, *IngressClassResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ IngressClassResource, out *IngressClassResourceOutput) (string, error) {
			if err := requireIngressClassIdentity(out); err != nil {
				return "", err
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[IngressClassResource]{
			runtime.EqualBy(runtime.InputField(func(r *IngressClassResource) *IngressClassMetadata {
				return &r.Metadata
			}), equalIngressClassMetadata),
			runtime.EqualBy(runtime.InputField(func(r *IngressClassResource) *IngressClassSpec {
				return &r.Spec
			}), equalIngressClassSpec),
		},
		Replace: runtime.Replacement[
			IngressClassResource, *IngressClassResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[IngressClassResource]{
				runtime.ReplaceWhen(
					runtime.InputField(func(r *IngressClassResource) *IngressClassMetadata {
						return &r.Metadata
					}), replaceIngressClassMetadata,
				),
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
		},
	}
}

func replaceIngressClassMetadata(a, b IngressClassMetadata) bool {
	return value(a.Name) != value(b.Name) || value(a.GenerateName) != value(b.GenerateName)
}

func equalIngressClassMetadata(a, b IngressClassMetadata) bool {
	return !replaceIngressClassMetadata(a, b) &&
		maps.Equal(value(a.Labels), value(b.Labels)) &&
		maps.Equal(value(a.Annotations), value(b.Annotations))
}

func equalIngressClassSpec(a, b IngressClassSpec) bool {
	return reflect.DeepEqual(expandIngressClassSpec(a), expandIngressClassSpec(b))
}
