package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	coordinationv1 "github.com/cloudboss/unobin-library-kubernetes/internal/api/coordination/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-coordination-v1",
		Description: "Kubernetes coordination.k8s.io/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI: "1.1", SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"lease": runtime.MakeResource[
				coordinationv1.LeaseResource, *coordinationv1.LeaseResourceOutput, *config.Configuration,
			]((&coordinationv1.LeaseResource{}).ResourceDefinition()),
		},
	}
}
