package v1

import (
	"context"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *CronJobResource) ResourceDefinition() runtime.ResourceDefinition[
	CronJobResource, *CronJobResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[CronJobResource, *CronJobResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input CronJobResource, _ *config.Configuration) error {
			_, err := input.prepare(nil)
			return err
		},
		StableID: func(_ CronJobResource, output *CronJobResourceOutput) (string, error) {
			if output == nil || output.UID == "" {
				return "", fmt.Errorf("missing CronJob UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[CronJobResource]{
			runtime.EqualBy(runtime.InputField(func(r *CronJobResource) *CronJobMetadata {
				return &r.Metadata
			}), cronJobMapsEqual),
			runtime.EqualBy(runtime.InputField(func(r *CronJobResource) *CronJobSpec {
				return &r.Spec
			}), cronJobMutableSpecEqual),
		},
		Replace: runtime.Replacement[CronJobResource, *CronJobResourceOutput, *config.Configuration]{
			Fields: []runtime.AnyInputField[CronJobResource]{
				runtime.InputField(func(r *CronJobResource) *CronJobMetadata { return &r.Metadata }),
				runtime.InputField(func(r *CronJobResource) *CronJobSpec { return &r.Spec }),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.CurrentContext }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Namespace }),
			},
		},
	}
}

func cronJobMapsEqual(a, b CronJobMetadata) bool {
	return reflect.DeepEqual(metadataMap(a.Labels), metadataMap(b.Labels)) &&
		reflect.DeepEqual(metadataMap(a.Annotations), metadataMap(b.Annotations))
}

func cronJobIdentityEqual(a, b CronJobMetadata, outer bool) bool {
	if b.Name == nil {
		b.Name = a.Name
	}
	namespace := func(v *string) string {
		if v == nil && outer {
			return "default"
		}
		if v == nil {
			return ""
		}
		return *v
	}
	text := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	return namespace(a.Namespace) == namespace(b.Namespace) &&
		text(a.Name) == text(b.Name) && text(a.GenerateName) == text(b.GenerateName)
}

func cronJobComparedJob(spec JobSpec, observed *JobSpec) (map[string]any, error) {
	value, err := comparedSpec(spec, observed)
	if err != nil {
		return nil, err
	}
	out := value.(map[string]any)
	if selector, ok := out["selector"].(map[string]any); ok {
		removeGeneratedAny(selector["matchLabels"])
		if labels, ok := selector["matchLabels"].(map[string]any); ok && len(labels) == 0 {
			delete(selector, "matchLabels")
		}
	}
	return out, nil
}

func cronJobMutableSpecEqual(a, b CronJobSpec) bool {
	left, err := cronJobComparedJob(a.JobTemplate.Spec, nil)
	if err != nil {
		return false
	}
	right, err := cronJobComparedJob(b.JobTemplate.Spec, &a.JobTemplate.Spec)
	if err != nil {
		return false
	}
	for _, key := range immutableSpecKeys {
		delete(left, key)
		delete(right, key)
	}
	normalize := func(s CronJobSpec) any {
		return []any{s.Schedule, integer(s.FailedJobsHistoryLimit, 1),
			integer(s.SuccessfulJobsHistoryLimit, 3), max(integer(s.StartingDeadlineSeconds, 0), 0),
			boolean(s.Suspend), textDefault(s.ConcurrencyPolicy, "Allow"), textDefault(s.Timezone, "")}
	}
	return reflect.DeepEqual(normalize(a), normalize(b)) && reflect.DeepEqual(left, right) &&
		cronJobMapsEqual(a.JobTemplate.Metadata, b.JobTemplate.Metadata)
}

func textDefault(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func (r *CronJobResource) immutableChanges(p cronJobPrior) (bool, bool) {
	baseline := p.Observed
	if baseline == nil {
		baseline = p.Outputs
	}
	metadata, template := p.Inputs.Metadata, p.Inputs.Spec.JobTemplate.Metadata
	spec := p.Inputs.Spec.JobTemplate.Spec
	var observed *JobSpec
	if baseline != nil {
		metadata.Name = baseline.Metadata.Name
		if metadata.Name == nil {
			metadata.Name = &baseline.Name
		}
		template.Name = baseline.Spec.JobTemplate.Metadata.Name
		spec = computedSnapshot(reflect.ValueOf(spec),
			reflect.ValueOf(baseline.Spec.JobTemplate.Spec)).Interface().(JobSpec)
		observed = &baseline.Spec.JobTemplate.Spec
	}
	changed := !cronJobIdentityEqual(template, r.Spec.JobTemplate.Metadata, false)
	left, leftErr := cronJobComparedJob(spec, nil)
	right, rightErr := cronJobComparedJob(r.Spec.JobTemplate.Spec, observed)
	if leftErr != nil || rightErr != nil {
		changed = true
	}
	for _, key := range immutableSpecKeys {
		changed = changed || !reflect.DeepEqual(left[key], right[key])
	}
	changed = changed || !reflect.DeepEqual(ineffectiveValues(reflect.ValueOf(spec.Template)),
		ineffectiveValues(reflect.ValueOf(r.Spec.JobTemplate.Spec.Template)))
	return !cronJobIdentityEqual(metadata, r.Metadata, true), changed
}
