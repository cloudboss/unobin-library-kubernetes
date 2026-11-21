package v1

import (
	"encoding/base64"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

func (r *ConfigMapResource) project(
	observed *corev1.ConfigMap, server string, tracked ConfigMapMetadataOutput,
) (*ConfigMapResourceOutput, error) {
	annotations, err := projectConfigMapMetadata(
		observed.Annotations, tracked.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectConfigMapMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	binary := make(map[string]string, len(observed.BinaryData))
	for key, value := range observed.BinaryData {
		binary[key] = base64.StdEncoding.EncodeToString(value)
	}
	return &ConfigMapResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ConfigMap",
		Object: ConfigMapObjectOutput{
			Metadata: ConfigMapMetadataOutput{
				Name: observed.Name, Namespace: observed.Namespace, GenerateName: observed.GenerateName,
				UID: string(observed.UID), Annotations: annotations, Labels: labels,
			},
			Data: configMapValues(&observed.Data), BinaryData: binary,
			Immutable: configMapValue(observed.Immutable),
		},
	}, nil
}

func projectConfigMapMetadata(
	observed, tracked map[string]string, expressions *[]string,
) (map[string]string, error) {
	var ignored []*regexp.Regexp
	for _, expression := range configMapValue(expressions) {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		ignored = append(ignored, pattern)
	}
	projected := map[string]string{}
	for key, value := range observed {
		if _, present := tracked[key]; present {
			projected[key] = value
			continue
		}
		domain, _, _ := strings.Cut(key, "/")
		internal := strings.HasSuffix(domain, "kubernetes.io") &&
			domain != "app.kubernetes.io" && domain != "service.beta.kubernetes.io"
		internal = internal || strings.Contains(key, "deprecated.daemonset.template.generation")
		for _, pattern := range ignored {
			internal = internal || pattern.MatchString(key)
		}
		if !internal {
			projected[key] = value
		}
	}
	return projected, nil
}
