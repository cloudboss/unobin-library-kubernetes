package v1

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func (r *DefaultServiceAccountResource) ResourceDefinition() runtime.ResourceDefinition[
	DefaultServiceAccountResource, *DefaultServiceAccountResourceOutput, *config.Configuration,
] {
	namespace := runtime.InputField(func(r *DefaultServiceAccountResource) **string {
		return &r.Object.Metadata.Namespace
	})
	return runtime.ResourceDefinition[
		DefaultServiceAccountResource, *DefaultServiceAccountResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[DefaultServiceAccountResource]{
			runtime.EqualBy(namespace, func(a, b *string) bool {
				return defaultServiceAccountNamespace(a) == defaultServiceAccountNamespace(b)
			}),
			runtime.EqualBy(runtime.InputField(func(r *DefaultServiceAccountResource) **string {
				return &r.Object.Metadata.Name
			}), func(a, b *string) bool {
				return ptr.Deref(a, defaultServiceAccountName) == ptr.Deref(b, defaultServiceAccountName)
			}),
			runtime.EqualBy(runtime.InputField(func(r *DefaultServiceAccountResource) **bool {
				return &r.Object.AutomountServiceAccountToken
			}), func(a, b *bool) bool {
				return ptr.Deref(a, true) == ptr.Deref(b, true)
			}),
			runtime.EqualBy(
				runtime.InputField(func(r *DefaultServiceAccountResource) **[]ServiceAccountReference {
					return &r.Object.ImagePullSecrets
				}), equalDefaultServiceAccountReferences,
			),
			runtime.EqualBy(
				runtime.InputField(func(r *DefaultServiceAccountResource) **[]ServiceAccountReference {
					return &r.Object.Secrets
				}), equalDefaultServiceAccountReferences,
			),
			runtime.EqualBy(
				runtime.InputField(func(r *DefaultServiceAccountResource) **map[string]string {
					return &r.Object.Metadata.Labels
				}), equalDefaultServiceAccountMaps,
			),
			runtime.EqualBy(
				runtime.InputField(func(r *DefaultServiceAccountResource) **map[string]string {
					return &r.Object.Metadata.Annotations
				}), equalDefaultServiceAccountMaps,
			),
			runtime.EqualBy(runtime.InputField(func(r *DefaultServiceAccountResource) **string {
				return &r.CreateTimeout
			}), func(a, b *string) bool {
				left, err := serviceAccountCreateDuration(a)
				right, other := serviceAccountCreateDuration(b)
				if err != nil || other != nil {
					return ptr.Deref(a, "30s") == ptr.Deref(b, "30s")
				}
				return left == right
			}),
			runtime.EqualBy(runtime.InputField(func(r *DefaultServiceAccountResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *DefaultServiceAccountResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		StableID: defaultServiceAccountStableID,
		Replace: runtime.Replacement[
			DefaultServiceAccountResource, *DefaultServiceAccountResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[DefaultServiceAccountResource]{
				runtime.ReplaceWhen(namespace, func(before, after *string) bool {
					return defaultServiceAccountNamespace(before) != defaultServiceAccountNamespace(after)
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

func defaultServiceAccountStableID(
	_ DefaultServiceAccountResource, output *DefaultServiceAccountResourceOutput,
) (string, error) {
	if output == nil || output.Server == "" || output.Object.Metadata.UID == "" {
		return "", fmt.Errorf("missing default ServiceAccount server or UID")
	}
	return output.Server + "/" + output.Object.Metadata.UID, nil
}

func defaultServiceAccountNamespace(namespace *string) string {
	return ptr.Deref(namespace, "default")
}

func normalizeDefaultServiceAccount(
	input DefaultServiceAccountObject,
) DefaultServiceAccountObjectOutput {
	return DefaultServiceAccountObjectOutput{
		Metadata: DefaultServiceAccountMetadataOutput{
			Name:        ptr.Deref(input.Metadata.Name, defaultServiceAccountName),
			Namespace:   defaultServiceAccountNamespace(input.Metadata.Namespace),
			Annotations: configMapValues(input.Metadata.Annotations),
			Labels:      configMapValues(input.Metadata.Labels),
		},
		ImagePullSecrets:             defaultServiceAccountReferences(input.ImagePullSecrets),
		Secrets:                      defaultServiceAccountReferences(input.Secrets),
		AutomountServiceAccountToken: ptr.Deref(input.AutomountServiceAccountToken, true),
	}
}

func defaultServiceAccountReferences(
	input *[]ServiceAccountReference,
) []ServiceAccountReferenceOutput {
	references := configMapValue(input)
	result := make([]ServiceAccountReferenceOutput, 0, len(references))
	for _, ref := range references {
		result = append(result, ServiceAccountReferenceOutput{Name: configMapValue(ref.Name)})
	}
	return serviceAccountReferenceSet(result)
}

func equalDefaultServiceAccountReferences(a, b *[]ServiceAccountReference) bool {
	return reflect.DeepEqual(defaultServiceAccountReferences(a), defaultServiceAccountReferences(b))
}

func equalDefaultServiceAccountMaps(a, b *map[string]string) bool {
	return maps.Equal(configMapValue(a), configMapValue(b))
}

func (r *DefaultServiceAccountResource) projectDefaultServiceAccount(
	live *corev1.ServiceAccount, target object.Target, token string,
	tracked DefaultServiceAccountMetadataOutput,
) *DefaultServiceAccountResourceOutput {
	pull := make([]ServiceAccountReferenceOutput, 0, len(live.ImagePullSecrets))
	for _, reference := range live.ImagePullSecrets {
		pull = append(pull, ServiceAccountReferenceOutput{Name: reference.Name})
	}
	secrets := make([]ServiceAccountReferenceOutput, 0, len(live.Secrets))
	for _, reference := range live.Secrets {
		if reference.Name != token {
			secrets = append(secrets, ServiceAccountReferenceOutput{Name: reference.Name})
		}
	}
	return &DefaultServiceAccountResourceOutput{
		Server: target.Server, APIVersion: target.APIVersion, Kind: target.Kind,
		DefaultSecretName: token,
		Object: DefaultServiceAccountObjectOutput{
			Metadata:                     r.projectDefaultServiceAccountMetadata(live, tracked),
			ImagePullSecrets:             serviceAccountReferenceSet(pull),
			Secrets:                      serviceAccountReferenceSet(secrets),
			AutomountServiceAccountToken: ptr.Deref(live.AutomountServiceAccountToken, false),
		},
	}
}

func (r *DefaultServiceAccountResource) projectDefaultServiceAccountMetadata(
	live *corev1.ServiceAccount, tracked DefaultServiceAccountMetadataOutput,
) DefaultServiceAccountMetadataOutput {
	return DefaultServiceAccountMetadataOutput{
		Name: live.Name, Namespace: live.Namespace, UID: string(live.UID),
		Annotations: defaultServiceAccountMetadata(live.Annotations, tracked.Annotations,
			r.IgnoreAnnotations),
		Labels: defaultServiceAccountMetadata(live.Labels, tracked.Labels, r.IgnoreLabels),
	}
}

func defaultServiceAccountMetadata(
	live, tracked map[string]string, expressions *[]string,
) map[string]string {
	projected, _ := projectConfigMapMetadata(live, tracked, nil)
	// Invalid historical expressions cannot match keys during refresh or teardown.
	for _, expression := range ptr.Deref(expressions, nil) {
		pattern, err := regexp.Compile(expression)
		if err == nil {
			maps.DeleteFunc(projected, func(key, _ string) bool {
				_, managed := tracked[key]
				return !managed && pattern.MatchString(key)
			})
		}
	}
	return projected
}

func defaultServiceAccountTrackedMetadata(
	prior DefaultServiceAccountMetadata, desired DefaultServiceAccountMetadataOutput,
) DefaultServiceAccountMetadataOutput {
	annotations := configMapValues(prior.Annotations)
	maps.Copy(annotations, desired.Annotations)
	labels := configMapValues(prior.Labels)
	maps.Copy(labels, desired.Labels)
	return DefaultServiceAccountMetadataOutput{Annotations: annotations, Labels: labels}
}
