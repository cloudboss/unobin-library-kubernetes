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
			SuggestedUnobinVersion: "v0.13.0-a.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"network-policy": runtime.MakeResource[
				svc.NetworkPolicyResource, *svc.NetworkPolicyResourceOutput, *config.Configuration,
			]((&svc.NetworkPolicyResource{}).ResourceDefinition()),
			"ingress": runtime.MakeResource[
				svc.IngressResource, *svc.IngressResourceOutput, *config.Configuration,
			]((&svc.IngressResource{}).ResourceDefinition()),
		},
	}
}
