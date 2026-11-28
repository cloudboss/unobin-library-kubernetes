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
		DataSources: map[string]runtime.DataSourceRegistration{
			"daemon-set": runtime.MakeDataSource[
				svc.DaemonSetDataSource, *svc.DaemonSetDataSourceOutput, *config.Configuration,
			](),
			"stateful-set": runtime.MakeDataSource[
				svc.StatefulSetDataSource, *svc.StatefulSetDataSourceOutput, *config.Configuration,
			](),
			"deployment": runtime.MakeDataSource[
				svc.DeploymentDataSource, *svc.DeploymentDataSourceOutput, *config.Configuration,
			](),
		},
		Actions: map[string]runtime.ActionRegistration{
			"scale": runtime.MakeAction[
				svc.ScaleAction, *svc.ScaleActionOutput, *config.Configuration,
			](),
			"rollout-status": runtime.MakeAction[
				svc.RolloutStatusAction, *svc.RolloutStatusActionOutput, *config.Configuration,
			](),
			"rollout-restart": runtime.MakeAction[
				svc.RolloutRestartAction, *svc.RolloutRestartActionOutput, *config.Configuration,
			](),
		},
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
