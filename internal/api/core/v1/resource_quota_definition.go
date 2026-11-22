package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ResourceQuotaResource) ResourceDefinition() runtime.ResourceDefinition[
	ResourceQuotaResource, *ResourceQuotaResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *ResourceQuotaResource) *ServiceMetadata {
		return &r.Metadata
	})
	spec := runtime.InputField(func(r *ResourceQuotaResource) **ResourceQuotaSpec {
		return &r.Spec
	})
	return runtime.ResourceDefinition[
		ResourceQuotaResource, *ResourceQuotaResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(
			_ context.Context, r ResourceQuotaResource, _ *config.Configuration,
		) error {
			_, _, _, err := r.prepare()
			return err
		},
		StableID: func(_ ResourceQuotaResource, out *ResourceQuotaResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing ResourceQuota server or UID")
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[ResourceQuotaResource]{
			runtime.EqualBy(metadata, func(a, b ServiceMetadata) bool {
				return serviceIdentity(a) == serviceIdentity(b) &&
					maps.Equal(serviceMap(a.Labels), serviceMap(b.Labels)) &&
					maps.Equal(serviceMap(a.Annotations), serviceMap(b.Annotations))
			}),
			runtime.EqualBy(spec, equalQuotaInputSpecs),
			runtime.EqualBy(runtime.InputField(func(r *ResourceQuotaResource) **string {
				return &r.CreateTimeout
			}), equalQuotaTimeouts),
			runtime.EqualBy(runtime.InputField(func(r *ResourceQuotaResource) **string {
				return &r.UpdateTimeout
			}), equalQuotaTimeouts),
			runtime.EqualBy(runtime.InputField(func(r *ResourceQuotaResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *ResourceQuotaResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		Replace: runtime.Replacement[
			ResourceQuotaResource, *ResourceQuotaResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ResourceQuotaResource]{
				runtime.ReplaceWhen(metadata, func(a, b ServiceMetadata) bool {
					return serviceIdentity(a) != serviceIdentity(b)
				}),
				runtime.ReplaceWhen(spec, replaceQuotaScopes),
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

func replaceQuotaScopes(a, b *ResourceQuotaSpec) bool {
	before, after := serviceValue(a), serviceValue(b)
	return !slices.Equal(pvcSet(serviceValue(before.Scopes)), pvcSet(serviceValue(after.Scopes)))
}

func equalQuotaInputSpecs(a, b *ResourceQuotaSpec) bool {
	left, leftErr := prepareQuotaSpec(a)
	right, rightErr := prepareQuotaSpec(b)
	if leftErr != nil || rightErr != nil {
		return reflect.DeepEqual(a, b)
	}
	return quotaHardEqual(left.Hard, right.Hard) && slices.Equal(left.Scopes, right.Scopes) &&
		reflect.DeepEqual(left.ScopeSelector, right.ScopeSelector)
}

func quotaTimeout(value *string) (time.Duration, error) {
	return time.ParseDuration(serviceDefault(value, "1m"))
}

func equalQuotaTimeouts(a, b *string) bool {
	left, leftErr := quotaTimeout(a)
	right, rightErr := quotaTimeout(b)
	if leftErr != nil || rightErr != nil {
		return reflect.DeepEqual(a, b)
	}
	return left == right
}
