package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	crd "github.com/cloudboss/unobin-library-kubernetes/internal/api/apiextensions/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-apiextensions-v1",
		Description: "Kubernetes apiextensions/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"custom-resource-definition": runtime.MakeDataSource[
				crd.CustomResourceDefinitionDataSource, *crd.CustomResourceDefinitionDataSourceOutput,
				*config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"custom-resource-definition": runtime.MakeResource[
				crd.CustomResourceDefinitionResource, *crd.CustomResourceDefinitionResourceOutput,
				*config.Configuration,
			]((&crd.CustomResourceDefinitionResource{}).ResourceDefinition()),
		},
	}
}
