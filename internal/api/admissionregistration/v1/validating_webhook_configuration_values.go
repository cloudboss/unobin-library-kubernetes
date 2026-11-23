package v1

import (
	"fmt"
	"net/url"
	"slices"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func valueOr[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

func prepareValidatingWebhooks(
	webhooks []ValidatingWebhook,
) ([]admissionv1.ValidatingWebhook, error) {
	for i, webhook := range webhooks {
		if endpoint := webhook.ClientConfig.URL; endpoint != nil {
			parsed, err := url.Parse(*endpoint)
			if err != nil {
				return nil, fmt.Errorf("webhook[%d].client-config.url: %w", i, err)
			}
			if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
				parsed.RawQuery != "" || parsed.Fragment != "" {
				return nil, fmt.Errorf(
					"webhook[%d].client-config.url must use HTTPS and a host, "+
						"without user info, query, or fragment", i,
				)
			}
		}
	}
	return nativeValidatingWebhooks(webhooks), nil
}

func nativeValidatingWebhooks(webhooks []ValidatingWebhook) []admissionv1.ValidatingWebhook {
	out := make([]admissionv1.ValidatingWebhook, 0, len(webhooks))
	for _, webhook := range webhooks {
		clientConfig := admissionv1.WebhookClientConfig{
			CABundle: []byte(valueOr(webhook.ClientConfig.CABundle, "")),
			URL:      webhook.ClientConfig.URL,
		}
		if service := webhook.ClientConfig.Service; service != nil {
			clientConfig.Service = &admissionv1.ServiceReference{
				Name: service.Name, Namespace: service.Namespace,
				Port: new(int32(valueOr(service.Port, 443))),
			}
			if valueOr(service.Path, "") != "" {
				clientConfig.Service.Path = service.Path
			}
		}
		failurePolicy := admissionv1.FailurePolicyType(valueOr(webhook.FailurePolicy, "Fail"))
		matchPolicy := admissionv1.MatchPolicyType(valueOr(webhook.MatchPolicy, "Equivalent"))
		out = append(out, admissionv1.ValidatingWebhook{
			Name: webhook.Name, ClientConfig: clientConfig,
			AdmissionReviewVersions: append([]string{}, valueOr(webhook.AdmissionReviewVersions, nil)...),
			FailurePolicy:           new(failurePolicy),
			MatchPolicy:             new(matchPolicy),
			NamespaceSelector:       nativeWebhookSelector(webhook.NamespaceSelector),
			ObjectSelector:          nativeWebhookSelector(webhook.ObjectSelector),
			Rules:                   nativeWebhookRules(valueOr(webhook.Rule, nil)),
			SideEffects:             new(admissionv1.SideEffectClass(valueOr(webhook.SideEffects, ""))),
			TimeoutSeconds:          new(int32(valueOr(webhook.TimeoutSeconds, 10))),
		})
	}
	return out
}

func nativeWebhookRules(rules []WebhookRule) []admissionv1.RuleWithOperations {
	out := make([]admissionv1.RuleWithOperations, 0, len(rules))
	for _, rule := range rules {
		operations := make([]admissionv1.OperationType, 0, len(rule.Operations))
		for _, operation := range rule.Operations {
			operations = append(operations, admissionv1.OperationType(operation))
		}
		out = append(out, admissionv1.RuleWithOperations{
			Operations: operations,
			Rule: admissionv1.Rule{
				APIGroups: rule.APIGroups, APIVersions: rule.APIVersions, Resources: rule.Resources,
				Scope: new(admissionv1.ScopeType(valueOr(rule.Scope, "*"))),
			},
		})
	}
	return out
}

func nativeWebhookSelector(selector *WebhookSelector) *metav1.LabelSelector {
	if selector == nil {
		return nil
	}
	out := &metav1.LabelSelector{MatchLabels: valueOr(selector.MatchLabels, nil)}
	for _, expression := range valueOr(selector.MatchExpressions, nil) {
		out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      valueOr(expression.Key, ""),
			Operator: metav1.LabelSelectorOperator(valueOr(expression.Operator, "")),
			Values:   webhookSelectorValues(valueOr(expression.Values, nil)),
		})
	}
	return out
}

func projectValidatingWebhooks(webhooks []admissionv1.ValidatingWebhook) []ValidatingWebhookOutput {
	out := make([]ValidatingWebhookOutput, 0, len(webhooks))
	for _, webhook := range webhooks {
		clientConfig := WebhookClientConfig{URL: webhook.ClientConfig.URL}
		if len(webhook.ClientConfig.CABundle) > 0 {
			clientConfig.CABundle = new(string(webhook.ClientConfig.CABundle))
		}
		if service := webhook.ClientConfig.Service; service != nil {
			clientConfig.Service = &WebhookService{
				Name: service.Name, Namespace: service.Namespace, Path: service.Path,
				Port: optionalInteger(service.Port),
			}
		}
		out = append(out, ValidatingWebhookOutput{
			Name: webhook.Name, ClientConfig: clientConfig,
			AdmissionReviewVersions: append([]string{}, webhook.AdmissionReviewVersions...),
			FailurePolicy:           optionalString(webhook.FailurePolicy),
			MatchPolicy:             optionalString(webhook.MatchPolicy),
			NamespaceSelector:       projectWebhookSelector(webhook.NamespaceSelector),
			ObjectSelector:          projectWebhookSelector(webhook.ObjectSelector),
			Rule:                    projectWebhookRules(webhook.Rules),
			SideEffects:             optionalString(webhook.SideEffects),
			TimeoutSeconds:          optionalInteger(webhook.TimeoutSeconds),
		})
	}
	return out
}

func projectWebhookRules(rules []admissionv1.RuleWithOperations) []WebhookRule {
	out := make([]WebhookRule, 0, len(rules))
	for _, rule := range rules {
		operations := make([]string, 0, len(rule.Operations))
		for _, operation := range rule.Operations {
			operations = append(operations, string(operation))
		}
		out = append(out, WebhookRule{
			APIGroups:   append([]string{}, rule.APIGroups...),
			APIVersions: append([]string{}, rule.APIVersions...),
			Operations:  operations,
			Resources:   append([]string{}, rule.Resources...),
			Scope:       optionalString(rule.Scope),
		})
	}
	return out
}

func projectWebhookSelector(selector *metav1.LabelSelector) *WebhookSelectorOutput {
	if selector == nil || len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return nil
	}
	out := &WebhookSelectorOutput{
		MatchLabels:      selector.MatchLabels,
		MatchExpressions: make([]WebhookSelectorExpressionOutput, 0, len(selector.MatchExpressions)),
	}
	for _, expression := range selector.MatchExpressions {
		out.MatchExpressions = append(out.MatchExpressions, WebhookSelectorExpressionOutput{
			Key: expression.Key, Operator: string(expression.Operator),
			Values: webhookSelectorValues(expression.Values),
		})
	}
	if out.MatchLabels == nil {
		out.MatchLabels = map[string]string{}
	}
	return out
}

func webhookSelectorValues(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return slices.Compact(out)
}

func optionalString[T ~string](value *T) *string {
	if value == nil {
		return nil
	}
	return new(string(*value))
}

func optionalInteger(value *int32) *int64 {
	if value == nil {
		return nil
	}
	return new(int64(*value))
}
