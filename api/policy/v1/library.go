package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	policy "github.com/cloudboss/unobin-library-kubernetes/internal/api/policy/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-policy-v1",
		Description: "Kubernetes policy/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"pod-disruption-budget": runtime.MakeResource[
				policy.PodDisruptionBudgetResource, *policy.PodDisruptionBudgetResourceOutput,
				*config.Configuration,
			]((&policy.PodDisruptionBudgetResource{}).ResourceDefinition()),
		},
	}
}
