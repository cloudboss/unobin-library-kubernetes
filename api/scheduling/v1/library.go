package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	schedulingv1 "github.com/cloudboss/unobin-library-kubernetes/internal/api/scheduling/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-scheduling-v1",
		Description: "Kubernetes scheduling/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"priority-class": runtime.MakeResource[
				schedulingv1.PriorityClassResource, *schedulingv1.PriorityClassResourceOutput,
				*config.Configuration,
			]((&schedulingv1.PriorityClassResource{}).ResourceDefinition()),
		},
	}
}
