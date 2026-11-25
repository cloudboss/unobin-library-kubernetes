package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apiregistration/v1"
)

func Library() *runtime.Library {
	service := runtime.MakeResource[
		svc.APIServiceResource, *svc.APIServiceResourceOutput, *config.Configuration,
	]((&svc.APIServiceResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-apiregistration-v1",
		Description: "Kubernetes apiregistration/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"api-service": svc.APIServiceRegistration[
				svc.APIServiceResource, *svc.APIServiceResourceOutput,
			](service, service.Read, service.KnownReplacementReasons),
		},
	}
}
