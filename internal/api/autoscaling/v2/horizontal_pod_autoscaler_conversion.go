package v2

import (
	"fmt"
	"maps"
	"slices"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hpaValue[T any](p *T) T {
	if p != nil {
		return *p
	}
	var zero T
	return zero
}

func hpaMap(p *map[string]string) map[string]string {
	out := maps.Clone(hpaValue(p))
	if out == nil {
		out = map[string]string{}
	}
	return out
}

func hpaMin(p *int) int {
	if p == nil {
		return 1
	}
	return *p
}

func hpaMetadata(m HPAMetadata) HPAMetadataOutput {
	ns := "default"
	if m.Namespace != nil {
		ns = *m.Namespace
	}
	return HPAMetadataOutput{Name: hpaValue(m.Name), GenerateName: hpaValue(m.GenerateName),
		Namespace:   ns,
		Annotations: hpaMap(m.Annotations), Labels: hpaMap(m.Labels)}
}

func expandHPASpec(input HPASpec) (autoscalingv2.HorizontalPodAutoscalerSpec, error) {
	out := autoscalingv2.HorizontalPodAutoscalerSpec{
		MaxReplicas: int32(input.MaxReplicas),
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
			APIVersion: hpaValue(input.ScaleTargetRef.APIVersion), Kind: input.ScaleTargetRef.Kind,
			Name: input.ScaleTargetRef.Name},
		Behavior: expandHPABehavior(input.Behavior),
	}
	if min := hpaMin(input.MinReplicas); min > 0 {
		out.MinReplicas = new(int32(min))
	}
	var err error
	out.Metrics, err = expandHPAMetrics(hpaValue(input.Metric))
	return out, err
}

func expandHPATarget(input *HPAMetricTarget) (autoscalingv2.MetricTarget, error) {
	if input == nil {
		return autoscalingv2.MetricTarget{}, nil
	}
	out := autoscalingv2.MetricTarget{Type: autoscalingv2.MetricTargetType(input.Type)}
	switch input.Type {
	case "Utilization":
		if v := hpaValue(input.AverageUtilization); v > 0 {
			out.AverageUtilization = new(int32(v))
		}
	case "Value", "AverageValue":
		text := hpaValue(input.Value)
		if input.Type == "AverageValue" {
			text = hpaValue(input.AverageValue)
		}
		if text != "" && text != "0" {
			q, err := resource.ParseQuantity(text)
			if err != nil {
				return out, fmt.Errorf("parse %s target quantity: %w", input.Type, err)
			}
			if input.Type == "Value" {
				out.Value = &q
			} else {
				out.AverageValue = &q
			}
		}
	}
	return out, nil
}

func projectHPATarget(input autoscalingv2.MetricTarget) (*HPAMetricTarget, error) {
	out := &HPAMetricTarget{Type: string(input.Type)}
	switch input.Type {
	case "Utilization":
		if input.AverageUtilization == nil {
			return nil, fmt.Errorf("remote Utilization target has no averageUtilization")
		}
		out.AverageUtilization = new(int(*input.AverageUtilization))
	case "Value":
		if input.Value == nil {
			return nil, fmt.Errorf("remote Value target has no value")
		}
		out.Value = new(input.Value.String())
	case "AverageValue":
		if input.AverageValue == nil {
			return nil, fmt.Errorf("remote AverageValue target has no averageValue")
		}
		out.AverageValue = new(input.AverageValue.String())
	}
	return out, nil
}

func expandHPAIdentifier(input HPAMetricIdentifier) autoscalingv2.MetricIdentifier {
	out := autoscalingv2.MetricIdentifier{Name: input.Name}
	if selectors := hpaValue(input.Selector); len(selectors) == 1 {
		s := selectors[0]
		out.Selector = &metav1.LabelSelector{MatchLabels: hpaMap(s.MatchLabels)}
		for _, e := range hpaValue(s.MatchExpressions) {
			values := slices.Clone(hpaValue(e.Values))
			slices.Sort(values)
			values = slices.Compact(values)
			out.Selector.MatchExpressions = append(out.Selector.MatchExpressions,
				metav1.LabelSelectorRequirement{
					Key: hpaValue(e.Key), Operator: metav1.LabelSelectorOperator(hpaValue(e.Operator)),
					Values: values})
		}
	}
	return out
}

