package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func Library() *runtime.Library {
	pvc := runtime.MakeResource[
		svc.PersistentVolumeClaimResource, *svc.PersistentVolumeClaimResourceOutput,
		*config.Configuration,
	]((&svc.PersistentVolumeClaimResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-core-v1",
		Description: "Kubernetes core/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.0-a.4",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"service-info": runtime.MakeDataSource[
				svc.ServiceDataSource, *svc.ServiceDataSourceOutput, *config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"persistent-volume-claim": svc.PersistentVolumeClaimRegistration[
				svc.PersistentVolumeClaimResource, *svc.PersistentVolumeClaimResourceOutput,
			](pvc, pvc.Read, pvc.KnownReplacementReasons),
			"service-account": runtime.MakeResource[
				svc.ServiceAccountResource, *svc.ServiceAccountResourceOutput, *config.Configuration,
			]((&svc.ServiceAccountResource{}).ResourceDefinition()),
			"secret": runtime.MakeResource[
				svc.SecretResource, *svc.SecretResourceOutput, *config.Configuration,
			]((&svc.SecretResource{}).ResourceDefinition()),
			"service": runtime.MakeResource[
				svc.ServiceResource, *svc.ServiceResourceOutput, *config.Configuration,
			]((&svc.ServiceResource{}).ResourceDefinition()),
			"namespace": runtime.MakeResource[
				svc.NamespaceResource, *svc.NamespaceResourceOutput, *config.Configuration,
			]((&svc.NamespaceResource{}).ResourceDefinition()),
			"config-map": runtime.MakeResource[
				svc.ConfigMapResource, *svc.ConfigMapResourceOutput, *config.Configuration,
			]((&svc.ConfigMapResource{}).ResourceDefinition()),
		},
	}
}
