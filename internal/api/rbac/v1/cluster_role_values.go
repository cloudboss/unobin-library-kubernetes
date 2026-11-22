package v1

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func clusterRoleList(input []string) []string {
	return append([]string{}, input...)
}

func normalizeClusterRoleRules(input *[]ClusterRoleRule) []ClusterRoleRuleOutput {
	out := make([]ClusterRoleRuleOutput, len(roleValue(input)))
	for i, rule := range roleValue(input) {
		out[i] = ClusterRoleRuleOutput{Verbs: clusterRoleList(rule.Verbs),
			APIGroups:       clusterRoleList(roleValue(rule.APIGroups)),
			Resources:       clusterRoleList(roleValue(rule.Resources)),
			ResourceNames:   clusterRoleList(roleValue(rule.ResourceNames)),
			NonResourceUrls: clusterRoleList(roleValue(rule.NonResourceUrls))}
	}
	return out
}

func nativeClusterRoleRules(input []ClusterRoleRuleOutput) []rbacv1.PolicyRule {
	out := make([]rbacv1.PolicyRule, len(input))
	for i, rule := range input {
		out[i] = rbacv1.PolicyRule{Verbs: rule.Verbs, APIGroups: rule.APIGroups,
			Resources: rule.Resources, ResourceNames: rule.ResourceNames,
			NonResourceURLs: rule.NonResourceUrls}
	}
	return out
}

func projectClusterRoleRules(input []rbacv1.PolicyRule) []ClusterRoleRuleOutput {
	out := make([]ClusterRoleRuleOutput, len(input))
	for i, rule := range input {
		out[i] = ClusterRoleRuleOutput{Verbs: clusterRoleList(rule.Verbs),
			APIGroups: clusterRoleList(rule.APIGroups), Resources: clusterRoleList(rule.Resources),
			ResourceNames:   clusterRoleList(rule.ResourceNames),
			NonResourceUrls: clusterRoleList(rule.NonResourceURLs)}
	}
	return out
}

func normalizeClusterRoleAggregation(input *ClusterRoleAggregation) *ClusterRoleAggregationOutput {
	if input == nil {
		return nil
	}
	out := &ClusterRoleAggregationOutput{
		ClusterRoleSelectors: make([]ClusterRoleSelectorOutput,
			len(roleValue(input.ClusterRoleSelectors))),
	}
	for i, selector := range roleValue(input.ClusterRoleSelectors) {
		item := ClusterRoleSelectorOutput{MatchLabels: roleMap(selector.MatchLabels),
			MatchExpressions: make([]ClusterRoleExpressionOutput, len(roleValue(selector.MatchExpressions)))}
		for j, expression := range roleValue(selector.MatchExpressions) {
			item.MatchExpressions[j] = ClusterRoleExpressionOutput{Key: roleValue(expression.Key),
				Operator: roleValue(expression.Operator), Values: roleSet(roleValue(expression.Values))}
		}
		out.ClusterRoleSelectors[i] = item
	}
	return out
}

func nativeClusterRoleAggregation(input *ClusterRoleAggregationOutput) *rbacv1.AggregationRule {
	if input == nil {
		return nil
	}
	out := &rbacv1.AggregationRule{ClusterRoleSelectors: make([]metav1.LabelSelector,
		len(input.ClusterRoleSelectors))}
	for i, selector := range input.ClusterRoleSelectors {
		item := metav1.LabelSelector{MatchLabels: selector.MatchLabels,
			MatchExpressions: make([]metav1.LabelSelectorRequirement, len(selector.MatchExpressions))}
		for j, expression := range selector.MatchExpressions {
			item.MatchExpressions[j] = metav1.LabelSelectorRequirement{Key: expression.Key,
				Operator: metav1.LabelSelectorOperator(expression.Operator), Values: expression.Values}
		}
		out.ClusterRoleSelectors[i] = item
	}
	return out
}

func projectClusterRoleAggregation(input *rbacv1.AggregationRule) *ClusterRoleAggregationOutput {
	if input == nil {
		return nil
	}
	out := ClusterRoleAggregation{ClusterRoleSelectors: &[]ClusterRoleSelector{}}
	for _, selector := range input.ClusterRoleSelectors {
		item := ClusterRoleSelector{MatchLabels: &selector.MatchLabels,
			MatchExpressions: &[]ClusterRoleExpression{}}
		for _, expression := range selector.MatchExpressions {
			operator := string(expression.Operator)
			*item.MatchExpressions = append(*item.MatchExpressions, ClusterRoleExpression{
				Key: &expression.Key, Operator: &operator, Values: &expression.Values})
		}
		*out.ClusterRoleSelectors = append(*out.ClusterRoleSelectors, item)
	}
	return normalizeClusterRoleAggregation(&out)
}

func clusterRoleRulePatch(before, after []ClusterRoleRuleOutput) []rolePatchOperation {
	operations := []rolePatchOperation{}
	for i := len(before) - len(after); i > 0; i-- {
		operations = append(operations, rolePatchOperation{Op: "remove",
			Path: fmt.Sprintf("/rules/%d", i)})
	}
	native := nativeClusterRoleRules(after)
	for i, rule := range native {
		op := "replace"
		if i >= len(before) {
			op = "add"
		}
		operations = append(operations, rolePatchOperation{Op: op, Path: fmt.Sprintf("/rules/%d",
			i), Value: rule})
	}
	return operations
}

func projectClusterRoleMetadata(observed, tracked map[string]string,
	expressions *[]string,
) (map[string]string, error) {
	patterns := make([]*regexp.Regexp, 0, len(roleValue(expressions)))
	for _, expression := range roleValue(expressions) {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pattern)
	}
	out := map[string]string{}
	for key, value := range observed {
		if _, present := tracked[key]; present {
			out[key] = value
			continue
		}
		if internalClusterRoleKey(key) {
			continue
		}
		ignored := false
		for _, pattern := range patterns {
			ignored = ignored || pattern.MatchString(key)
		}
		if !ignored {
			out[key] = value
		}
	}
	return out, nil
}

func internalClusterRoleKey(key string) bool {
	parsed, err := url.Parse("//" + key)
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
