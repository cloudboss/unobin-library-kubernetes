package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
)

func Library() *runtime.Library {
	roleBinding := runtime.MakeResource[
		svc.RoleBindingResource, *svc.RoleBindingResourceOutput, *config.Configuration,
	]((&svc.RoleBindingResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-rbac-v1",
		Description: "Kubernetes rbac/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.0",
			SuggestedUnobinVersion: "v0.12.0",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"role-binding": svc.RoleBindingRegistration[
				svc.RoleBindingResource, *svc.RoleBindingResourceOutput,
			](roleBinding, roleBinding.Read, roleBinding.KnownReplacementReasons),
			"role": runtime.MakeResource[
				svc.RoleResource, *svc.RoleResourceOutput, *config.Configuration,
			]((&svc.RoleResource{}).ResourceDefinition()),
		},
	}
}
