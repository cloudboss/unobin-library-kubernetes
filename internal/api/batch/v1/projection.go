package v1

import (
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

var generatedLabels = []string{
	"batch.kubernetes.io/controller-uid", "batch.kubernetes.io/job-name", "controller-uid", "job-name",
}

func removeGenerated(values map[string]string) {
	for _, key := range generatedLabels {
		delete(values, key)
	}
}

func removeGeneratedAny(value any) {
	if values, ok := value.(map[string]any); ok {
		for _, key := range generatedLabels {
			delete(values, key)
		}
	}
}

func (r *JobResource) output(obj *batchv1.Job, server string) (*JobResourceOutput, error) {
	return r.outputTracked(obj, server, r.Metadata)
}

func (r *JobResource) outputTracked(
	obj *batchv1.Job, server string, tracked Metadata,
) (*JobResourceOutput, error) {
	if obj.UID == "" {
		return nil, fmt.Errorf("job has no Kubernetes UID")
	}
	obj = obj.DeepCopy()
	if obj.Spec.ManualSelector == nil || !*obj.Spec.ManualSelector {
		removeGenerated(obj.Labels)
		if obj.Spec.Selector != nil {
			removeGenerated(obj.Spec.Selector.MatchLabels)
		}
	}
	removeGenerated(obj.Spec.Template.Labels)
	filterMetadata(obj.Labels, tracked.Labels, r.IgnoreLabels)
	filterMetadata(obj.Annotations, tracked.Annotations, r.IgnoreAnnotations)
	data, err := nativeMap(obj.ObjectMeta)
	if err != nil {
		return nil, err
	}
	out := &JobResourceOutput{
		Server: server, APIVersion: "batch/v1", Kind: "Job",
		Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
	}
	if err := readValue(reflect.ValueOf(&out.Metadata).Elem(), data); err != nil {
		return nil, err
	}
	out.Spec, err = normalizedSpec(obj.Spec)
	return out, err
}

func normalizedSpec(spec batchv1.JobSpec) (JobSpec, error) {
	spec = *spec.DeepCopy()
	if spec.PodFailurePolicy != nil {
		for i := range spec.PodFailurePolicy.Rules {
			matcher := spec.PodFailurePolicy.Rules[i].OnExitCodes
			if matcher != nil && matcher.ContainerName != nil && *matcher.ContainerName == "" {
				matcher.ContainerName = nil
			}
		}
	}
	pod := &spec.Template.Spec
	account := pod.ServiceAccountName
	if account == "" {
		account = "default"
	}
	token := regexp.MustCompile(account + "-token-([a-z0-9]{5})")
	isToken := func(name string) bool {
		return strings.HasPrefix(name, "kube-api-access") || token.MatchString(name)
	}
	for i, volume := range pod.Volumes {
		if isToken(volume.Name) {
			pod.Volumes = append(pod.Volumes[:i], pod.Volumes[i+1:]...)
			break
		}
	}
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for i := range containers {
			for j, mount := range containers[i].VolumeMounts {
				if isToken(mount.Name) {
					containers[i].VolumeMounts = append(containers[i].VolumeMounts[:j],
						containers[i].VolumeMounts[j+1:]...)
					break
				}
			}
			for j := range containers[i].VolumeMounts {
				mount := &containers[i].VolumeMounts[j]
				if mount.MountPropagation == nil {
					mount.MountPropagation = new(corev1.MountPropagationNone)
				}
			}
		}
	}
	data, err := nativeMap(spec)
	if err != nil {
		return JobSpec{}, err
	}
	var out JobSpec
	err = readValue(reflect.ValueOf(&out).Elem(), data)
	return out, err
}

func internalMetadata(key string) bool {
	parsed, err := url.Parse("//" + key)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "app.kubernetes.io" || host == "service.beta.kubernetes.io" {
		return false
	}
	return strings.HasSuffix(host, "kubernetes.io") ||
		strings.Contains(key, "deprecated.daemonset.template.generation")
}

func filterMetadata(values map[string]string, tracked *map[string]string, ignore *[]string) {
	for key := range values {
		if tracked != nil {
			if _, ok := (*tracked)[key]; ok {
				continue
			}
		}
		filtered := internalMetadata(key)
		if ignore != nil {
			for _, pattern := range *ignore {
				matched, err := regexp.MatchString(pattern, key)
				filtered = filtered || err == nil && matched
			}
		}
		if filtered {
			delete(values, key)
		}
	}
}
