package v1

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func (r *StatefulSetResource) statefulSetSpecMap(
	observed *StatefulSetSpec,
) (map[string]any, error) {
	var previous reflect.Value
	if observed != nil {
		copy := *observed
		if r.Spec.PersistentVolumeClaimRetentionPolicy == nil {
			copy.PersistentVolumeClaimRetentionPolicy = nil
		}
		previous = reflect.ValueOf(copy)
	}
	value, err := expandValue(reflect.ValueOf(r.Spec), previous, "spec")
	if err != nil {
		return nil, err
	}
	spec := value.(map[string]any)
	if observed != nil && r.Spec.PersistentVolumeClaimRetentionPolicy == nil &&
		observed.PersistentVolumeClaimRetentionPolicy != nil {
		policy, err := statefulSetObservedValue(
			reflect.ValueOf(observed.PersistentVolumeClaimRetentionPolicy))
		if err != nil {
			return nil, err
		}
		spec["persistentVolumeClaimRetentionPolicy"] = policy
	}
	if claims, ok := spec["volumeClaimTemplates"].([]any); ok {
		for i, claim := range claims {
			if (*r.Spec.VolumeClaimTemplate)[i].Metadata.Namespace == nil {
				claim.(map[string]any)["metadata"].(map[string]any)["namespace"] = "default"
			}
		}
	}
	return spec, nil
}

func (r *StatefulSetResource) expand(observed *StatefulSetSpec) (*appsv1.StatefulSet, error) {
	spec, err := r.statefulSetSpecMap(observed)
	if err != nil {
		return nil, err
	}
	if r.Spec.Replicas != nil && *r.Spec.Replicas != "" {
		replicas, err := strconv.ParseInt(*r.Spec.Replicas, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("convert StatefulSet replicas: %w", err)
		}
		spec["replicas"] = replicas
	}
	if revision, ok := spec["revisionHistoryLimit"].(int64); ok {
		spec["revisionHistoryLimit"] = int64(int32(revision))
	} else {
		spec["revisionHistoryLimit"] = int64(0)
	}
	if strategy, ok := spec["updateStrategy"].(map[string]any); ok {
		if rolling, ok := strategy["rollingUpdate"].(map[string]any); ok {
			rolling["partition"] = int64(int32(rolling["partition"].(int64)))
		}
	}
	if _, ok := spec["persistentVolumeClaimRetentionPolicy"]; !ok {
		spec["persistentVolumeClaimRetentionPolicy"] = map[string]any{}
	}
	if r.Spec.VolumeClaimTemplate == nil || len(*r.Spec.VolumeClaimTemplate) == 0 {
		spec["minReadySeconds"] = int64(0)
	}
	metadata, err := expandValue(reflect.ValueOf(r.Metadata), reflect.Value{}, "metadata")
	if err != nil {
		return nil, err
	}
	if r.Metadata.Namespace == nil {
		metadata.(map[string]any)["namespace"] = "default"
	}
	encoded, err := json.Marshal(map[string]any{
		"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": metadata, "spec": spec,
	})
	if err != nil {
		return nil, err
	}
	obj := &appsv1.StatefulSet{}
	if err := json.Unmarshal(encoded, obj); err != nil {
		return nil, fmt.Errorf("convert StatefulSet: %w", err)
	}
	return obj, nil
}

