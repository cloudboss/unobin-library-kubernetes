package v1

import (
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/authorization/v1"
)

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-authorization-v1",
		Description: "Kubernetes authorization.k8s.io/v1 queries for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.1",
			SuggestedUnobinVersion: "v0.13.1",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"self-subject-access-review": runtime.MakeDataSource[
				svc.SelfSubjectAccessReviewDataSource, *svc.SelfSubjectAccessReviewDataSourceOutput,
				*config.Configuration,
			](),
		},
	}
}
