package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	storagev1 "github.com/cloudboss/unobin-library-kubernetes/internal/api/storage/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-storage-v1",
		Description: "Kubernetes storage/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.0",
			SuggestedUnobinVersion: "v0.13.0-a.7",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"storage-class": runtime.MakeResource[
				storagev1.StorageClassResource, *storagev1.StorageClassResourceOutput,
				*config.Configuration,
			]((&storagev1.StorageClassResource{}).ResourceDefinition()),
		},
	}
}
