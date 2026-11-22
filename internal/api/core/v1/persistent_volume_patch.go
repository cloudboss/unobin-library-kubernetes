package v1

import (
	"maps"
	"reflect"
	"regexp"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func pvMetadata(
	observed, tracked map[string]string, patterns *[]string,
) map[string]string {
	var regexes []*regexp.Regexp
	for _, pattern := range ptr.Deref(patterns, nil) {
		// Historical inputs can predate the native regex rule.
		compiled, err := regexp.Compile(pattern)
		if err == nil {
			regexes = append(regexes, compiled)
		}
	}
	projected := map[string]string{}
	for key, value := range observed {
		_, managed := tracked[key]
		if managed || (!pvcInternalKey(key) && !slices.ContainsFunc(regexes,
			func(pattern *regexp.Regexp) bool { return pattern.MatchString(key) })) {
			projected[key] = value
		}
	}
	return projected
}

func pvMetadataProjectionChanged(before PersistentVolumeResource, after *PersistentVolumeResource,
) bool {
	if !equalConfigMapLists(before.IgnoreAnnotations, after.IgnoreAnnotations) ||
		!equalConfigMapLists(before.IgnoreLabels, after.IgnoreLabels) {
		return true
	}
	sameKeys := func(left, right *map[string]string) bool {
		return maps.EqualFunc(ptr.Deref(left, nil), ptr.Deref(right, nil),
			func(_, _ string) bool { return true })
	}
	old, next := before.Object.Metadata, after.Object.Metadata
	return !sameKeys(old.Annotations, next.Annotations) || !sameKeys(old.Labels, next.Labels)
}

func pvPatch(
	target object.Target, baseline PersistentVolumeObjectOutput, desired *corev1.PersistentVolume,
) []namespacePatchOperation {
	old := baseline.Spec
	next := pvSpecOutput(desired.Spec)
	ops := []namespacePatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	ops = append(ops,
		namespaceMapPatch("/metadata/annotations", baseline.Metadata.Annotations, desired.Annotations)...)
	ops = append(ops,
		namespaceMapPatch("/metadata/labels", baseline.Metadata.Labels, desired.Labels)...)
	if !pvcQuantitiesEqual(old.Capacity, next.Capacity) {
		ops = append(ops, namespacePatchOperation{
			Op: "replace", Path: "/spec/capacity", Value: next.Capacity,
		})
	}
	if !slices.Equal(old.AccessModes, next.AccessModes) {
		ops = append(ops, namespacePatchOperation{
			Op: "replace", Path: "/spec/accessModes", Value: next.AccessModes,
		})
	}
	if old.PersistentVolumeReclaimPolicy != next.PersistentVolumeReclaimPolicy {
		ops = append(ops, namespacePatchOperation{
			Op: "replace", Path: "/spec/persistentVolumeReclaimPolicy",
			Value: next.PersistentVolumeReclaimPolicy,
		})
	}
	if next.NodeAffinity != nil && !reflect.DeepEqual(old.NodeAffinity, next.NodeAffinity) {
		ops = append(ops, namespacePatchOperation{
			Op: "replace", Path: "/spec/nodeAffinity", Value: desired.Spec.NodeAffinity,
		})
	}
	if old.StorageClassName != next.StorageClassName {
		op := "replace"
		if old.StorageClassName == "" {
			op = "add"
		}
		ops = append(ops, namespacePatchOperation{
			Op: op, Path: "/spec/storageClassName", Value: next.StorageClassName,
		})
	}
	if !slices.Equal(old.MountOptions, next.MountOptions) {
		ops = append(ops, namespacePatchOperation{
			Op: "replace", Path: "/spec/mountOptions", Value: next.MountOptions,
		})
	}
	if next.ClaimRef != nil && !reflect.DeepEqual(old.ClaimRef, next.ClaimRef) {
		ops = append(ops, namespacePatchOperation{
			Op: "replace", Path: "/spec/claimRef", Value: desired.Spec.ClaimRef,
		})
	}
	if len(ops) == 1 {
		return nil
	}
	return ops
}
