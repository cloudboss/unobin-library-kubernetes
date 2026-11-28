package v1

import (
	"context"
	"maps"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *LeaseResource) ResourceDefinition() runtime.ResourceDefinition[
	LeaseResource, *LeaseResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *LeaseResource) *LeaseMetadata { return &r.Metadata })
	return runtime.ResourceDefinition[LeaseResource, *LeaseResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, r LeaseResource, _ *config.Configuration) error {
			_, err := prepareLease(r)
			return err
		},
		StableID: func(_ LeaseResource, out *LeaseResourceOutput) (string, error) {
			if err := validLeaseIdentity(out); err != nil {
				return "", err
			}
			return out.Server + "/" + out.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[LeaseResource]{
			runtime.EqualBy(metadata, func(a, b LeaseMetadata) bool {
				return !replaceLease(a, b) && maps.Equal(ptr.Deref(a.Labels, nil), ptr.Deref(b.Labels, nil)) &&
					maps.Equal(ptr.Deref(a.Annotations, nil), ptr.Deref(b.Annotations, nil))
			}),
			runtime.EqualBy(runtime.InputField(func(r *LeaseResource) **LeaseSpec { return &r.Spec }),
				func(a, b *LeaseSpec) bool {
					return reflect.DeepEqual(ptr.Deref(a, LeaseSpec{}), ptr.Deref(b, LeaseSpec{}))
				}),
		},
		Replace: runtime.Replacement[LeaseResource, *LeaseResourceOutput, *config.Configuration]{
			Rules: []runtime.ReplacementRule[LeaseResource]{runtime.ReplaceWhen(metadata, replaceLease)},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.CurrentContext }),
			},
		},
	}
}

func replaceLease(a, b LeaseMetadata) bool {
	return ptr.Deref(a.Name, "") != ptr.Deref(b.Name, "") ||
		ptr.Deref(a.GenerateName, "") != ptr.Deref(b.GenerateName, "") ||
		ptr.Deref(a.Namespace, "default") != ptr.Deref(b.Namespace, "default")
}

func leaseConfigChanged(a, b *config.Configuration) bool {
	before, after := ptr.Deref(a, config.Configuration{}), ptr.Deref(b, config.Configuration{})
	return !reflect.DeepEqual(before.Server, after.Server) ||
		!reflect.DeepEqual(before.Kubeconfig, after.Kubeconfig) ||
		!reflect.DeepEqual(before.CurrentContext, after.CurrentContext)
}
