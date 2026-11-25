package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/authentication/v1"
)

func Library() *runtime.Library {
	tokenRequest := runtime.MakeResource[
		svc.TokenRequestResource, *svc.TokenRequestResourceOutput, *config.Configuration,
	]((&svc.TokenRequestResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-authentication-v1",
		Description: "Kubernetes authentication.k8s.io/v1 resources for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"token-request": svc.TokenRequestRegistration[
				svc.TokenRequestResource, *svc.TokenRequestResourceOutput,
			](tokenRequest, tokenRequest.Read, tokenRequest.KnownReplacementReasons),
		},
	}
}
