package v1

import (
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *IngressResource) ResourceDefinition() runtime.ResourceDefinition[
	IngressResource, *IngressResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[IngressResource, *IngressResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		StableID: func(_ IngressResource, out *IngressResourceOutput) (string, error) {
			if err := requireIngressIdentity(out); err != nil {
				return "", err
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[IngressResource]{
			runtime.EqualBy(runtime.InputField(func(r *IngressResource) *IngressMetadata {
				return &r.Metadata
			}), equalMetadata),
			runtime.EqualBy(runtime.InputField(func(r *IngressResource) *IngressSpec {
				return &r.Spec
			}), equalSpec),
			runtime.EqualBy(runtime.InputField(func(r *IngressResource) **bool {
				return &r.WaitForLoadBalancer
			}), func(a, b *bool) bool { return value(a) == value(b) }),
		},
		Replace: runtime.Replacement[IngressResource, *IngressResourceOutput, *config.Configuration]{
			Rules: []runtime.ReplacementRule[IngressResource]{
				runtime.ReplaceWhen(runtime.InputField(func(r *IngressResource) *IngressMetadata {
					return &r.Metadata
				}), replaceMetadata),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func namespace(m IngressMetadata) string {
	if m.Namespace == nil {
		return "default"
	}
	return *m.Namespace
}

func replaceMetadata(a, b IngressMetadata) bool {
	return (b.Name != nil && value(a.Name) != *b.Name) ||
		value(a.GenerateName) != value(b.GenerateName) || namespace(a) != namespace(b)
}

func equalMetadata(a, b IngressMetadata) bool {
	return !replaceMetadata(a, b) && maps.Equal(value(a.Labels), value(b.Labels)) &&
		maps.Equal(value(a.Annotations), value(b.Annotations))
}

func equalSpec(a, b IngressSpec) bool {
	return reflect.DeepEqual(expandIngressSpec(a), expandIngressSpec(b))
}

func duration(p *string, field string) (time.Duration, error) {
	if p == nil {
		return 20 * time.Minute, nil
	}
	d, err := time.ParseDuration(*p)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", field, err)
	}
	return d, nil
}
