package v1

import (
	"context"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *JobResource) ResourceDefinition() runtime.ResourceDefinition[
	JobResource, *JobResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[JobResource, *JobResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input JobResource, _ *config.Configuration) error {
			_, err := input.requestSpec(nil, false)
			return err
		},
		StableID: func(_ JobResource, output *JobResourceOutput) (string, error) {
			if output == nil || output.UID == "" {
				return "", fmt.Errorf("missing Job UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[JobResource]{
			runtime.EqualBy(runtime.InputField(func(r *JobResource) *Metadata {
				return &r.Metadata
			}), mutableMetadataEqual),
			runtime.EqualBy(runtime.InputField(func(r *JobResource) *JobSpec {
				return &r.Spec
			}), mutableSpecEqual),
		},
		Replace: runtime.Replacement[JobResource, *JobResourceOutput, *config.Configuration]{
			Rules: []runtime.ReplacementRule[JobResource]{
				runtime.ReplaceWhen(runtime.InputField(func(r *JobResource) *Metadata {
					return &r.Metadata
				}), func(a, b Metadata) bool { return !metadataIdentityEqual(a, b) }),
				runtime.ReplaceWhen(runtime.InputField(func(r *JobResource) *JobSpec {
					return &r.Spec
				}), immutableChanged),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.CurrentContext }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Namespace }),
			},
		},
	}
}

func metadataIdentityEqual(a, b Metadata) bool {
	if b.Name == nil {
		b.Name = a.Name
	}
	namespace := func(v *string) string {
		if v == nil {
			return "default"
		}
		return *v
	}
	text := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	return namespace(a.Namespace) == namespace(b.Namespace) &&
		text(a.Name) == text(b.Name) && text(a.GenerateName) == text(b.GenerateName)
}

func metadataEqual(a, b Metadata) bool {
	if !metadataIdentityEqual(a, b) {
		return false
	}
	if b.Labels == nil {
		b.Labels = a.Labels
	}
	return reflect.DeepEqual(metadataMap(a.Labels), metadataMap(b.Labels)) &&
		reflect.DeepEqual(metadataMap(a.Annotations), metadataMap(b.Annotations))
}

func metadataMap(value *map[string]string) map[string]string {
	if value == nil || len(*value) == 0 {
		return nil
	}
	return *value
}

func comparedSpec(spec JobSpec, observed *JobSpec) (any, error) {
	var old reflect.Value
	if observed != nil {
		old = reflect.ValueOf(*observed)
	}
	value, err := expandValueMode(reflect.ValueOf(spec), old, "spec", false)
	if err != nil {
		return nil, err
	}
	data := value.(map[string]any)
	if template, ok := data["template"].(map[string]any); ok {
		if metadata, ok := template["metadata"].(map[string]any); ok {
			removeGeneratedAny(metadata["labels"])
		}
	}
	if !boolean(spec.ManualSelector) {
		if selector, ok := data["selector"].(map[string]any); ok {
			removeGeneratedAny(selector["matchLabels"])
		}
	}
	return comparisonValue(data, reflect.TypeFor[JobSpec](), "", false), nil
}

func equivalentSpec(a, b JobSpec) bool {
	left, err := comparedSpec(a, nil)
	if err != nil {
		return false
	}
	right, err := comparedSpec(b, &a)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(left, right) &&
		reflect.DeepEqual(ineffectiveValues(reflect.ValueOf(a)), ineffectiveValues(reflect.ValueOf(b)))
}

func immutableChanged(a, b JobSpec) bool {
	left, err := comparedSpec(a, nil)
	if err != nil {
		return true
	}
	right, err := comparedSpec(b, &a)
	if err != nil {
		return true
	}
	for _, key := range []string{
		"completions", "backoffLimitPerIndex", "completionMode", "podFailurePolicy",
		"selector", "template",
	} {
		if !reflect.DeepEqual(left.(map[string]any)[key], right.(map[string]any)[key]) {
			return true
		}
	}
	return !reflect.DeepEqual(ineffectiveValues(reflect.ValueOf(a.Template)),
		ineffectiveValues(reflect.ValueOf(b.Template)))
}
