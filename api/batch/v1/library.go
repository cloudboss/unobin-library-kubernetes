package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/batch/v1"
)

func Library() *runtime.Library {
	base := runtime.MakeResource[
		svc.JobResource, *svc.JobResourceOutput, *config.Configuration,
	]((&svc.JobResource{}).ResourceDefinition())
	cron := runtime.MakeResource[
		svc.CronJobResource, *svc.CronJobResourceOutput, *config.Configuration,
	]((&svc.CronJobResource{}).ResourceDefinition())
	return &runtime.Library{
		Name:        "kubernetes-batch-v1",
		Description: "Kubernetes batch/v1 objects for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		Actions: map[string]runtime.ActionRegistration{
			"run-cron-job": runtime.MakeAction[
				svc.RunCronJobAction, *svc.RunCronJobActionOutput, *config.Configuration,
			](),
		},
		Resources: map[string]runtime.ResourceRegistration{
			"cron-job": svc.MakeCronJobRegistration[
				svc.CronJobResource, *svc.CronJobResourceOutput,
			](cron, cron.Read, cron.KnownReplacementReasons),
			"job": svc.MakeJobRegistration[svc.JobResource, *svc.JobResourceOutput](
				base, base.Read, base.KnownReplacementReasons),
		},
	}
}
