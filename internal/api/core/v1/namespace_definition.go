package v1

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation"
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
		Validate: func(_ context.Context, input NamespaceResource, _ *config.Configuration) error {
			return input.validateNamespace()
		},
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
				return previousErr == nil && desiredErr == nil && previous == desired
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

func (r *NamespaceResource) validateNamespace() error {
	metadata := r.Object.Metadata
	if metadata.Name != nil && metadata.GenerateName != nil {
		return fmt.Errorf("metadata.name conflicts with metadata.generate-name")
	}
	if metadata.Name != nil {
		if errors := validation.IsDNS1123Subdomain(*metadata.Name); len(errors) > 0 {
			return fmt.Errorf("invalid metadata.name: %s", strings.Join(errors, "; "))
		}
	}
	if metadata.GenerateName != nil {
		if errors := apivalidation.NameIsDNSLabel(*metadata.GenerateName, true); len(errors) > 0 {
			return fmt.Errorf("invalid metadata.generate-name: %s", strings.Join(errors, "; "))
		}
	}
	for key, value := range ptr.Deref(metadata.Labels, nil) {
		if errors := validation.IsQualifiedName(key); len(errors) > 0 {
			return fmt.Errorf("invalid label key %q: %s", key, strings.Join(errors, "; "))
		}
		if errors := validation.IsValidLabelValue(value); len(errors) > 0 {
			return fmt.Errorf("invalid label value %q: %s", value, strings.Join(errors, "; "))
		}
	}
	for key := range ptr.Deref(metadata.Annotations, nil) {
		if errors := validation.IsQualifiedName(strings.ToLower(key)); len(errors) > 0 {
			return fmt.Errorf("invalid annotation key %q: %s", key, strings.Join(errors, "; "))
		}
	}
	if _, err := namespaceDeleteDuration(r.DeleteTimeout); err != nil {
		return err
	}
	for _, expressions := range []*[]string{r.IgnoreAnnotations, r.IgnoreLabels} {
		for _, expression := range ptr.Deref(expressions, nil) {
			if _, err := regexp.Compile(expression); err != nil {
				return fmt.Errorf("invalid metadata ignore expression: %w", err)
			}
		}
	}
	return nil
}

func namespaceDeleteDuration(value *string) (time.Duration, error) {
	if value == nil {
		return 5 * time.Minute, nil
	}
	parsed, err := time.ParseDuration(*value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("delete-timeout must be a positive Go duration")
	}
	return parsed, nil
}

func equalNamespacePatterns(before, after *[]string) bool {
	return slices.Equal(ptr.Deref(before, nil), ptr.Deref(after, nil))
}
