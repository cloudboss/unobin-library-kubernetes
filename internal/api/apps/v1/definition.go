package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *DeploymentResource) ResourceDefinition() runtime.ResourceDefinition[
	DeploymentResource, *DeploymentResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		DeploymentResource, *DeploymentResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input DeploymentResource, _ *config.Configuration) error {
			_, err := input.expand(nil)
			return err
		},
		StableID: func(_ DeploymentResource, output *DeploymentResourceOutput) (string, error) {
			if output == nil || output.UID == "" {
				return "", fmt.Errorf("missing Deployment UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[DeploymentResource]{
			runtime.EqualBy(runtime.InputField(func(r *DeploymentResource) *Metadata {
				return &r.Metadata
			}), func(a, b Metadata) bool {
				return outerIdentity(a) == outerIdentity(b) &&
					reflect.DeepEqual(metadataMap(a.Labels), metadataMap(b.Labels)) &&
					reflect.DeepEqual(metadataMap(a.Annotations), metadataMap(b.Annotations))
			}),
			runtime.EqualBy(runtime.InputField(func(r *DeploymentResource) *DeploymentSpec {
				return &r.Spec
			}), equivalentSpec),
		},
		Replace: runtime.Replacement[
			DeploymentResource, *DeploymentResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[DeploymentResource]{
				runtime.ReplaceWhen(runtime.InputField(func(r *DeploymentResource) *Metadata {
					return &r.Metadata
				}), func(a, b Metadata) bool { return outerIdentity(a) != outerIdentity(b) }),
				runtime.ReplaceWhen(runtime.InputField(func(r *DeploymentResource) *DeploymentSpec {
					return &r.Spec
				}), func(a, b DeploymentSpec) bool {
					return metadataIdentity(a.Template.Metadata) != metadataIdentity(b.Template.Metadata) ||
						!reflect.DeepEqual(replacementValues(reflect.ValueOf(a), ""),
							replacementValues(reflect.ValueOf(b), ""))
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Namespace
				}),
			},
		},
	}
}

func metadataIdentity(m Metadata) [3]string {
	var result [3]string
	for i, value := range []*string{m.Name, m.GenerateName, m.Namespace} {
		if value != nil {
			result[i] = *value
		}
	}
	return result
}

func outerIdentity(m Metadata) [3]string {
	identity := metadataIdentity(m)
	if m.Namespace == nil {
		identity[2] = "default"
	}
	return identity
}

func metadataMap(value *map[string]string) map[string]string {
	if value == nil || len(*value) == 0 {
		return nil
	}
	return *value
}

func equivalentSpec(a, b DeploymentSpec) bool {
	left, leftErr := expandValue(reflect.ValueOf(a), reflect.Value{}, "spec")
	right, rightErr := expandValue(reflect.ValueOf(b), reflect.Value{}, "spec")
	return leftErr == nil && rightErr == nil &&
		reflect.DeepEqual(comparisonValue(left, reflect.TypeFor[DeploymentSpec](), "", false),
			comparisonValue(right, reflect.TypeFor[DeploymentSpec](), "", false)) &&
		reflect.DeepEqual(ineffectiveValues(reflect.ValueOf(a)), ineffectiveValues(reflect.ValueOf(b)))
}

func comparisonValue(value any, model reflect.Type, format string, computed bool) any {
	if value == nil {
		return nil
	}
	for model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	if !computed && (model.Kind() == reflect.Slice || model.Kind() == reflect.Map) &&
		reflect.ValueOf(value).Len() == 0 {
		return nil
	}
	if format == "quantity" {
		quantity, err := resource.ParseQuantity(value.(string))
		if err == nil {
			mantissa, exponent := quantity.AsCanonicalBytes(nil)
			return string(mantissa) + "e" + strconv.Itoa(int(exponent))
		}
	}
	if format == "quantities" {
		out := map[string]any{}
		for key, quantity := range value.(map[string]any) {
			out[key] = comparisonValue(quantity, reflect.TypeFor[string](), "quantity", false)
		}
		return out
	}
	switch model.Kind() {
	case reflect.Struct:
		values, ok := value.(map[string]any)
		if !ok {
			return value
		}
		out := maps.Clone(values)
		for field := range model.Fields() {
			name := field.Tag.Get("json")
			if item, present := values[name]; present {
				item = comparisonValue(item, field.Type, field.Tag.Get("format"),
					field.Tag.Get("computed") == "true")
				if item == nil {
					delete(out, name)
				} else {
					out[name] = item
				}
			}
		}
		return out
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return comparisonValue(value, model.Elem(), "", false)
		}
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = comparisonValue(item, model.Elem(), "", false)
		}
		return out
	}
	return value
}

func equivalentObservedSpec(a, b DeploymentSpec) bool {
	left, leftErr := nativeMap(a)
	right, rightErr := nativeMap(b)
	return leftErr == nil && rightErr == nil &&
		reflect.DeepEqual(comparisonValue(left, reflect.TypeFor[DeploymentSpec](), "", false),
			comparisonValue(right, reflect.TypeFor[DeploymentSpec](), "", false))
}

func ineffectiveValues(v reflect.Value) any {
	v = indirect(v)
	if !v.IsValid() {
		return nil
	}
	out := map[string]any{}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if field.Tag.Get("json") == "-" {
				out[field.Name] = v.Field(i).Interface()
			} else if child := ineffectiveValues(v.Field(i)); child != nil {
				out[field.Name] = child
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			if child := ineffectiveValues(v.Index(i)); child != nil {
				out[fmt.Sprint(i)] = child
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func replacementValues(v reflect.Value, prefix string) map[string]any {
	out := map[string]any{}
	v = indirect(v)
	if !v.IsValid() {
		return out
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			key := prefix + "." + field.Name
			if field.Tag.Get("replace") == "true" {
				value, err := expandValue(v.Field(i), reflect.Value{}, key)
				if err == nil && value != nil {
					value, err = formatValue(field.Tag.Get("format"), value)
				}
				if err == nil {
					if list, ok := value.([]any); ok && field.Tag.Get("set") == "true" {
						value = canonicalSet(list)
					}
					value = comparisonValue(value, field.Type, field.Tag.Get("format"),
						field.Tag.Get("computed") == "true")
					if value != nil {
						out[key] = value
					}
				}
			} else {
				maps.Copy(out, replacementValues(v.Field(i), key))
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			maps.Copy(out, replacementValues(v.Index(i), fmt.Sprintf("%s[%d]", prefix, i)))
		}
	}
	return out
}

func duration(value *string, field string) (time.Duration, error) {
	if value == nil {
		return 10 * time.Minute, nil
	}
	parsed, err := time.ParseDuration(*value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", field)
	}
	return parsed, nil
}

func (r *DeploymentResource) validateControls() error {
	for _, entry := range []struct {
		name  string
		value *string
	}{
		{"create-timeout", r.CreateTimeout}, {"update-timeout", r.UpdateTimeout},
		{"delete-timeout", r.DeleteTimeout},
	} {
		if _, err := duration(entry.value, entry.name); err != nil {
			return err
		}
	}
	for _, patterns := range []*[]string{r.IgnoreAnnotations, r.IgnoreLabels} {
		if patterns != nil {
			for _, pattern := range *patterns {
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("invalid metadata ignore expression: %w", err)
				}
			}
		}
	}
	return nil
}
