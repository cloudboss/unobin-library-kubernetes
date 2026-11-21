package v1

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ServiceAccountResource) ResourceDefinition() runtime.ResourceDefinition[
	ServiceAccountResource, *ServiceAccountResourceOutput, *config.Configuration,
] {
	objectField := runtime.InputField(func(r *ServiceAccountResource) *ServiceAccountObject {
		return &r.Object
	})
	return runtime.ResourceDefinition[
		ServiceAccountResource, *ServiceAccountResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input ServiceAccountResource, _ *config.Configuration) error {
			return input.validate()
		},
		Equality: []runtime.InputEqualityRule[ServiceAccountResource]{
			runtime.EqualBy(objectField, func(a, b ServiceAccountObject) bool {
				return reflect.DeepEqual(normalizeServiceAccount(a), normalizeServiceAccount(b))
			}),
			runtime.EqualBy(runtime.InputField(func(r *ServiceAccountResource) **string {
				return &r.CreateTimeout
			}), func(a, b *string) bool {
				left, err := serviceAccountCreateDuration(a)
				right, other := serviceAccountCreateDuration(b)
				return err == nil && other == nil && left == right
			}),
			runtime.EqualBy(runtime.InputField(func(r *ServiceAccountResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *ServiceAccountResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		StableID: func(_ ServiceAccountResource, output *ServiceAccountResourceOutput) (string, error) {
			if output == nil || output.Server == "" || output.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing ServiceAccount server or UID")
			}
			return output.Server + "/" + output.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			ServiceAccountResource, *ServiceAccountResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ServiceAccountResource]{
				runtime.ReplaceWhen(objectField, replaceServiceAccount),
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

func (r *ServiceAccountResource) validate() error {
	metadata := &ConfigMapResource{Object: ConfigMapObject{Metadata: r.Object.Metadata},
		IgnoreAnnotations: r.IgnoreAnnotations, IgnoreLabels: r.IgnoreLabels}
	if err := metadata.validate(); err != nil {
		return err
	}
	_, err := serviceAccountCreateDuration(r.CreateTimeout)
	return err
}

func serviceAccountCreateDuration(input *string) (time.Duration, error) {
	if input == nil {
		return 30 * time.Second, nil
	}
	duration, err := time.ParseDuration(*input)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("create-timeout must be a positive Go duration")
	}
	return duration, nil
}

func normalizeServiceAccount(input ServiceAccountObject) ServiceAccountObjectOutput {
	references := func(input *[]ServiceAccountReference) []ServiceAccountReferenceOutput {
		result := make([]ServiceAccountReferenceOutput, 0, len(configMapValue(input)))
		for _, reference := range configMapValue(input) {
			result = append(result, ServiceAccountReferenceOutput{Name: configMapValue(reference.Name)})
		}
		return serviceAccountReferenceSet(result)
	}
	automount := true
	if input.AutomountServiceAccountToken != nil {
		automount = *input.AutomountServiceAccountToken
	}
	return ServiceAccountObjectOutput{
		Metadata:         normalizeConfigMap(ConfigMapObject{Metadata: input.Metadata}).Metadata,
		ImagePullSecrets: references(input.ImagePullSecrets), Secrets: references(input.Secrets),
		AutomountServiceAccountToken: automount,
	}
}

func replaceServiceAccount(before, after ServiceAccountObject) bool {
	a, b := normalizeServiceAccount(before).Metadata, normalizeServiceAccount(after).Metadata
	return a.Name != b.Name || a.GenerateName != b.GenerateName || a.Namespace != b.Namespace
}

func serviceAccountReferenceSet(
	input []ServiceAccountReferenceOutput) []ServiceAccountReferenceOutput {
	result := append([]ServiceAccountReferenceOutput{}, input...)
	slices.SortFunc(result, func(a, b ServiceAccountReferenceOutput) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return slices.Compact(result)
}

func serviceAccountPullReferences(
	input []ServiceAccountReferenceOutput) []corev1.LocalObjectReference {
	result := make([]corev1.LocalObjectReference, 0, len(input))
	for _, reference := range input {
		result = append(result, corev1.LocalObjectReference{Name: reference.Name})
	}
	return result
}

func serviceAccountSecretReferences(
	input []ServiceAccountReferenceOutput) []corev1.ObjectReference {
	result := make([]corev1.ObjectReference, 0, len(input))
	for _, reference := range input {
		result = append(result, corev1.ObjectReference{Name: reference.Name})
	}
	return result
}

func (r *ServiceAccountResource) project(
	observed *corev1.ServiceAccount, saved *ServiceAccountResourceOutput,
	tracked ConfigMapMetadataOutput,
) (*ServiceAccountResourceOutput, error) {
	annotations, err := projectConfigMapMetadata(
		observed.Annotations, tracked.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectConfigMapMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	pull := make([]ServiceAccountReferenceOutput, 0, len(observed.ImagePullSecrets))
	for _, reference := range observed.ImagePullSecrets {
		pull = append(pull, ServiceAccountReferenceOutput{Name: reference.Name})
	}
	secrets := make([]ServiceAccountReferenceOutput, 0, len(observed.Secrets))
	for _, reference := range observed.Secrets {
		if reference.Name != saved.DefaultSecretName {
			secrets = append(secrets, ServiceAccountReferenceOutput{Name: reference.Name})
		}
	}
	return &ServiceAccountResourceOutput{
		Server: saved.Server, APIVersion: "v1", Kind: "ServiceAccount",
		DefaultSecretName: saved.DefaultSecretName,
		Object: ServiceAccountObjectOutput{
			Metadata: ConfigMapMetadataOutput{Name: observed.Name, GenerateName: observed.GenerateName,
				Namespace: observed.Namespace, UID: string(observed.UID),
				Annotations: annotations, Labels: labels},
			ImagePullSecrets: serviceAccountReferenceSet(pull), Secrets: serviceAccountReferenceSet(secrets),
			AutomountServiceAccountToken: configMapValue(observed.AutomountServiceAccountToken),
		},
	}, nil
}
