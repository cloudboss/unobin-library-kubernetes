package v1

import (
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nativePolicySpec(input PolicySpec) admissionv1.ValidatingAdmissionPolicySpec {
	out := admissionv1.ValidatingAdmissionPolicySpec{
		FailurePolicy:    new(admissionv1.FailurePolicyType(input.FailurePolicy)),
		MatchConstraints: nativePolicyMatch(input.MatchConstraints),
		AuditAnnotations: policyList(input.AuditAnnotations,
			func(item PolicyAuditAnnotation) admissionv1.AuditAnnotation {
				return admissionv1.AuditAnnotation{
					Key: item.Key, ValueExpression: item.ValueExpression,
				}
			}),
		MatchConditions: policyList(policyValue(input.MatchConditions),
			func(item PolicyMatchCondition) admissionv1.MatchCondition {
				return admissionv1.MatchCondition{Name: item.Name, Expression: item.Expression}
			}),
		Validations: policyList(policyValue(input.Validations),
			func(item PolicyValidation) admissionv1.Validation {
				return admissionv1.Validation{
					Expression: item.Expression, Message: item.Message,
					MessageExpression: policyValue(item.MessageExpression),
				}
			}),
		Variables: policyList(policyValue(input.Variables),
			func(item PolicyVariable) admissionv1.Variable {
				return admissionv1.Variable{
					Name: policyValue(item.Name), Expression: policyValue(item.Expression),
				}
			}),
	}
	if input.ParamKind != nil {
		out.ParamKind = &admissionv1.ParamKind{
			APIVersion: input.ParamKind.APIVersion, Kind: input.ParamKind.Kind,
		}
	}
	return out
}

func nativePolicyMatch(input PolicyMatchConstraints) *admissionv1.MatchResources {
	return &admissionv1.MatchResources{
		MatchPolicy:          (*admissionv1.MatchPolicyType)(input.MatchPolicy),
		NamespaceSelector:    nativePolicySelector(input.NamespaceSelector),
		ObjectSelector:       nativePolicySelector(input.ObjectSelector),
		ResourceRules:        policyList(policyValue(input.ResourceRules), nativePolicyRule),
		ExcludeResourceRules: policyList(policyValue(input.ExcludeResourceRules), nativePolicyRule),
	}
}

func nativePolicySelector(input *PolicySelector) *metav1.LabelSelector {
	if input == nil {
		return nil
	}
	return &metav1.LabelSelector{
		MatchLabels: policyValue(input.MatchLabels),
		MatchExpressions: policyList(policyValue(input.MatchExpressions),
			func(item PolicySelectorExpression) metav1.LabelSelectorRequirement {
				return metav1.LabelSelectorRequirement{
					Key:    policyValue(item.Key),
					Values: policyValue(item.Values),
					Operator: metav1.LabelSelectorOperator(
						policyValue(item.Operator),
					),
				}
			}),
	}
}

func nativePolicyRule(input PolicyResourceRule) admissionv1.NamedRuleWithOperations {
	return admissionv1.NamedRuleWithOperations{
		ResourceNames: policyValue(input.ResourceNames),
		RuleWithOperations: admissionv1.RuleWithOperations{
			Operations: policyList(input.Operations,
				func(operation string) admissionv1.OperationType {
					return admissionv1.OperationType(operation)
				}),
			Rule: admissionv1.Rule{
				APIGroups: input.APIGroups, APIVersions: input.APIVersions,
				Resources: input.Resources, Scope: (*admissionv1.ScopeType)(input.Scope),
			},
		},
	}
}

func policyList[In, Out any](input []In, convert func(In) Out) []Out {
	var output []Out
	if len(input) != 0 {
		output = make([]Out, len(input))
		for i, item := range input {
			output[i] = convert(item)
		}
	}
	return output
}
