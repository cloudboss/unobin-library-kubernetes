package dynamic

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func Library() *runtime.Library {
	env := runtime.MakeResource[
		svc.EnvResource, *svc.EnvResourceOutput, *config.Configuration,
	]((&svc.EnvResource{}).ResourceDefinition())
	object := runtime.MakeResource[
		svc.ObjectResource, *svc.ObjectResourceOutput, *config.Configuration,
	]((&svc.ObjectResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-dynamic",
		Description: "Kubernetes objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Actions: map[string]runtime.ActionRegistration{
			"wait": runtime.MakeAction[
				svc.WaitAction, *svc.WaitActionOutput, *config.Configuration,
			](),
		},
		DataSources: map[string]runtime.DataSourceRegistration{
			"api-resources": runtime.MakeDataSource[
				svc.APIResourcesDataSource, *svc.APIResourcesDataSourceOutput, *config.Configuration,
			](),
			"objects": runtime.MakeDataSource[
				svc.ObjectsDataSource, *svc.ObjectsDataSourceOutput, *config.Configuration,
			](),
			"object": runtime.MakeDataSource[
				svc.ObjectDataSource, *svc.ObjectDataSourceOutput, *config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"annotations": runtime.MakeResource[
				svc.AnnotationsResource, *svc.AnnotationsResourceOutput, *config.Configuration,
			]((&svc.AnnotationsResource{}).ResourceDefinition()),
			"env": svc.EnvRegistration[svc.EnvResource, *svc.EnvResourceOutput](
				env, env.Read, env.KnownReplacementReasons),
			"labels": runtime.MakeResource[
				svc.LabelsResource, *svc.LabelsResourceOutput, *config.Configuration,
			]((&svc.LabelsResource{}).ResourceDefinition()),
			"object": svc.ObjectRegistration[svc.ObjectResource, *svc.ObjectResourceOutput](
				object, object.Read, object.KnownReplacementReasons),
		},
	}
}
