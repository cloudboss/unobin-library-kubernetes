package v1

import (
	"encoding/json"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
)

func (r *DaemonSetResource) expand(observed *DaemonSetSpec) (*appsv1.DaemonSet, error) {
	metadata, err := expandValue(reflect.ValueOf(r.Metadata), reflect.Value{}, "metadata")
	if err != nil {
		return nil, err
	}
	m := metadata.(map[string]any)
	if r.Metadata.Namespace == nil {
		m["namespace"] = "default"
	}
	var priorValue reflect.Value
	if observed != nil {
		priorValue = reflect.ValueOf(*observed)
	}
	spec, err := expandValue(reflect.ValueOf(r.Spec), priorValue, "spec")
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(map[string]any{
		"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": m, "spec": spec,
	})
	if err != nil {
		return nil, err
	}
	obj := &appsv1.DaemonSet{}
	if err := json.Unmarshal(encoded, obj); err != nil {
		return nil, fmt.Errorf("convert DaemonSet: %w", err)
	}
	return obj, nil
}
func (r *DaemonSetResource) output(obj *appsv1.DaemonSet, server string) (
	*DaemonSetResourceOutput, error,
) {
	if obj.UID == "" {
		return nil, fmt.Errorf("DaemonSet has no Kubernetes UID")
	}
	obj = obj.DeepCopy()
	filterMetadata(obj.Labels, r.Metadata.Labels, r.IgnoreLabels)
	filterMetadata(obj.Annotations, r.Metadata.Annotations, r.IgnoreAnnotations)
	projectRolloutAnnotation(obj.Spec.Template.Annotations, r.Spec.Template.Metadata.Annotations)
	data, err := nativeMap(obj.ObjectMeta)
	if err != nil {
		return nil, err
	}
	out := &DaemonSetResourceOutput{
		Server: server, APIVersion: "apps/v1", Kind: "DaemonSet",
		Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
	}
	if err := readValue(reflect.ValueOf(&out.Metadata).Elem(), data); err != nil {
		return nil, err
	}
	out.Spec, err = normalizedDaemonSetSpec(obj.Spec)
	return out, err
}

func normalizedDaemonSetSpec(spec appsv1.DaemonSetSpec) (DaemonSetSpec, error) {
	pod, err := normalizedSpec(appsv1.DeploymentSpec{Template: spec.Template})
	if err != nil {
		return DaemonSetSpec{}, err
	}
	data, err := nativeMap(spec)
	if err != nil {
		return DaemonSetSpec{}, err
	}
	var result DaemonSetSpec
	if err := readValue(reflect.ValueOf(&result).Elem(), data); err != nil {
		return DaemonSetSpec{}, err
	}
	result.Template = DaemonSetPodTemplate{
		Metadata: pod.Template.Metadata, Spec: &pod.Template.Spec,
	}
	return result, nil
}
