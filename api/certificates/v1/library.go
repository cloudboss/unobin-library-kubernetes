package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	certificatesv1 "github.com/cloudboss/unobin-library-kubernetes/internal/api/certificates/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-certificates-v1",
		Description: "Kubernetes certificates/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"certificate-signing-request": runtime.MakeResource[
				certificatesv1.CertificateSigningRequestResource,
				*certificatesv1.CertificateSigningRequestResourceOutput, *config.Configuration,
			]((&certificatesv1.CertificateSigningRequestResource{}).ResourceDefinition()),
		},
	}
}