func projectHPAIdentifier(input autoscalingv2.MetricIdentifier) HPAMetricIdentifier {
	out := HPAMetricIdentifier{Name: input.Name}
	if input.Selector != nil {
		s := HPASelector{MatchLabels: new(maps.Clone(input.Selector.MatchLabels))}
		expressions := []HPASelectorExpression{}
		for _, e := range input.Selector.MatchExpressions {
			values := slices.Clone(e.Values)
			slices.Sort(values)
			values = slices.Compact(values)
			expressions = append(expressions, HPASelectorExpression{Key: new(e.Key), Operator: new(
				string(e.Operator)), Values: &values})
		}
		s.MatchExpressions = &expressions
		out.Selector = &[]HPASelector{s}
	}
	return out
}

func expandHPAMetrics(input []HPAMetric) ([]autoscalingv2.MetricSpec, error) {
	out := make([]autoscalingv2.MetricSpec, 0, len(input))
	for i, m := range input {
		n := autoscalingv2.MetricSpec{Type: autoscalingv2.MetricSourceType(m.Type)}
		if m.Resource != nil {
			t, err := expandHPATarget(m.Resource.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].resource: %w", i, err)
			}
			n.Resource = &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceName(m.Resource.Name),
				Target: t}
		}
		if m.ContainerResource != nil {
			t, err := expandHPATarget(m.ContainerResource.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].container-resource: %w", i, err)
			}
			n.ContainerResource = &autoscalingv2.ContainerResourceMetricSource{
				Name:      corev1.ResourceName(m.ContainerResource.Name),
				Container: m.ContainerResource.Container, Target: t}
		}
		if m.External != nil {
			t, err := expandHPATarget(m.External.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].external: %w", i, err)
			}
			n.External = &autoscalingv2.ExternalMetricSource{Metric: expandHPAIdentifier(
				m.External.Metric), Target: t}
		}
		if m.Pods != nil {
			t, err := expandHPATarget(m.Pods.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].pods: %w", i, err)
			}
			n.Pods = &autoscalingv2.PodsMetricSource{Metric: expandHPAIdentifier(m.Pods.Metric), Target: t}
		}
		if m.Object != nil {
			t, err := expandHPATarget(m.Object.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].object: %w", i, err)
			}
			d := m.Object.DescribedObject
			n.Object = &autoscalingv2.ObjectMetricSource{Metric: expandHPAIdentifier(m.Object.Metric),
				Target: t,
				DescribedObject: autoscalingv2.CrossVersionObjectReference{APIVersion: d.APIVersion,
					Kind: d.Kind, Name: d.Name}}
		}
		out = append(out, n)
	}
	return out, nil
}

func projectHPAMetrics(input []autoscalingv2.MetricSpec) ([]HPAMetric, error) {
	out := make([]HPAMetric, 0, len(input))
	for i, n := range input {
		m := HPAMetric{Type: string(n.Type)}
		if n.Resource != nil {
			t, err := projectHPATarget(n.Resource.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].resource: %w", i, err)
			}
			m.Resource = &HPAResource{Name: string(n.Resource.Name), Target: t}
		}
		if n.ContainerResource != nil {
			t, err := projectHPATarget(n.ContainerResource.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].container-resource: %w", i, err)
			}
			m.ContainerResource = &HPAContainerResource{Name: string(n.ContainerResource.Name),
				Container: n.ContainerResource.Container, Target: t}
		}
		if n.External != nil {
			t, err := projectHPATarget(n.External.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].external: %w", i, err)
			}
			m.External = &HPAExternal{Metric: projectHPAIdentifier(n.External.Metric), Target: t}
		}
		if n.Pods != nil {
			t, err := projectHPATarget(n.Pods.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].pods: %w", i, err)
			}
			m.Pods = &HPAExternal{Metric: projectHPAIdentifier(n.Pods.Metric), Target: t}
		}
		if n.Object != nil {
			t, err := projectHPATarget(n.Object.Target)
			if err != nil {
				return nil, fmt.Errorf("metric[%d].object: %w", i, err)
			}
			d := n.Object.DescribedObject
			m.Object = &HPAObjectMetric{Metric: projectHPAIdentifier(n.Object.Metric), Target: t,
				DescribedObject: HPADescribedObject{APIVersion: d.APIVersion, Kind: d.Kind, Name: d.Name}}
		}
		out = append(out, m)
	}
	return out, nil
}

func expandHPABehavior(input *HPABehavior) *autoscalingv2.HorizontalPodAutoscalerBehavior {
	out := &autoscalingv2.HorizontalPodAutoscalerBehavior{}
	if input != nil {
		out.ScaleUp = expandHPARules(input.ScaleUp)
		out.ScaleDown = expandHPARules(input.ScaleDown)
	}
	return out
}

