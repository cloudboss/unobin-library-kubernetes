package v1

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func pdbValue[T any](input *T) T {
	if input != nil {
		return *input
	}
	var zero T
	return zero
}

func pdbMap(input *map[string]string) map[string]string {
	if input == nil || len(*input) == 0 {
		return map[string]string{}
	}
	return maps.Clone(*input)
}

func normalizePDBMetadata(input PodDisruptionBudgetMetadata) PodDisruptionBudgetMetadataOutput {
	namespace := "default"
	if input.Namespace != nil {
		namespace = *input.Namespace
	}
	return PodDisruptionBudgetMetadataOutput{
		Name: pdbValue(input.Name), GenerateName: pdbValue(input.GenerateName), Namespace: namespace,
		Annotations: pdbMap(input.Annotations), Labels: pdbMap(input.Labels),
	}
}

func normalizePDBSpec(input PodDisruptionBudgetSpec) PodDisruptionBudgetSpecOutput {
	out := PodDisruptionBudgetSpecOutput{
		MinAvailable: pdbValue(input.MinAvailable), MaxUnavailable: pdbValue(input.MaxUnavailable),
		Selector: PodDisruptionBudgetSelectorOutput{MatchLabels: pdbMap(input.Selector.MatchLabels),
			MatchExpressions: []PodDisruptionBudgetExpressionOutput{}},
	}
	for _, expression := range pdbValue(input.Selector.MatchExpressions) {
		values := append([]string{}, pdbValue(expression.Values)...)
		slices.Sort(values)
		out.Selector.MatchExpressions = append(out.Selector.MatchExpressions,
			PodDisruptionBudgetExpressionOutput{Key: pdbValue(expression.Key),
				Operator: pdbValue(expression.Operator), Values: slices.Compact(values)})
	}
	return out
}

func equalPDBSpec(before, after PodDisruptionBudgetSpec) bool {
	return reflect.DeepEqual(normalizePDBSpec(before), normalizePDBSpec(after))
}

func pdbAvailability(input string) (*intstr.IntOrString, error) {
	if input == "" {
		return nil, nil
	}
	if strings.HasSuffix(input, "%") {
		value := intstr.FromString(input)
		return &value, nil
	}
	value, err := strconv.ParseInt(input, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("convert PodDisruptionBudget availability: %w", err)
	}
	converted := intstr.FromInt32(int32(value))
	return &converted, nil
}

func nativePDBSpec(input PodDisruptionBudgetSpec) (policyv1.PodDisruptionBudgetSpec, error) {
	normalized := normalizePDBSpec(input)
	minimum, err := pdbAvailability(normalized.MinAvailable)
	if err != nil {
		return policyv1.PodDisruptionBudgetSpec{}, err
	}
	maximum, err := pdbAvailability(normalized.MaxUnavailable)
	if err != nil {
		return policyv1.PodDisruptionBudgetSpec{}, err
	}
	selector := &metav1.LabelSelector{MatchLabels: normalized.Selector.MatchLabels}
	for _, expression := range normalized.Selector.MatchExpressions {
		selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: expression.Key, Operator: metav1.LabelSelectorOperator(expression.Operator),
			Values: expression.Values,
		})
	}
	return policyv1.PodDisruptionBudgetSpec{
		MinAvailable: minimum, MaxUnavailable: maximum, Selector: selector,
	}, nil
}

func projectPDBSpec(input policyv1.PodDisruptionBudgetSpec) PodDisruptionBudgetSpecOutput {
	converted := PodDisruptionBudgetSpec{}
	if input.MinAvailable != nil {
		converted.MinAvailable = new(input.MinAvailable.String())
	}
	if input.MaxUnavailable != nil {
		converted.MaxUnavailable = new(input.MaxUnavailable.String())
	}
	if input.Selector != nil {
		converted.Selector.MatchLabels = &input.Selector.MatchLabels
		expressions := []PodDisruptionBudgetExpression{}
		for _, expression := range input.Selector.MatchExpressions {
			expressions = append(expressions, PodDisruptionBudgetExpression{
				Key: &expression.Key, Operator: new(string(expression.Operator)),
				Values: &expression.Values,
			})
		}
		converted.Selector.MatchExpressions = &expressions
	}
	return normalizePDBSpec(converted)
}
