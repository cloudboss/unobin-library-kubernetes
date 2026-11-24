package v2

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/autoscaling/v2"
)

func Library() *runtime.Library {
	base := runtime.MakeResource[
		svc.HorizontalPodAutoscalerResource, *svc.HorizontalPodAutoscalerResourceOutput,
		*config.Configuration,
	]((&svc.HorizontalPodAutoscalerResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-autoscaling-v2",
		Description: "Kubernetes autoscaling/v2 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.0",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"horizontal-pod-autoscaler": svc.HorizontalPodAutoscalerRegistration[
				svc.HorizontalPodAutoscalerResource, *svc.HorizontalPodAutoscalerResourceOutput,
			](base, base.Read, base.KnownReplacementReasons),
		},
	}
}
