package object

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-object",
		Description: "Kubernetes objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.0",
			SuggestedUnobinVersion: "v0.12.0",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"object": runtime.MakeResource[
				svc.ObjectResource, *svc.ObjectResourceOutput, *config.Configuration,
			]((&svc.ObjectResource{}).ResourceDefinition()),
		},
	}
}
