package v1

import (
	"fmt"
	"net/url"

	admissionv1 "k8s.io/api/admissionregistration/v1"
)

func checkWebhookURL(index int, endpoint *string) error {
	if endpoint == nil {
		return nil
	}
	parsed, err := url.Parse(*endpoint)
	if err != nil {
		return fmt.Errorf("webhook[%d].client-config.url: %w", index, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("webhook[%d].client-config.url must use HTTPS and a host, "+
			"without user info, query, or fragment", index)
	}
	return nil
}

func nativeWebhookClientConfig(input WebhookClientConfig) admissionv1.WebhookClientConfig {
	out := admissionv1.WebhookClientConfig{
		CABundle: []byte(valueOr(input.CABundle, "")),
		URL:      input.URL,
	}
	if service := input.Service; service != nil {
		out.Service = &admissionv1.ServiceReference{
			Name: service.Name, Namespace: service.Namespace,
			Port: new(int32(valueOr(service.Port, 443))),
		}
		if valueOr(service.Path, "") != "" {
			out.Service.Path = service.Path
		}
	}
	return out
}

func projectWebhookClientConfig(input admissionv1.WebhookClientConfig) WebhookClientConfig {
	out := WebhookClientConfig{URL: input.URL}
	if len(input.CABundle) > 0 {
		out.CABundle = new(string(input.CABundle))
	}
	if service := input.Service; service != nil {
		out.Service = &WebhookService{
			Name: service.Name, Namespace: service.Namespace, Path: service.Path,
			Port: optionalInteger(service.Port),
		}
	}
	return out
}
