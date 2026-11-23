package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *LimitRangeResource) ResourceDefinition() runtime.ResourceDefinition[
	LimitRangeResource, *LimitRangeResourceOutput, *config.Configuration,
] {
	objectField := runtime.InputField(func(r *LimitRangeResource) *LimitRangeObject {
		return &r.Object
	})
	return runtime.ResourceDefinition[
		LimitRangeResource, *LimitRangeResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, r LimitRangeResource, _ *config.Configuration) error {
			_, err := r.prepare()
			return err
		},
		Equality: []runtime.InputEqualityRule[LimitRangeResource]{
			runtime.EqualBy(objectField, equalLimitRangeObjects),
			runtime.EqualBy(runtime.InputField(func(r *LimitRangeResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *LimitRangeResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		StableID: func(_ LimitRangeResource, out *LimitRangeResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing LimitRange server or UID")
			}
			return out.Server + "/" + out.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			LimitRangeResource, *LimitRangeResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[LimitRangeResource]{
				runtime.ReplaceWhen(objectField, func(before, after LimitRangeObject) bool {
					return limitRangeIdentity(before.Metadata) != limitRangeIdentity(after.Metadata)
				}),
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
