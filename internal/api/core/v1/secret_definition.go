package v1

import (
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"

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
				if err != nil || other != nil {
					return configMapValue(before) == configMapValue(after)
				}
				return left == right
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
	if err != nil {
		return 0, fmt.Errorf("parse create-timeout: %w", err)
	}
	return timeout, nil
}