func (r *StatefulSetResource) statefulSetOutput(
	obj *appsv1.StatefulSet, server string, tracked *Metadata,
) (*StatefulSetResourceOutput, error) {
	if obj.UID == "" {
		return nil, fmt.Errorf("stateful-set has no Kubernetes UID")
	}
	obj = obj.DeepCopy()
	baseline := r.Metadata
	if tracked != nil {
		baseline = *tracked
	}
	filterStatefulSetMetadata(obj.Labels, baseline.Labels, r.IgnoreLabels)
	filterStatefulSetMetadata(obj.Annotations, baseline.Annotations, r.IgnoreAnnotations)
	projectRolloutAnnotation(obj.Spec.Template.Annotations, r.Spec.Template.Metadata.Annotations)
	for _, containers := range [][]corev1.Container{
		obj.Spec.Template.Spec.Containers, obj.Spec.Template.Spec.InitContainers,
	} {
		for i := range containers {
			for j := range containers[i].VolumeMounts {
				mount := &containers[i].VolumeMounts[j]
				if mount.MountPropagation == nil {
					mount.MountPropagation = new(corev1.MountPropagationNone)
				}
			}
		}
	}
	pod, err := normalizedSpec(appsv1.DeploymentSpec{Template: obj.Spec.Template})
	if err != nil {
		return nil, err
	}
	data, err := nativeMap(obj.Spec)
	if err != nil {
		return nil, err
	}
	if r.Spec.UpdateStrategy == nil {
		delete(data, "updateStrategy")
	}
	out := &StatefulSetResourceOutput{
		Server: server, APIVersion: "apps/v1", Kind: "StatefulSet",
		Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
	}
	if err := readValue(reflect.ValueOf(&out.Spec).Elem(), data); err != nil {
		return nil, err
	}
	normalizedPod := StatefulSetPodSpec(pod.Template.Spec)
	out.Spec.Template.Spec = &normalizedPod
	metadata, err := nativeMap(obj.ObjectMeta)
	if err != nil {
		return nil, err
	}
	if err := readValue(reflect.ValueOf(&out.Metadata).Elem(), metadata); err != nil {
		return nil, err
	}
	return out, nil
}

func filterStatefulSetMetadata(
	values map[string]string, tracked *map[string]string, ignore *[]string,
) {
	for key := range values {
		if tracked != nil {
			if _, ok := (*tracked)[key]; ok {
				continue
			}
		}
		parsed, err := url.Parse("//" + key)
		internal := false
		if err == nil {
			host := parsed.Hostname()
			internal = host != "app.kubernetes.io" && host != "service.beta.kubernetes.io" &&
				(strings.HasSuffix(host, "kubernetes.io") ||
					strings.Contains(key, "deprecated.daemonset.template.generation"))
		}
		if ignore != nil {
			for _, pattern := range *ignore {
				match, err := regexp.MatchString(pattern, key)
				internal = internal || err == nil && match
			}
		}
		if internal {
			delete(values, key)
		}
	}
}

