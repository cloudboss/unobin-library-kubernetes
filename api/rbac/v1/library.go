package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
)

func Library() *runtime.Library {
	role := runtime.MakeResource[
		svc.RoleResource, *svc.RoleResourceOutput, *config.Configuration,
	]((&svc.RoleResource{}).ResourceDefinition())
	roleBinding := runtime.MakeResource[
		svc.RoleBindingResource, *svc.RoleBindingResourceOutput, *config.Configuration,
	]((&svc.RoleBindingResource{}).ResourceDefinition())
	clusterRole := runtime.MakeResource[
		svc.ClusterRoleResource, *svc.ClusterRoleResourceOutput, *config.Configuration,
	]((&svc.ClusterRoleResource{}).ResourceDefinition())
	clusterRoleBinding := runtime.MakeResource[
		svc.ClusterRoleBindingResource, *svc.ClusterRoleBindingResourceOutput, *config.Configuration,
	]((&svc.ClusterRoleBindingResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-rbac-v1",
		Description: "Kubernetes rbac/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.0-a.2",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"cluster-role-binding": svc.ClusterRoleBindingRegistration[
				svc.ClusterRoleBindingResource, *svc.ClusterRoleBindingResourceOutput,
			](clusterRoleBinding, clusterRoleBinding.Read, clusterRoleBinding.KnownReplacementReasons),
			"cluster-role": svc.ClusterRoleRegistration[
				svc.ClusterRoleResource, *svc.ClusterRoleResourceOutput,
			](clusterRole, clusterRole.Read, clusterRole.KnownReplacementReasons),
			"role-binding": svc.RoleBindingRegistration[
				svc.RoleBindingResource, *svc.RoleBindingResourceOutput,
			](roleBinding, roleBinding.Read, roleBinding.KnownReplacementReasons),
			"role": svc.RoleRegistration[
				svc.RoleResource, *svc.RoleResourceOutput,
			](role, role.Read, role.KnownReplacementReasons),
		},
	}
}
