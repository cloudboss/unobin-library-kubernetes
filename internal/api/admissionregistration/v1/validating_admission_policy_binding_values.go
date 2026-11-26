package v1

import (
	"maps"
	"slices"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nativeBindingSpec(input BindingSpec) admissionv1.ValidatingAdmissionPolicyBindingSpec {
	out := admissionv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName: input.PolicyName,
		ValidationActions: policyList(input.ValidationActions,
			func(v string) admissionv1.ValidationAction {
				return admissionv1.ValidationAction(v)
			}),
	}
	if p := input.ParamRef; p != nil {
		out.ParamRef = &admissionv1.ParamRef{
			Name: policyValue(p.Name), Namespace: policyValue(p.Namespace),
			Selector:                nativeBindingSelector(p.Selector),
			ParameterNotFoundAction: new(admissionv1.ParameterNotFoundActionType(p.ParameterNotFoundAction)),
		}
	}
	if m := input.MatchResources; m != nil {
		out.MatchResources = &admissionv1.MatchResources{
			MatchPolicy:          (*admissionv1.MatchPolicyType)(m.MatchPolicy),
			NamespaceSelector:    nativeBindingSelector(m.NamespaceSelector),
			ObjectSelector:       nativeBindingSelector(m.ObjectSelector),
			ResourceRules:        policyList(policyValue(m.ResourceRules), nativePolicyRule),
			ExcludeResourceRules: policyList(policyValue(m.ExcludeResourceRules), nativePolicyRule),
		}
	}
	return out
}

func nativeBindingSelector(input *BindingSelector) *metav1.LabelSelector {
	if input == nil {
		return nil
	}
	return &metav1.LabelSelector{
		MatchLabels: maps.Clone(policyValue(input.MatchLabels)),
		MatchExpressions: policyList(policyValue(input.MatchExpressions),
			func(e BindingSelectorExpression) metav1.LabelSelectorRequirement {
				return metav1.LabelSelectorRequirement{
					Key: e.Key, Operator: metav1.LabelSelectorOperator(e.Operator),
					Values: slices.Clone(policyValue(e.Values)),
				}
			}),
	}
}

func projectBindingSpec(input admissionv1.ValidatingAdmissionPolicyBindingSpec) BindingSpec {
	out := BindingSpec{
		PolicyName: input.PolicyName,
		ValidationActions: policyList(input.ValidationActions,
			func(v admissionv1.ValidationAction) string {
				return string(v)
			}),
	}
	slices.Sort(out.ValidationActions)
	if p := input.ParamRef; p != nil {
		out.ParamRef = &BindingParamRef{
			Selector:                projectBindingSelector(p.Selector),
			ParameterNotFoundAction: string(policyValue(p.ParameterNotFoundAction)),
		}
		if p.Name != "" {
			out.ParamRef.Name = new(p.Name)
		}
		if p.Namespace != "" {
			out.ParamRef.Namespace = new(p.Namespace)
		}
	}
	if m := input.MatchResources; m != nil {
		match := &BindingMatchResources{
			MatchPolicy:          new(string(policyValue(m.MatchPolicy))),
			NamespaceSelector:    projectBindingSelector(m.NamespaceSelector),
			ObjectSelector:       projectBindingSelector(m.ObjectSelector),
			ResourceRules:        projectBindingRules(m.ResourceRules),
			ExcludeResourceRules: projectBindingRules(m.ExcludeResourceRules),
		}
		if m.MatchPolicy == nil {
			match.MatchPolicy = new("Equivalent")
		}
		if emptyBindingSelector(match.NamespaceSelector) {
			match.NamespaceSelector = nil
		}
		if emptyBindingSelector(match.ObjectSelector) {
			match.ObjectSelector = nil
		}
		if *match.MatchPolicy != "Equivalent" || match.NamespaceSelector != nil ||
			match.ObjectSelector != nil || match.ResourceRules != nil || match.ExcludeResourceRules != nil {
			out.MatchResources = match
		}
	}
	return out
}

func projectBindingSelector(input *metav1.LabelSelector) *BindingSelector {
	if input == nil {
		return nil
	}
	out := &BindingSelector{}
	if len(input.MatchLabels) != 0 {
		out.MatchLabels = new(maps.Clone(input.MatchLabels))
	}
	if len(input.MatchExpressions) != 0 {
		out.MatchExpressions = new(policyList(input.MatchExpressions,
			func(e metav1.LabelSelectorRequirement) BindingSelectorExpression {
				out := BindingSelectorExpression{Key: e.Key, Operator: string(e.Operator)}
				if len(e.Values) != 0 {
					out.Values = new(slices.Clone(e.Values))
				}
				return out
			}))
	}
	return out
}

func emptyBindingSelector(input *BindingSelector) bool {
	return input == nil || input.MatchLabels == nil && input.MatchExpressions == nil
}

func projectBindingRules(input []admissionv1.NamedRuleWithOperations) *[]PolicyResourceRule {
	if len(input) == 0 {
		return nil
	}
	return new(policyList(input, func(r admissionv1.NamedRuleWithOperations) PolicyResourceRule {
		out := PolicyResourceRule{
			APIGroups: slices.Clone(r.APIGroups), APIVersions: slices.Clone(r.APIVersions),
			Resources: slices.Clone(r.Resources), Scope: new("*"),
			Operations: policyList(r.Operations, func(v admissionv1.OperationType) string {
				return string(v)
			}),
		}
		if r.Scope != nil {
			out.Scope = new(string(*r.Scope))
		}
		if len(r.ResourceNames) != 0 {
			out.ResourceNames = new(slices.Clone(r.ResourceNames))
		}
		return out
	}))
}
