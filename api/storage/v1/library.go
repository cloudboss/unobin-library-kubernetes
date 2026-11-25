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
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"csi-driver": runtime.MakeResource[
				storagev1.CSIDriverResource, *storagev1.CSIDriverResourceOutput, *config.Configuration,
			]((&storagev1.CSIDriverResource{}).ResourceDefinition()),
			"storage-class": runtime.MakeResource[
				storagev1.StorageClassResource, *storagev1.StorageClassResourceOutput,
				*config.Configuration,
			]((&storagev1.StorageClassResource{}).ResourceDefinition()),
		},
		DataSources: map[string]runtime.DataSourceRegistration{
			"storage-class": runtime.MakeDataSource[
				storagev1.StorageClassDataSource, *storagev1.StorageClassDataSourceOutput,
				*config.Configuration,
			](),
		},
	}
}
