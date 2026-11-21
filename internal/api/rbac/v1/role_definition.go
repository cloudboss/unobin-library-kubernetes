package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/cloudboss/unobin/pkg/runtime"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *RoleResource) ResourceDefinition() runtime.ResourceDefinition[
	RoleResource, *RoleResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *RoleResource) *RoleMetadata { return &r.Metadata })
	return runtime.ResourceDefinition[RoleResource, *RoleResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input RoleResource, _ *config.Configuration) error {
			return input.validate()
		},
		StableID: func(_ RoleResource, output *RoleResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.UID == "" {
				return "", fmt.Errorf("missing Role server or UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[RoleResource]{
			runtime.EqualBy(metadata, func(before, after RoleMetadata) bool {
				left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
				if after.Name == nil {
					right.Name = left.Name
				}
				return reflect.DeepEqual(left, right)
			}),
			runtime.EqualBy(runtime.InputField(func(r *RoleResource) *[]RoleRule {
				return &r.Rule
			}),
				func(before, after []RoleRule) bool {
					return reflect.DeepEqual(normalizeRoleRules(before), normalizeRoleRules(after))
				}),
			runtime.EqualBy(runtime.InputField(func(r *RoleResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalRoleLists),
			runtime.EqualBy(runtime.InputField(func(r *RoleResource) **[]string {
				return &r.IgnoreLabels
			}), equalRoleLists),
		},
		Replace: runtime.Replacement[RoleResource, *RoleResourceOutput, *config.Configuration]{
			Rules: []runtime.ReplacementRule[RoleResource]{
				runtime.ReplaceWhen(metadata, func(before, after RoleMetadata) bool {
					left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
					return (after.Name != nil && left.Name != right.Name) ||
						left.Namespace != right.Namespace || left.GenerateName != right.GenerateName
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
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

func (r *RoleResource) validate() error {
	if r.Metadata.Name != nil && r.Metadata.GenerateName != nil {
		return fmt.Errorf("metadata.name conflicts with metadata.generate-name")
	}
	for _, field := range []struct {
		name  string
		value *string
	}{{"name", r.Metadata.Name}, {"generate-name", r.Metadata.GenerateName}} {
		if field.value != nil {
			if errors := pathvalidation.IsValidPathSegmentName(*field.value); len(errors) > 0 {
				return fmt.Errorf("invalid metadata.%s: %s", field.name, strings.Join(errors, "; "))
			}
		}
	}
	for key, value := range roleMap(r.Metadata.Labels) {
		if errors := validation.IsQualifiedName(key); len(errors) > 0 {
			return fmt.Errorf("invalid label key %q: %s", key, strings.Join(errors, "; "))
		}
		if errors := validation.IsValidLabelValue(value); len(errors) > 0 {
			return fmt.Errorf("invalid label value %q: %s", value, strings.Join(errors, "; "))
		}
	}
	for key := range roleMap(r.Metadata.Annotations) {
		if errors := validation.IsQualifiedName(strings.ToLower(key)); len(errors) > 0 {
			return fmt.Errorf("invalid annotation key %q: %s", key, strings.Join(errors, "; "))
		}
	}
	if len(r.Rule) == 0 {
		return fmt.Errorf("rule requires at least one policy rule")
	}
	for i, rule := range r.Rule {
		if rule.APIGroups == nil || rule.Resources == nil || rule.Verbs == nil {
			return fmt.Errorf("rule[%d] requires api-groups, resources, and verbs", i)
		}
	}
	for _, expressions := range []*[]string{r.IgnoreAnnotations, r.IgnoreLabels} {
		for _, expression := range roleValue(expressions) {
			if _, err := regexp.Compile(expression); err != nil {
				return fmt.Errorf("invalid metadata ignore expression: %w", err)
			}
		}
	}
	return nil
}

func normalizeRoleMetadata(input RoleMetadata) RoleMetadataOutput {
	namespace := "default"
	if input.Namespace != nil {
		namespace = *input.Namespace
	}
	return RoleMetadataOutput{
		Name: roleValue(input.Name), GenerateName: roleValue(input.GenerateName),
		Namespace:   namespace,
		Annotations: roleMap(input.Annotations), Labels: roleMap(input.Labels),
	}
}

func roleValue[T any](input *T) T {
	if input == nil {
		var zero T
		return zero
	}
	return *input
}

func roleMap(input *map[string]string) map[string]string {
	values := roleValue(input)
	if len(values) == 0 {
		return map[string]string{}
	}
	return maps.Clone(values)
}

func equalRoleLists(before, after *[]string) bool {
	return slices.Equal(roleValue(before), roleValue(after))
}
