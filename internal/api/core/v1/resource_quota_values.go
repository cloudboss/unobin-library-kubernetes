package v1

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *ResourceQuotaResource) prepare() (
	*corev1.ResourceQuota, time.Duration, time.Duration, error,
) {
	createTimeout, err := quotaTimeout(r.CreateTimeout)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("parse create-timeout: %w", err)
	}
	updateTimeout, err := quotaTimeout(r.UpdateTimeout)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("parse update-timeout: %w", err)
	}
	spec, err := prepareQuotaSpec(r.Spec)
	if err != nil {
		return nil, 0, 0, err
	}
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name: serviceValue(r.Metadata.Name), GenerateName: serviceValue(r.Metadata.GenerateName),
			Namespace:   serviceDefault(r.Metadata.Namespace, "default"),
			Annotations: serviceMap(r.Metadata.Annotations), Labels: serviceMap(r.Metadata.Labels),
		},
		Spec: spec,
	}, createTimeout, updateTimeout, nil
}

func prepareQuotaSpec(input *ResourceQuotaSpec) (corev1.ResourceQuotaSpec, error) {
	spec := serviceValue(input)
	hard, err := pvcQuantities(spec.Hard)
	if err != nil {
		return corev1.ResourceQuotaSpec{}, fmt.Errorf("ResourceQuota hard: %w", err)
	}
	result := corev1.ResourceQuotaSpec{Hard: hard}
	for _, scope := range pvcSet(serviceValue(spec.Scopes)) {
		result.Scopes = append(result.Scopes, corev1.ResourceQuotaScope(scope))
	}
	if spec.ScopeSelector != nil {
		result.ScopeSelector = &corev1.ScopeSelector{}
		for _, expression := range serviceValue(spec.ScopeSelector.MatchExpressions) {
			requirement := corev1.ScopedResourceSelectorRequirement{
				ScopeName: corev1.ResourceQuotaScope(expression.ScopeName),
				Operator:  corev1.ScopeSelectorOperator(expression.Operator),
				Values:    pvcSet(serviceValue(expression.Values)),
			}
			result.ScopeSelector.MatchExpressions = append(
				result.ScopeSelector.MatchExpressions, requirement)
		}
	}
	return result, nil
}

func quotaHardEqual(a, b corev1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}
	for key, quantity := range a {
		other, ok := b[key]
		if !ok || quantity.Cmp(other) != 0 {
			return false
		}
	}
	return true
}

func quotaSpecChanged(before ResourceQuotaSpecOutput, desired corev1.ResourceQuotaSpec) bool {
	hard, err := pvcQuantities(&before.Hard)
	if err != nil || !quotaHardEqual(hard, desired.Hard) {
		return true
	}
	after := quotaSpecOutput(desired)
	return !slices.Equal(before.Scopes, after.Scopes) ||
		!reflect.DeepEqual(before.ScopeSelector, after.ScopeSelector)
}

func quotaSpecOutput(spec corev1.ResourceQuotaSpec) ResourceQuotaSpecOutput {
	out := ResourceQuotaSpecOutput{
		Hard:   pvcQuantityStrings(spec.Hard),
		Scopes: make([]string, 0, len(spec.Scopes)),
	}
	for _, scope := range spec.Scopes {
		out.Scopes = append(out.Scopes, string(scope))
	}
	out.Scopes = pvcSet(out.Scopes)
	if spec.ScopeSelector != nil {
		out.ScopeSelector = &ResourceQuotaScopeSelectorOutput{
			MatchExpressions: make([]ResourceQuotaScopeExpression, 0,
				len(spec.ScopeSelector.MatchExpressions)),
		}
		for _, requirement := range spec.ScopeSelector.MatchExpressions {
			expression := ResourceQuotaScopeExpression{
				ScopeName: string(requirement.ScopeName), Operator: string(requirement.Operator),
			}
			if values := pvcSet(requirement.Values); len(values) != 0 {
				expression.Values = &values
			}
			out.ScopeSelector.MatchExpressions = append(out.ScopeSelector.MatchExpressions, expression)
		}
	}
	return out
}

func (r *ResourceQuotaResource) output(
	live *corev1.ResourceQuota, server string, tracked ServiceMetadataOutput,
) *ResourceQuotaResourceOutput {
	prefix := live.GenerateName
	if prefix == "" {
		prefix = tracked.GenerateName
	}
	return &ResourceQuotaResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ResourceQuota", Name: live.Name,
		Namespace: live.Namespace, UID: string(live.UID),
		Metadata: ServiceMetadataOutput{
			Name: live.Name, Namespace: live.Namespace, GenerateName: prefix,
			Annotations: pvMetadata(live.Annotations, tracked.Annotations, r.IgnoreAnnotations),
			Labels:      pvMetadata(live.Labels, tracked.Labels, r.IgnoreLabels),
		},
		Spec: quotaSpecOutput(live.Spec),
	}
}
