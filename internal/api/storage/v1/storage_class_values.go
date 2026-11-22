package v1

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func (r *StorageClassResource) createRequest() *storagev1.StorageClass {
	policy := corev1.PersistentVolumeReclaimPolicy(storageClassPolicy(r.ReclaimPolicy))
	return &storagev1.StorageClass{
		ObjectMeta:           storageClassMetadata(r.Metadata),
		Provisioner:          r.Provisioner,
		Parameters:           ptr.Deref(r.Parameters, nil),
		ReclaimPolicy:        &policy,
		VolumeBindingMode:    new(storagev1.VolumeBindingMode(storageClassBinding(r.VolumeBindingMode))),
		AllowVolumeExpansion: new(storageClassExpansion(r.AllowVolumeExpansion)),
		MountOptions:         storageClassSet(ptr.Deref(r.MountOptions, nil)),
		AllowedTopologies:    storageClassTopologies(r.AllowedTopologies),
	}
}

func storageClassMetadata(input StorageClassMetadata) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: ptr.Deref(input.Name, ""), GenerateName: ptr.Deref(input.GenerateName, ""),
		Annotations: ptr.Deref(input.Annotations, nil), Labels: ptr.Deref(input.Labels, nil),
	}
}

func storageClassPolicy(value *string) string {
	return ptr.Deref(value, string(corev1.PersistentVolumeReclaimDelete))
}

func storageClassBinding(value *string) string {
	return ptr.Deref(value, string(storagev1.VolumeBindingImmediate))
}

func storageClassExpansion(value *bool) bool {
	return ptr.Deref(value, true)
}

func storageClassSet(items []string) []string {
	result := append([]string{}, items...)
	slices.Sort(result)
	return slices.Compact(result)
}

func storageClassTopologies(terms *[]StorageClassTopologyTerm) []corev1.TopologySelectorTerm {
	var result []corev1.TopologySelectorTerm
	for _, term := range ptr.Deref(terms, nil) {
		native := corev1.TopologySelectorTerm{}
		for _, expression := range ptr.Deref(term.MatchLabelExpressions, nil) {
			native.MatchLabelExpressions = append(native.MatchLabelExpressions,
				corev1.TopologySelectorLabelRequirement{
					Key:    ptr.Deref(expression.Key, ""),
					Values: storageClassSet(ptr.Deref(expression.Values, nil)),
				})
		}
		result = append(result, native)
	}
	return result
}

func storageClassTopologyOutput(
	terms []corev1.TopologySelectorTerm,
) []StorageClassTopologyTermOutput {
	result := []StorageClassTopologyTermOutput{}
	if terms == nil {
		return result
	}
	expressions := []StorageClassTopologyExpressionOutput{}
	for _, term := range terms {
		if len(term.MatchLabelExpressions) == 0 {
			continue
		}
		expressions = make([]StorageClassTopologyExpressionOutput, 0, len(term.MatchLabelExpressions))
		for _, expression := range term.MatchLabelExpressions {
			expressions = append(expressions, StorageClassTopologyExpressionOutput{
				Key: expression.Key, Values: storageClassSet(expression.Values),
			})
		}
	}
	return append(result, StorageClassTopologyTermOutput{MatchLabelExpressions: expressions})
}

func (r *StorageClassResource) output(
	observed *storagev1.StorageClass, baseline *StorageClassResourceOutput, tracked metav1.ObjectMeta,
) *StorageClassResourceOutput {
	result := &StorageClassResourceOutput{
		Server: baseline.Server, APIVersion: storageClassAPIVersion, Kind: storageClassKind,
		Metadata: StorageClassMetadataOutput{
			Name: observed.Name, GenerateName: observed.GenerateName, UID: string(observed.UID),
			Annotations: projectStorageClassMetadata(
				observed.Annotations, tracked.Annotations, r.IgnoreAnnotations),
			Labels: projectStorageClassMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels),
		},
		Provisioner: observed.Provisioner, Parameters: map[string]string{},
		ReclaimPolicy: string(ptr.Deref(observed.ReclaimPolicy, "")),
		VolumeBindingMode: string(ptr.Deref(observed.VolumeBindingMode,
			storagev1.VolumeBindingMode(baseline.VolumeBindingMode))),
		AllowVolumeExpansion: ptr.Deref(observed.AllowVolumeExpansion, baseline.AllowVolumeExpansion),
		MountOptions:         storageClassSet(observed.MountOptions),
		AllowedTopologies:    baseline.AllowedTopologies,
	}
	maps.Copy(result.Parameters, observed.Parameters)
	if observed.AllowedTopologies != nil {
		result.AllowedTopologies = storageClassTopologyOutput(observed.AllowedTopologies)
	}
	return result
}

func projectStorageClassMetadata(
	observed, tracked map[string]string, expressions *[]string,
) map[string]string {
	var ignored []*regexp.Regexp
	for _, expression := range ptr.Deref(expressions, nil) {
		pattern, err := regexp.Compile(expression)
		if err == nil {
			ignored = append(ignored, pattern)
		}
	}
	projected := map[string]string{}
	for key, value := range observed {
		if _, present := tracked[key]; present {
			projected[key] = value
			continue
		}
		internal := storageClassInternalKey(key)
		for _, pattern := range ignored {
			internal = internal || pattern.MatchString(key)
		}
		if !internal {
			projected[key] = value
		}
	}
	return projected
}

func storageClassInternalKey(key string) bool {
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

type storageClassPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func storageClassMetadataPatch(
	path string, observed, prior, desired map[string]string, ignored *[]string,
) []storageClassPatchOperation {
	tracked := make(map[string]string, len(prior)+len(desired))
	maps.Copy(tracked, prior)
	maps.Copy(tracked, desired)
	before := projectStorageClassMetadata(observed, tracked, ignored)
	if maps.Equal(before, desired) {
		return nil
	}
	if len(before) == 0 {
		return []storageClassPatchOperation{{Op: "add", Path: path, Value: desired}}
	}
	keys := make([]string, 0, len(before)+len(desired))
	for key := range before {
		keys = append(keys, key)
	}
	for key := range desired {
		if _, present := before[key]; !present {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var operations []storageClassPatchOperation
	for _, key := range keys {
		value, present := desired[key]
		previous, existed := before[key]
		if present && existed && value == previous {
			continue
		}
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operation := storageClassPatchOperation{Path: path + "/" + pointer}
		switch {
		case !present:
			operation.Op = "remove"
		case !existed:
			operation.Op, operation.Value = "add", value
		default:
			operation.Op, operation.Value = "replace", value
		}
		operations = append(operations, operation)
	}
	return operations
}
