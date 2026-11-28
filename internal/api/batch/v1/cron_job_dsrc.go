package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// CronJobDataSource observes one named CronJob without waiting for schedules.
type CronJobDataSource struct{ Metadata CronJobLookupMetadata }
type CronJobLookupMetadata struct {
	Name      string
	Namespace *string
}
type CronJobDataSourceOutput struct {
	Metadata CronJobLookupMetadataOutput
	Spec     *CronJobLookupSpec
	Status   *CronJobLookupStatus
}
type CronJobLookupMetadataOutput struct {
	Name            string
	Namespace       string
	GenerateName    string
	UID             string `json:"uid"`
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}
type CronJobLookupSpec struct {
	Schedule                   string
	Timezone                   *string `json:"timeZone"`
	ConcurrencyPolicy          string
	StartingDeadlineSeconds    *int64
	Suspend                    *bool
	SuccessfulJobsHistoryLimit *int64
	FailedJobsHistoryLimit     *int64
	JobTemplate                CronJobLookupTemplate
}
type CronJobLookupTemplate struct {
	Metadata CronJobLookupTemplateMetadata
	Spec     JobLookupSpec
}
type CronJobLookupTemplateMetadata struct {
	Name         string
	GenerateName string
	Namespace    string
	Labels       map[string]string
	Annotations  map[string]string
}
type CronJobLookupStatus struct {
	Active             []CronJobLookupReference
	LastScheduleTime   *string
	LastSuccessfulTime *string
}
type CronJobLookupReference struct {
	APIVersion      string `json:"apiVersion"`
	Kind            string
	Namespace       string
	Name            string
	UID             string `json:"uid"`
	ResourceVersion string
	FieldPath       string
}

var _ runtime.TypedDataSource[
	*CronJobDataSourceOutput, *config.Configuration,
] = (*CronJobDataSource)(nil)

func (r CronJobDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.Present(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.AtMost(constraint.Length(r.Metadata.Name), 253),
		),
		constraint.When(constraint.Present(r.Metadata.Namespace)).Require(
			constraint.IsMatch(r.Metadata.Namespace, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
			constraint.AtMost(constraint.Length(r.Metadata.Namespace), 63),
		),
	}
}

func (r CronJobDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *CronJobDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*CronJobDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace, name := textDefault(r.Metadata.Namespace, "default"), r.Metadata.Name
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("get CronJob %s/%s: %w", namespace, name, err)
	}
	obj, err := api.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &CronJobDataSourceOutput{Metadata: CronJobLookupMetadataOutput{
			Name: name, Namespace: namespace, Labels: map[string]string{}, Annotations: map[string]string{},
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get CronJob %s/%s: %w", namespace, name, err)
	}
	out, err := cronJobLookupOutput(obj)
	if err != nil {
		return nil, fmt.Errorf("read CronJob %s/%s: %w", namespace, name, err)
	}
	return out, nil
}
