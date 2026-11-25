package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func Library() *runtime.Library {
	pv := runtime.MakeResource[
		svc.PersistentVolumeResource, *svc.PersistentVolumeResourceOutput, *config.Configuration,
	]((&svc.PersistentVolumeResource{}).ResourceDefinition())
	pvc := runtime.MakeResource[
		svc.PersistentVolumeClaimResource, *svc.PersistentVolumeClaimResourceOutput,
		*config.Configuration,
	]((&svc.PersistentVolumeClaimResource{}).ResourceDefinition())
	pod := runtime.MakeResource[
		svc.PodResource, *svc.PodResourceOutput, *config.Configuration,
	]((&svc.PodResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-core-v1",
		Description: "Kubernetes core/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"config-map": runtime.MakeDataSource[
				svc.ConfigMapDataSource, *svc.ConfigMapDataSourceOutput, *config.Configuration,
			](),
			"endpoints": runtime.MakeDataSource[
				svc.EndpointsDataSource, *svc.EndpointsDataSourceOutput, *config.Configuration,
			](),
			"namespace": runtime.MakeDataSource[
				svc.NamespaceDataSource, *svc.NamespaceDataSourceOutput, *config.Configuration,
			](),
			"namespaces": runtime.MakeDataSource[
				svc.NamespacesDataSource, *svc.NamespacesDataSourceOutput, *config.Configuration,
			](),
			"nodes": runtime.MakeDataSource[
				svc.NodesDataSource, *svc.NodesDataSourceOutput, *config.Configuration,
			](),
			"persistent-volume": runtime.MakeDataSource[
				svc.PersistentVolumeDataSource, *svc.PersistentVolumeDataSourceOutput,
				*config.Configuration,
			](),
			"persistent-volume-claim": runtime.MakeDataSource[
				svc.PersistentVolumeClaimDataSource, *svc.PersistentVolumeClaimDataSourceOutput,
				*config.Configuration,
			](),
			"pod": runtime.MakeDataSource[
				svc.PodDataSource, *svc.PodDataSourceOutput, *config.Configuration,
			](),
			"secret": runtime.MakeDataSource[
				svc.SecretDataSource, *svc.SecretDataSourceOutput, *config.Configuration,
			](),
			"server-version": runtime.MakeDataSource[
				svc.ServerVersionDataSource, *svc.ServerVersionDataSourceOutput,
				*config.Configuration,
			](),
			"service": runtime.MakeDataSource[
				svc.ServiceDataSource, *svc.ServiceDataSourceOutput, *config.Configuration,
			](),
			"service-account": runtime.MakeDataSource[
				svc.ServiceAccountDataSource, *svc.ServiceAccountDataSourceOutput,
				*config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"config-map-data": runtime.MakeResource[
				svc.ConfigMapDataResource, *svc.ConfigMapDataResourceOutput, *config.Configuration,
			]((&svc.ConfigMapDataResource{}).ResourceDefinition()),
			"limit-range": runtime.MakeResource[
				svc.LimitRangeResource, *svc.LimitRangeResourceOutput, *config.Configuration,
			]((&svc.LimitRangeResource{}).ResourceDefinition()),
			"node-taint": runtime.MakeResource[
				svc.NodeTaintResource, *svc.NodeTaintResourceOutput, *config.Configuration,
			]((&svc.NodeTaintResource{}).ResourceDefinition()),
			"resource-quota": runtime.MakeResource[
				svc.ResourceQuotaResource, *svc.ResourceQuotaResourceOutput, *config.Configuration,
			]((&svc.ResourceQuotaResource{}).ResourceDefinition()),
			"persistent-volume": svc.PersistentVolumeRegistration[
				svc.PersistentVolumeResource, *svc.PersistentVolumeResourceOutput,
			](pv, pv.Read, pv.KnownReplacementReasons),
			"persistent-volume-claim": svc.PersistentVolumeClaimRegistration[
				svc.PersistentVolumeClaimResource, *svc.PersistentVolumeClaimResourceOutput,
			](pvc, pvc.Read, pvc.KnownReplacementReasons),
			"default-service-account": runtime.MakeResource[
				svc.DefaultServiceAccountResource, *svc.DefaultServiceAccountResourceOutput,
				*config.Configuration,
			]((&svc.DefaultServiceAccountResource{}).ResourceDefinition()),
			"service-account": runtime.MakeResource[
				svc.ServiceAccountResource, *svc.ServiceAccountResourceOutput, *config.Configuration,
			]((&svc.ServiceAccountResource{}).ResourceDefinition()),
			"secret-data": runtime.MakeResource[
				svc.SecretDataResource, *svc.SecretDataResourceOutput, *config.Configuration,
			]((&svc.SecretDataResource{}).ResourceDefinition()),
			"secret": runtime.MakeResource[
				svc.SecretResource, *svc.SecretResourceOutput, *config.Configuration,
			]((&svc.SecretResource{}).ResourceDefinition()),
			"pod": svc.PodRegistration[svc.PodResource, *svc.PodResourceOutput](
				pod, pod.Read, pod.KnownReplacementReasons),
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
