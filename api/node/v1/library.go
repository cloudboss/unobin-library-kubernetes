package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	nodev1 "github.com/cloudboss/unobin-library-kubernetes/internal/api/node/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-node-v1",
		Description: "Kubernetes node/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.0",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"runtime-class": runtime.MakeResource[
				nodev1.RuntimeClassResource, *nodev1.RuntimeClassResourceOutput, *config.Configuration,
			]((&nodev1.RuntimeClassResource{}).ResourceDefinition()),
		},
	}
}
