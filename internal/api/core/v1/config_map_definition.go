package v1

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ConfigMapResource) ResourceDefinition() runtime.ResourceDefinition[
	ConfigMapResource, *ConfigMapResourceOutput, *config.Configuration,
] {
	objectField := runtime.InputField(func(r *ConfigMapResource) *ConfigMapObject {
		return &r.Object
	})
	return runtime.ResourceDefinition[
		ConfigMapResource, *ConfigMapResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[ConfigMapResource]{
			runtime.EqualBy(objectField, func(before, after ConfigMapObject) bool {
				return reflect.DeepEqual(normalizeConfigMap(before), normalizeConfigMap(after))
			}),
			runtime.EqualBy(runtime.InputField(func(r *ConfigMapResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *ConfigMapResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		StableID: func(_ ConfigMapResource, output *ConfigMapResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing ConfigMap server or UID")
			}
			return output.Server + "/" + output.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			ConfigMapResource, *ConfigMapResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ConfigMapResource]{
				runtime.ReplaceWhen(objectField, replaceConfigMap),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
			Drift: []runtime.DriftRule[*ConfigMapResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(func(o *ConfigMapResourceOutput) *bool {
					return &o.Object.Immutable
				}), func(before, after bool) bool { return !before && after }),
			},
		},
	}
}

func normalizeConfigMap(input ConfigMapObject) ConfigMapObjectOutput {
	namespace := "default"
	if input.Metadata.Namespace != nil {
		namespace = *input.Metadata.Namespace
	}
	return ConfigMapObjectOutput{
		Metadata: ConfigMapMetadataOutput{
			Name:         configMapValue(input.Metadata.Name),
			GenerateName: configMapValue(input.Metadata.GenerateName), Namespace: namespace,
			Annotations: configMapValues(input.Metadata.Annotations),
			Labels:      configMapValues(input.Metadata.Labels),
		},
		Data: configMapValues(input.Data), BinaryData: configMapValues(input.BinaryData),
		Immutable: configMapValue(input.Immutable),
	}
}

func replaceConfigMap(before, after ConfigMapObject) bool {
	previous, desired := normalizeConfigMap(before), normalizeConfigMap(after)
	if previous.Metadata.Name != desired.Metadata.Name ||
		previous.Metadata.Namespace != desired.Metadata.Namespace ||
		previous.Metadata.GenerateName != desired.Metadata.GenerateName {
		return true
	}
	return previous.Immutable && (previous.Immutable != desired.Immutable ||
		!maps.Equal(previous.Data, desired.Data) || !maps.Equal(previous.BinaryData, desired.BinaryData))
}

func configMapValue[T any](input *T) T {
	if input == nil {
		var zero T
		return zero
	}
	return *input
}

func configMapValues(input *map[string]string) map[string]string {
	values := configMapValue(input)
	if values == nil {
		return map[string]string{}
	}
	return maps.Clone(values)
}

func equalConfigMapLists(before, after *[]string) bool {
	return slices.Equal(configMapValue(before), configMapValue(after))
}
