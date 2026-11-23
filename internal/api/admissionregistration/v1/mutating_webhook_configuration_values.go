package v1

import (
	admissionv1 "k8s.io/api/admissionregistration/v1"
)

func prepareMutatingWebhooks(
	webhooks []MutatingWebhook,
) ([]admissionv1.MutatingWebhook, error) {
	for i, webhook := range webhooks {
		if err := checkWebhookURL(i, webhook.ClientConfig.URL); err != nil {
			return nil, err
		}
	}
	return nativeMutatingWebhooks(webhooks), nil
}

func nativeMutatingWebhooks(webhooks []MutatingWebhook) []admissionv1.MutatingWebhook {
	out := make([]admissionv1.MutatingWebhook, 0, len(webhooks))
	for _, webhook := range webhooks {
		failurePolicy := admissionv1.FailurePolicyType(valueOr(webhook.FailurePolicy, "Fail"))
		matchPolicy := admissionv1.MatchPolicyType(valueOr(webhook.MatchPolicy, "Equivalent"))
		reinvocationPolicy := admissionv1.ReinvocationPolicyType(
			valueOr(webhook.ReinvocationPolicy, "Never"),
		)
		out = append(out, admissionv1.MutatingWebhook{
			Name:         webhook.Name,
			ClientConfig: nativeWebhookClientConfig(webhook.ClientConfig),
			AdmissionReviewVersions: append(
				[]string{}, valueOr(webhook.AdmissionReviewVersions, nil)...,
			),
			FailurePolicy:      new(failurePolicy),
			MatchPolicy:        new(matchPolicy),
			NamespaceSelector:  nativeWebhookSelector(webhook.NamespaceSelector),
			ObjectSelector:     nativeWebhookSelector(webhook.ObjectSelector),
			ReinvocationPolicy: new(reinvocationPolicy),
			Rules:              nativeWebhookRules(valueOr(webhook.Rule, nil)),
			SideEffects:        new(admissionv1.SideEffectClass(valueOr(webhook.SideEffects, ""))),
			TimeoutSeconds:     new(int32(valueOr(webhook.TimeoutSeconds, 10))),
		})
	}
	return out
}

func projectMutatingWebhooks(webhooks []admissionv1.MutatingWebhook) []MutatingWebhookOutput {
	out := make([]MutatingWebhookOutput, 0, len(webhooks))
	for _, webhook := range webhooks {
		out = append(out, MutatingWebhookOutput{
			Name: webhook.Name, ClientConfig: projectWebhookClientConfig(webhook.ClientConfig),
			AdmissionReviewVersions: append([]string{}, webhook.AdmissionReviewVersions...),
			FailurePolicy:           optionalString(webhook.FailurePolicy),
			MatchPolicy:             optionalString(webhook.MatchPolicy),
			NamespaceSelector:       projectWebhookSelector(webhook.NamespaceSelector),
			ObjectSelector:          projectWebhookSelector(webhook.ObjectSelector),
			ReinvocationPolicy:      optionalString(webhook.ReinvocationPolicy),
			Rule:                    projectWebhookRules(webhook.Rules),
			SideEffects:             optionalString(webhook.SideEffects),
			TimeoutSeconds:          optionalInteger(webhook.TimeoutSeconds),
		})
	}
	return out
}
