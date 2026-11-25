package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
)

func Library() *runtime.Library {
	statefulSet := runtime.MakeResource[
		svc.StatefulSetResource, *svc.StatefulSetResourceOutput, *config.Configuration,
	]((&svc.StatefulSetResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-apps-v1",
		Description: "Kubernetes apps/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"stateful-set": svc.StatefulSetRegistration[
				svc.StatefulSetResource, *svc.StatefulSetResourceOutput,
			](statefulSet, statefulSet.Read, statefulSet.KnownReplacementReasons),
			"daemon-set": runtime.MakeResource[
				svc.DaemonSetResource, *svc.DaemonSetResourceOutput, *config.Configuration,
			]((&svc.DaemonSetResource{}).ResourceDefinition()),
			"deployment": runtime.MakeResource[
				svc.DeploymentResource, *svc.DeploymentResourceOutput, *config.Configuration,
			]((&svc.DeploymentResource{}).ResourceDefinition()),
		},
	}
}