func statefulSetSpecPatch(
	prior, observed, desired StatefulSetSpec, next map[string]any,
) ([]patchOperation, error) {
	oldValue, err := statefulSetObservedValue(reflect.ValueOf(observed))
	if err != nil {
		return nil, err
	}
	old := oldValue.(map[string]any)
	var patch []patchOperation
	for _, field := range []string{"replicas", "template", "minReadySeconds"} {
		value, present := next[field]
		if !present {
			continue
		}
		if field == "template" {
			if statefulSetValuesEqual(value, old[field],
				reflect.TypeFor[StatefulSetPodTemplate]()) {
				continue
			}
		} else if fmt.Sprint(value) == fmt.Sprint(old[field]) {
			continue
		}
		patch = append(patch, patchOperation{Op: "replace", Path: "/spec/" + field, Value: value})
	}
	if prior.UpdateStrategy != nil && desired.UpdateStrategy == nil {
		return nil, fmt.Errorf("update-strategy cannot be removed")
	}
	if desired.UpdateStrategy != nil {
		strategy := next["updateStrategy"].(map[string]any)
		oldStrategy, _ := old["updateStrategy"].(map[string]any)
		if !reflect.DeepEqual(strategy["type"], oldStrategy["type"]) {
			op := "replace"
			if prior.UpdateStrategy == nil {
				op = "add"
			}
			patch = append(patch, patchOperation{
				Op: op, Path: "/spec/updateStrategy/type", Value: strategy["type"],
			})
		}
		rolling, has := strategy["rollingUpdate"].(map[string]any)
		oldRolling, had := oldStrategy["rollingUpdate"].(map[string]any)
		if !has && had {
			patch = append(patch, patchOperation{
				Op: "remove", Path: "/spec/updateStrategy/rollingUpdate",
			})
		}
		if has {
			if !had {
				patch = append(patch, patchOperation{
					Op: "add", Path: "/spec/updateStrategy/rollingUpdate", Value: map[string]any{},
				})
			}
			if !had || fmt.Sprint(rolling["partition"]) != fmt.Sprint(oldRolling["partition"]) {
				op := "replace"
				if !had {
					op = "add"
				}
				patch = append(patch, patchOperation{
					Op: op, Path: "/spec/updateStrategy/rollingUpdate/partition",
					Value: rolling["partition"],
				})
			}
		}
	}
	if desired.PersistentVolumeClaimRetentionPolicy != nil {
		policy := next["persistentVolumeClaimRetentionPolicy"].(map[string]any)
		oldPolicy, _ := old["persistentVolumeClaimRetentionPolicy"].(map[string]any)
		if !reflect.DeepEqual(policy, oldPolicy) {
			for _, field := range []string{"whenDeleted", "whenScaled"} {
				if policy[field] != "" {
					patch = append(patch, patchOperation{
						Op: "replace", Path: "/spec/persistentVolumeClaimRetentionPolicy/" + field,
						Value: policy[field],
					})
				}
			}
		}
	}
	return patch, nil
}

func statefulSetValuesEqual(a, b any, model reflect.Type) bool {
	return reflect.DeepEqual(
		comparisonValue(a, model, "", false), comparisonValue(b, model, "", false))
}

func statefulSetObservedValue(value reflect.Value) (any, error) {
	value = indirect(value)
	if !value.IsValid() {
		return nil, nil
	}
	switch value.Kind() {
	case reflect.Struct:
		out := map[string]any{}
		for i := range value.NumField() {
			field := value.Type().Field(i)
			name := field.Tag.Get("json")
			if name == "-" {
				continue
			}
			item, err := statefulSetObservedValue(value.Field(i))
			if err != nil {
				return nil, err
			}
			if item == nil {
				continue
			}
			if field.Tag.Get("omit") == "true" && reflect.ValueOf(item).IsZero() {
				continue
			}
			item, err = formatValue(field.Tag.Get("format"), item)
			if err != nil {
				return nil, err
			}
			if item == nil {
				continue
			}
			if field.Tag.Get("cast") == "int32" {
				item = int64(int32(item.(int64)))
			}
			if field.Tag.Get("set") == "true" {
				item = canonicalSet(item.([]any))
			}
			out[name] = item
		}
		if value.Type() == reflect.TypeFor[ProjectedVolume]() {
			sources := []any{}
			if groups, ok := out["sources"].([]any); ok {
				for _, item := range groups {
					group := item.(map[string]any)
					for _, key := range []string{
						"secret", "configMap", "downwardAPI", "serviceAccountToken",
					} {
						if list, ok := group[key].([]any); ok {
							for _, source := range list {
								sources = append(sources, map[string]any{key: source})
							}
						} else if source, ok := group[key]; ok {
							sources = append(sources, map[string]any{key: source})
						}
					}
				}
			}
			out["sources"] = sources
		}
		if value.Type() == reflect.TypeFor[SeccompProfile]() && out["type"] != "Localhost" {
			delete(out, "localhostProfile")
		}
		return out, nil
	case reflect.Slice:
		out := make([]any, value.Len())
		for i := range value.Len() {
			item, err := statefulSetObservedValue(value.Index(i))
			if err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	case reflect.Map:
		out := map[string]any{}
		for _, key := range value.MapKeys() {
			out[key.String()] = value.MapIndex(key).Interface()
		}
		return out, nil
	default:
		return value.Interface(), nil
	}
}
