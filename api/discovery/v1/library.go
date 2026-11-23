package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	discovery "github.com/cloudboss/unobin-library-kubernetes/internal/api/discovery/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-discovery-v1",
		Description: "Kubernetes discovery/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.0-a.10",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"endpoint-slice": runtime.MakeResource[
				discovery.EndpointSliceResource,
				*discovery.EndpointSliceResourceOutput,
				*config.Configuration,
			]((&discovery.EndpointSliceResource{}).ResourceDefinition()),
		},
	}
}
