package v1

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

func (r *SecretResource) project(
	observed *corev1.Secret, server string, tracked SecretMetadataOutput,
) (*SecretResourceOutput, error) {
	annotations, err := projectSecretMetadata(observed.Annotations,
		tracked.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectSecretMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	binary := map[string]string{}
	text := make(map[string]string, len(observed.Data))
	selected := configMapValues(r.Object.BinaryData)
	for key := range selected {
		binary[key] = base64.StdEncoding.EncodeToString(observed.Data[key])
	}
	for key, value := range observed.Data {
		if _, present := selected[key]; !present {
			text[key] = string(value)
		}
	}
	return &SecretResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Secret",
		Object: SecretObjectOutput{
			Metadata: SecretMetadataOutput{
				Name: observed.Name, GenerateName: observed.GenerateName, Namespace: observed.Namespace,
				UID: string(observed.UID), Annotations: annotations, Labels: labels,
			},
			Data: text, BinaryData: binary, Immutable: configMapValue(observed.Immutable),
			Type: string(observed.Type),
		},
	}, nil
}

func projectSecretMetadata(
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
		internal := secretInternalKey(key)
		for _, pattern := range ignored {
			internal = internal || pattern.MatchString(key)
		}
		if !internal {
			projected[key] = value
		}
	}
	return projected, nil
}

func secretInternalKey(key string) bool {
	parsed, err := url.Parse("https://" + key)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "app.kubernetes.io" || host == "service.beta.kubernetes.io" {
		return false
	}
	return strings.HasSuffix(host, "kubernetes.io") ||
		strings.Contains(key, "deprecated.daemonset.template.generation")
}
