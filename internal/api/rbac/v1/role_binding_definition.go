package v1

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/cloudboss/unobin/pkg/runtime"
	rbacv1 "k8s.io/api/rbac/v1"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *RoleBindingResource) ResourceDefinition() runtime.ResourceDefinition[
	RoleBindingResource, *RoleBindingResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *RoleBindingResource) *RoleMetadata { return &r.Metadata })
	return runtime.ResourceDefinition[RoleBindingResource, *RoleBindingResourceOutput,
		*config.Configuration]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input RoleBindingResource, _ *config.Configuration) error {
			return input.validate()
		},
		StableID: func(_ RoleBindingResource, output *RoleBindingResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.UID == "" {
				return "", fmt.Errorf("missing RoleBinding server or UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[RoleBindingResource]{
			runtime.EqualBy(metadata, func(before, after RoleMetadata) bool {
				left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
				if after.Name == nil {
					right.Name = left.Name
				}
				return reflect.DeepEqual(left, right)
			}),
			runtime.EqualBy(runtime.InputField(func(r *RoleBindingResource) *[]RoleBindingSubject {
				return &r.Subject
			}), equalBindingSubjects),
			runtime.EqualBy(runtime.InputField(func(r *RoleBindingResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalRoleLists),
			runtime.EqualBy(runtime.InputField(func(r *RoleBindingResource) **[]string {
				return &r.IgnoreLabels
			}), equalRoleLists),
		},
		Replace: runtime.Replacement[RoleBindingResource, *RoleBindingResourceOutput,
			*config.Configuration]{
			Fields: []runtime.AnyInputField[RoleBindingResource]{
				runtime.InputField(func(r *RoleBindingResource) *RoleBindingRoleRef { return &r.RoleRef }),
			},
			Rules: []runtime.ReplacementRule[RoleBindingResource]{
				runtime.ReplaceWhen(metadata, func(before, after RoleMetadata) bool {
					left, right := normalizeRoleMetadata(before), normalizeRoleMetadata(after)
					return (after.Name != nil && left.Name != right.Name) ||
						left.Namespace != right.Namespace || left.GenerateName != right.GenerateName
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
			Drift: []runtime.DriftRule[*RoleBindingResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(
					func(o *RoleBindingResourceOutput) *RoleBindingRoleRef { return &o.RoleRef },
				), func(before, after RoleBindingRoleRef) bool { return before != after }),
			},
		},
	}
}

func (r *RoleBindingResource) validate() error {
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
	if r.RoleRef.APIGroup != rbacv1.GroupName ||
		(r.RoleRef.Kind != "Role" && r.RoleRef.Kind != "ClusterRole") {
		return fmt.Errorf("role-ref requires api-group rbac.authorization.k8s.io " +
			"and kind Role or ClusterRole")
	}
	if len(r.Subject) == 0 {
		return fmt.Errorf("subject requires at least one entry")
	}
	for i, subject := range r.Subject {
		if subject.Kind != "User" && subject.Kind != "Group" && subject.Kind != "ServiceAccount" {
			return fmt.Errorf("subject[%d].kind must be User, Group, or ServiceAccount", i)
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
