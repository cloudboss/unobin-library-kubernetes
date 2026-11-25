package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/admissionregistration/v1"
)

func Library() *runtime.Library {
	policy := runtime.MakeResource[
		svc.ValidatingAdmissionPolicyResource, *svc.ValidatingAdmissionPolicyResourceOutput,
		*config.Configuration,
	]((&svc.ValidatingAdmissionPolicyResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-admissionregistration-v1",
		Description: "Kubernetes admissionregistration/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"mutating-webhook-configuration": runtime.MakeDataSource[
				svc.MutatingWebhookConfigurationDataSource,
				*svc.MutatingWebhookConfigurationDataSourceOutput,
				*config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"validating-admission-policy": svc.ValidatingAdmissionPolicyRegistration[
				svc.ValidatingAdmissionPolicyResource, *svc.ValidatingAdmissionPolicyResourceOutput,
			](policy, policy.Read, policy.KnownReplacementReasons),
			"validating-webhook-configuration": runtime.MakeResource[
				svc.ValidatingWebhookConfigurationResource,
				*svc.ValidatingWebhookConfigurationResourceOutput,
				*config.Configuration,
			]((&svc.ValidatingWebhookConfigurationResource{}).ResourceDefinition()),
			"mutating-webhook-configuration": runtime.MakeResource[
				svc.MutatingWebhookConfigurationResource,
				*svc.MutatingWebhookConfigurationResourceOutput,
				*config.Configuration,
			]((&svc.MutatingWebhookConfigurationResource{}).ResourceDefinition()),
		},
	}
}