func expandHPARules(input *[]HPARule) *autoscalingv2.HPAScalingRules {
	out := &autoscalingv2.HPAScalingRules{}
	if rules := hpaValue(input); len(rules) > 0 {
		r := rules[0]
		out.SelectPolicy = new(autoscalingv2.ScalingPolicySelect(hpaValue(r.SelectPolicy)))
		out.StabilizationWindowSeconds = new(int32(hpaValue(r.StabilizationWindowSeconds)))
		for _, p := range r.Policy {
			out.Policies = append(out.Policies, autoscalingv2.HPAScalingPolicy{
				Type: autoscalingv2.HPAScalingPolicyType(p.Type), PeriodSeconds: int32(p.PeriodSeconds),
				Value: int32(p.Value)})
		}
	}
	return out
}

func projectHPARules(input *autoscalingv2.HPAScalingRules) *[]HPARule {
	if input == nil {
		return nil
	}
	r := HPARule{Policy: []HPAPolicy{}}
	if input.SelectPolicy != nil {
		r.SelectPolicy = new(string(*input.SelectPolicy))
	}
	if input.StabilizationWindowSeconds != nil {
		r.StabilizationWindowSeconds = new(int(*input.StabilizationWindowSeconds))
	}
	for _, p := range input.Policies {
		r.Policy = append(r.Policy, HPAPolicy{Type: string(p.Type), PeriodSeconds: int(
			p.PeriodSeconds), Value: int(p.Value)})
	}
	return &[]HPARule{r}
}

func projectHPASpec(input autoscalingv2.HorizontalPodAutoscalerSpec) (HPASpecOutput, error) {
	out := HPASpecOutput{MaxReplicas: int(input.MaxReplicas), ScaleTargetRef: HPATargetRef{
		Kind: input.ScaleTargetRef.Kind, Name: input.ScaleTargetRef.Name}}
	if input.ScaleTargetRef.APIVersion != "" {
		out.ScaleTargetRef.APIVersion = new(input.ScaleTargetRef.APIVersion)
	}
	if input.MinReplicas != nil {
		out.MinReplicas = new(int(*input.MinReplicas))
	}
	metrics, err := projectHPAMetrics(input.Metrics)
	if err != nil {
		return out, err
	}
	out.Metric = &metrics
	if input.Behavior != nil {
		out.Behavior = &HPABehavior{ScaleUp: projectHPARules(input.Behavior.ScaleUp),
			ScaleDown: projectHPARules(input.Behavior.ScaleDown)}
	}
	return out, nil
}

func restoreHPAMetrics(input []HPAMetric) ([]autoscalingv2.MetricSpec, error) {
	out, err := expandHPAMetrics(input)
	if err != nil {
		return nil, err
	}
	for i, m := range input {
		if m.Resource != nil {
			out[i].Resource.Target, err = restoreHPATarget(m.Resource.Target)
			if err != nil {
				return nil, err
			}
		}
		if m.ContainerResource != nil {
			out[i].ContainerResource.Target, err = restoreHPATarget(m.ContainerResource.Target)
			if err != nil {
				return nil, err
			}
		}
		if m.External != nil {
			out[i].External.Target, err = restoreHPATarget(m.External.Target)
			if err != nil {
				return nil, err
			}
		}
		if m.Pods != nil {
			out[i].Pods.Target, err = restoreHPATarget(m.Pods.Target)
			if err != nil {
				return nil, err
			}
		}
		if m.Object != nil {
			out[i].Object.Target, err = restoreHPATarget(m.Object.Target)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func restoreHPATarget(input *HPAMetricTarget) (autoscalingv2.MetricTarget, error) {
	out, err := expandHPATarget(input)
	if err != nil || input == nil {
		return out, err
	}
	switch input.Type {
	case "Utilization":
		if input.AverageUtilization != nil {
			out.AverageUtilization = new(int32(*input.AverageUtilization))
		}
	case "Value":
		if input.Value != nil {
			q, err := resource.ParseQuantity(*input.Value)
			if err != nil {
				return out, err
			}
			out.Value = &q
		}
	case "AverageValue":
		if input.AverageValue != nil {
			q, err := resource.ParseQuantity(*input.AverageValue)
			if err != nil {
				return out, err
			}
			out.AverageValue = &q
		}
	}
	return out, nil
}
