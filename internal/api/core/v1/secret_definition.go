package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *SecretResource) ResourceDefinition() runtime.ResourceDefinition[
	SecretResource, *SecretResourceOutput, *config.Configuration,
] {
	field := runtime.InputField(func(r *SecretResource) *SecretObject { return &r.Object })
	return runtime.ResourceDefinition[
		SecretResource, *SecretResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input SecretResource, _ *config.Configuration) error {
			return input.validate()
		},
		Equality: []runtime.InputEqualityRule[SecretResource]{
			runtime.EqualBy(field, func(before, after SecretObject) bool {
				return reflect.DeepEqual(normalizeSecret(before), normalizeSecret(after))
			}),
			runtime.EqualBy(runtime.InputField(func(r *SecretResource) **bool {
				return &r.WaitForServiceAccountToken
			}), func(before, after *bool) bool {
				return (before == nil || *before) == (after == nil || *after)
			}),
			runtime.EqualBy(runtime.InputField(func(r *SecretResource) **string {
				return &r.CreateTimeout
			}), func(before, after *string) bool {
				left, err := secretCreateTimeout(before)
				right, other := secretCreateTimeout(after)
				return err == nil && other == nil && left == right
			}),
			runtime.EqualBy(runtime.InputField(func(r *SecretResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *SecretResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		StableID: func(_ SecretResource, output *SecretResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing Secret server or UID")
			}
			return output.Server + "/" + output.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			SecretResource, *SecretResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[SecretResource]{runtime.ReplaceWhen(field, replaceSecret)},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
			Drift: []runtime.DriftRule[*SecretResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(func(o *SecretResourceOutput) *bool {
					return &o.Object.Immutable
				}), func(before, after bool) bool { return !before && after }),
			},
		},
	}
}

func (r *SecretResource) validate() error {
	metadata := r.Object.Metadata
	if metadata.Name != nil && metadata.GenerateName != nil {
		return fmt.Errorf("metadata.name conflicts with metadata.generate-name")
	}
	if metadata.Name != nil {
		if problems := validation.IsDNS1123Subdomain(*metadata.Name); len(problems) > 0 {
			return fmt.Errorf("invalid metadata.name: %s", strings.Join(problems, "; "))
		}
	}
	if metadata.GenerateName != nil {
		if problems := apivalidation.NameIsDNSLabel(*metadata.GenerateName, true); len(problems) > 0 {
			return fmt.Errorf("invalid metadata.generate-name: %s", strings.Join(problems, "; "))
		}
	}
	for key, value := range configMapValues(metadata.Labels) {
		if problems := validation.IsQualifiedName(key); len(problems) > 0 {
			return fmt.Errorf("invalid metadata label key: %s", strings.Join(problems, "; "))
		}
		if problems := validation.IsValidLabelValue(value); len(problems) > 0 {
			return fmt.Errorf("invalid metadata label value: %s", strings.Join(problems, "; "))
		}
	}
	for key := range configMapValues(metadata.Annotations) {
		if problems := validation.IsQualifiedName(strings.ToLower(key)); len(problems) > 0 {
			return fmt.Errorf("invalid metadata annotation key: %s", strings.Join(problems, "; "))
		}
	}
	for _, expressions := range []*[]string{r.IgnoreAnnotations, r.IgnoreLabels} {
		for _, expression := range configMapValue(expressions) {
			if _, err := regexp.Compile(expression); err != nil {
				return fmt.Errorf("invalid metadata ignore expression: %w", err)
			}
		}
	}
	_, err := secretCreateTimeout(r.CreateTimeout)
	return err
}

func normalizeSecret(input SecretObject) SecretObjectOutput {
	namespace, kind := "default", "Opaque"
	if input.Metadata.Namespace != nil {
		namespace = *input.Metadata.Namespace
	}
	if input.Type != nil {
		kind = *input.Type
	}
	var text map[string]string
	if input.Data != nil {
		text = configMapValues(input.Data)
	}
	return SecretObjectOutput{
		Metadata: SecretMetadataOutput{
			Name:         configMapValue(input.Metadata.Name),
			GenerateName: configMapValue(input.Metadata.GenerateName), Namespace: namespace,
			Annotations: configMapValues(input.Metadata.Annotations),
			Labels:      configMapValues(input.Metadata.Labels),
		},
		Data: text, BinaryData: configMapValues(input.BinaryData),
		Immutable: configMapValue(input.Immutable), Type: kind,
	}
}

func replaceSecret(before, after SecretObject) bool {
	previous, desired := normalizeSecret(before), normalizeSecret(after)
	if previous.Metadata.Name != desired.Metadata.Name ||
		previous.Metadata.GenerateName != desired.Metadata.GenerateName ||
		previous.Metadata.Namespace != desired.Metadata.Namespace || previous.Type != desired.Type {
		return true
	}
	return previous.Immutable && (previous.Immutable != desired.Immutable ||
		(before.Data == nil) != (after.Data == nil) || !maps.Equal(previous.Data, desired.Data) ||
		!maps.Equal(previous.BinaryData, desired.BinaryData))
}

func secretCreateTimeout(input *string) (time.Duration, error) {
	if input == nil {
		return time.Minute, nil
	}
	timeout, err := time.ParseDuration(*input)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("create-timeout must be a positive Go duration")
	}
	return timeout, nil
}
