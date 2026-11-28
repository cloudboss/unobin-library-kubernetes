package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/networking/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-networking-v1",
		Description: "Kubernetes networking/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"network-policy": runtime.MakeDataSource[
				svc.NetworkPolicyDataSource, *svc.NetworkPolicyDataSourceOutput, *config.Configuration,
			](),
			"ingress": runtime.MakeDataSource[
				svc.IngressDataSource, *svc.IngressDataSourceOutput, *config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"network-policy": runtime.MakeResource[
				svc.NetworkPolicyResource, *svc.NetworkPolicyResourceOutput, *config.Configuration,
			]((&svc.NetworkPolicyResource{}).ResourceDefinition()),
			"ingress": runtime.MakeResource[
				svc.IngressResource, *svc.IngressResourceOutput, *config.Configuration,
			]((&svc.IngressResource{}).ResourceDefinition()),
			"ingress-class": runtime.MakeResource[
				svc.IngressClassResource, *svc.IngressClassResourceOutput, *config.Configuration,
			]((&svc.IngressClassResource{}).ResourceDefinition()),
		},
	}
}
