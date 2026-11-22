package v1

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/robfig/cron"
	batchv1 "k8s.io/api/batch/v1"
)

func (r *CronJobResource) prepare(observed *CronJobSpec) (*batchv1.CronJob, error) {
	if _, err := cron.ParseStandard(r.Spec.Schedule); err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	var old reflect.Value
	if observed != nil {
		old = reflect.ValueOf(*observed)
	}
	value, err := expandValue(reflect.ValueOf(r.Spec), old, "spec")
	if err != nil {
		return nil, err
	}
	spec := value.(map[string]any)
	for key, fallback := range map[string]int64{
		"failedJobsHistoryLimit": 1, "successfulJobsHistoryLimit": 3,
	} {
		// Omission depends on the original integer, before the unchecked int32 conversion.
		original := r.Spec.FailedJobsHistoryLimit
		if key == "successfulJobsHistoryLimit" {
			original = r.Spec.SuccessfulJobsHistoryLimit
		}
		spec[key] = int64(int32(integer(original, fallback)))
		if integer(original, fallback) == fallback {
			delete(spec, key)
		}
	}
	if integer(r.Spec.StartingDeadlineSeconds, 0) <= 0 {
		delete(spec, "startingDeadlineSeconds")
	}
	metadata, err := expandValue(reflect.ValueOf(r.Metadata), reflect.Value{}, "metadata")
	if err != nil {
		return nil, err
	}
	m := metadata.(map[string]any)
	if r.Metadata.Namespace == nil {
		m["namespace"] = "default"
	}
	data, err := json.Marshal(map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob", "metadata": m, "spec": spec,
	})
	if err != nil {
		return nil, err
	}
	obj := &batchv1.CronJob{}
	if err := json.Unmarshal(data, obj); err != nil {
		return nil, fmt.Errorf("convert CronJob: %w", err)
	}
	return obj, nil
}

func (r *CronJobResource) output(
	obj *batchv1.CronJob, server string, tracked CronJobMetadata,
) (*CronJobResourceOutput, error) {
	obj = obj.DeepCopy()
	removeGenerated(obj.Labels)
	if obj.Spec.JobTemplate.Spec.Selector != nil {
		removeGenerated(obj.Spec.JobTemplate.Spec.Selector.MatchLabels)
	}
	removeGenerated(obj.Spec.JobTemplate.Spec.Template.Labels)
	filterMetadata(obj.Labels, tracked.Labels, r.IgnoreLabels)
	filterMetadata(obj.Annotations, tracked.Annotations, r.IgnoreAnnotations)
	out := &CronJobResourceOutput{
		Server: server, APIVersion: "batch/v1", Kind: "CronJob",
		Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
	}
	data, err := nativeMap(obj.ObjectMeta)
	if err != nil {
		return nil, err
	}
	if err := readValue(reflect.ValueOf(&out.Metadata).Elem(), data); err != nil {
		return nil, err
	}
	data, err = nativeMap(obj.Spec)
	if err != nil {
		return nil, err
	}
	if err := readValue(reflect.ValueOf(&out.Spec).Elem(), data); err != nil {
		return nil, err
	}
	out.Spec.JobTemplate.Spec, err = normalizedSpec(obj.Spec.JobTemplate.Spec)
	return out, err
}
