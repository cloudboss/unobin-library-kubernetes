package v2

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-autoscaling-v2",
		Description: "Kubernetes autoscaling/v2 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.0",
			SuggestedUnobinVersion: "v0.12.0",
		},
		Configuration: config.LibraryConfiguration(),
		Resources:     map[string]runtime.ResourceRegistration{},
	}
}
