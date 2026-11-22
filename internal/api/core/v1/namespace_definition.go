package v1

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *NamespaceResource) ResourceDefinition() runtime.ResourceDefinition[
	NamespaceResource, *NamespaceResourceOutput, *config.Configuration,
] {
	objectField := runtime.InputField(func(r *NamespaceResource) *NamespaceObject {
		return &r.Object
	})
	return runtime.ResourceDefinition[
		NamespaceResource, *NamespaceResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[NamespaceResource]{
			runtime.EqualBy(objectField, func(before, after NamespaceObject) bool {
				return reflect.DeepEqual(normalizeNamespaceMetadata(before.Metadata),
					normalizeNamespaceMetadata(after.Metadata))
			}),
			runtime.EqualBy(runtime.InputField(func(r *NamespaceResource) **bool {
				return &r.WaitForDefaultServiceAccount
			}), func(before, after *bool) bool {
				return ptr.Deref(before, false) == ptr.Deref(after, false)
			}),
			runtime.EqualBy(runtime.InputField(func(r *NamespaceResource) **string {
				return &r.DeleteTimeout
			}), func(before, after *string) bool {
				previous, previousErr := namespaceDeleteDuration(before)
				desired, desiredErr := namespaceDeleteDuration(after)
				if previousErr != nil || desiredErr != nil {
					return configMapValue(before) == configMapValue(after)
				}
				return previous == desired
			}),
			runtime.EqualBy(runtime.InputField(func(r *NamespaceResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalNamespacePatterns),
			runtime.EqualBy(runtime.InputField(func(r *NamespaceResource) **[]string {
				return &r.IgnoreLabels
			}), equalNamespacePatterns),
		},
		StableID: func(_ NamespaceResource, output *NamespaceResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing Namespace server or UID")
			}
			return output.Server + "/" + output.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			NamespaceResource, *NamespaceResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[NamespaceResource]{
				runtime.ReplaceWhen(objectField, func(before, after NamespaceObject) bool {
					previous := normalizeNamespaceMetadata(before.Metadata)
					desired := normalizeNamespaceMetadata(after.Metadata)
					return previous.Name != desired.Name || previous.GenerateName != desired.GenerateName
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

func namespaceDeleteDuration(value *string) (time.Duration, error) {
	if value == nil {
		return 5 * time.Minute, nil
	}
	parsed, err := time.ParseDuration(*value)
	if err != nil {
		return 0, fmt.Errorf("parse delete-timeout: %w", err)
	}
	return parsed, nil
}

func equalNamespacePatterns(before, after *[]string) bool {
	return slices.Equal(ptr.Deref(before, nil), ptr.Deref(after, nil))
}
