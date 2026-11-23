package dynamic

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-dynamic",
		Description: "Kubernetes objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.0-a.11",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"objects": runtime.MakeDataSource[
				svc.ObjectsDataSource, *svc.ObjectsDataSourceOutput, *config.Configuration,
			](),
			"object-info": runtime.MakeDataSource[
				svc.ObjectDataSource, *svc.ObjectDataSourceOutput, *config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"object": runtime.MakeResource[
				svc.ObjectResource, *svc.ObjectResourceOutput, *config.Configuration,
			]((&svc.ObjectResource{}).ResourceDefinition()),
		},
	}
}
