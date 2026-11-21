package v1

import (
	"slices"

	rbacv1 "k8s.io/api/rbac/v1"
)

func roleSet(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return slices.Compact(out)
}

func normalizeRoleRules(input []RoleRule) []RoleRuleOutput {
	out := make([]RoleRuleOutput, len(input))
	for i, rule := range input {
		out[i] = RoleRuleOutput{
			APIGroups: roleSet(rule.APIGroups), Resources: roleSet(rule.Resources),
			Verbs: roleSet(rule.Verbs), ResourceNames: roleSet(roleValue(rule.ResourceNames)),
		}
	}
	return out
}

func canonicalRoleRules(input []RoleRuleOutput) []RoleRuleOutput {
	out := make([]RoleRuleOutput, len(input))
	for i, rule := range input {
		out[i] = RoleRuleOutput{
			APIGroups: roleSet(rule.APIGroups), Resources: roleSet(rule.Resources),
			Verbs: roleSet(rule.Verbs), ResourceNames: roleSet(rule.ResourceNames),
		}
	}
	return out
}

func nativeRoleRules(input []RoleRuleOutput) []rbacv1.PolicyRule {
	out := make([]rbacv1.PolicyRule, len(input))
	for i, rule := range input {
		out[i] = rbacv1.PolicyRule{
			APIGroups: rule.APIGroups, Resources: rule.Resources, Verbs: rule.Verbs,
			ResourceNames: rule.ResourceNames,
		}
	}
	return out
}

func projectRoleRules(input []rbacv1.PolicyRule) []RoleRuleOutput {
	out := make([]RoleRuleOutput, len(input))
	for i, rule := range input {
		out[i] = RoleRuleOutput{
			APIGroups: roleSet(rule.APIGroups), Resources: roleSet(rule.Resources),
			Verbs: roleSet(rule.Verbs), ResourceNames: roleSet(rule.ResourceNames),
		}
	}
	return out
}
