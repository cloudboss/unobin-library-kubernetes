package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-apps-v1",
		Description: "Kubernetes apps/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.0",
			SuggestedUnobinVersion: "v0.12.0",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"deployment": runtime.MakeResource[
				svc.DeploymentResource, *svc.DeploymentResourceOutput, *config.Configuration,
			]((&svc.DeploymentResource{}).ResourceDefinition()),
		},
	}
}
