package v1

import (
	autoscalingv1 "k8s.io/api/autoscaling/v1"
)

func hpaValue[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func hpaMin(p *int) int {
	if p == nil {
		return 1
	}
	return *p
}

func hpaNamespace(p *string) string {
	if p == nil {
		return "default"
	}
	return *p
}

func expandHPASpec(s HPASpec) autoscalingv1.HorizontalPodAutoscalerSpec {
	spec := autoscalingv1.HorizontalPodAutoscalerSpec{
		MaxReplicas: int32(s.MaxReplicas), ScaleTargetRef: expandHPARef(s.ScaleTargetRef),
	}
	if min := hpaMin(s.MinReplicas); min > 0 {
		spec.MinReplicas = new(int32(min))
	}
	if cpu := hpaValue(s.TargetCPUUtilizationPercentage); cpu > 0 {
		spec.TargetCPUUtilizationPercentage = new(int32(cpu))
	}
	return spec
}

func expandHPARef(r HPATargetRef) autoscalingv1.CrossVersionObjectReference {
	return autoscalingv1.CrossVersionObjectReference{
		APIVersion: hpaValue(r.APIVersion), Kind: r.Kind, Name: r.Name,
	}
}

func projectHPA(
	server string, live *autoscalingv1.HorizontalPodAutoscaler, r *HorizontalPodAutoscalerResource,
) *HorizontalPodAutoscalerResourceOutput {
	m, s := live.ObjectMeta, live.Spec
	desired := r.Object.Metadata
	out := &HorizontalPodAutoscalerResourceOutput{
		Server: server, APIVersion: hpaAPIVersion, Kind: hpaKind,
		Object: HPAObjectOutput{
			Metadata: HPAMetadataOutput{
				Name: m.Name, GenerateName: m.GenerateName, Namespace: m.Namespace, UID: string(m.UID),
				Annotations: projectHPAMetadata(m.Annotations, r.IgnoreAnnotations,
					hpaValue(desired.Annotations)),
				Labels: projectHPAMetadata(m.Labels, r.IgnoreLabels, hpaValue(desired.Labels)),
			},
			Spec: HPASpec{
				MaxReplicas:    int(s.MaxReplicas),
				ScaleTargetRef: HPATargetRef{Kind: s.ScaleTargetRef.Kind, Name: s.ScaleTargetRef.Name},
			},
		},
	}
	if s.MinReplicas != nil {
		out.Object.Spec.MinReplicas = new(int(*s.MinReplicas))
	}
	if s.TargetCPUUtilizationPercentage != nil {
		out.Object.Spec.TargetCPUUtilizationPercentage = new(int(*s.TargetCPUUtilizationPercentage))
	}
	if s.ScaleTargetRef.APIVersion != "" {
		out.Object.Spec.ScaleTargetRef.APIVersion = new(s.ScaleTargetRef.APIVersion)
	}
	return out
}
