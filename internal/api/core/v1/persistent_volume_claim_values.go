package v1

import (
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func pvcQuantities(values *map[string]string) (corev1.ResourceList, error) {
	if len(ptr.Deref(values, nil)) == 0 {
		return nil, nil
	}
	result := corev1.ResourceList{}
	for key, value := range *values {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("parse quantity %s=%q: %w", key, value, err)
		}
		result[corev1.ResourceName(key)] = quantity
	}
	return result, nil
}

func pvcCreateDuration(value *string) (time.Duration, error) {
	duration, err := time.ParseDuration(ptr.Deref(value, "5m"))
	if err != nil {
		return 0, fmt.Errorf("parse create-timeout: %w", err)
	}
	return duration, nil
}

func (r *PersistentVolumeClaimResource) prepare() (*corev1.PersistentVolumeClaim, error) {
	if _, err := pvcCreateDuration(r.CreateTimeout); err != nil {
		return nil, err
	}
	limits, err := pvcQuantities(r.Object.Spec.Resources.Limits)
	if err != nil {
		return nil, err
	}
	requests, err := pvcQuantities(r.Object.Spec.Resources.Requests)
	if err != nil {
		return nil, err
	}
	metadata := normalizeConfigMap(ConfigMapObject{Metadata: r.Object.Metadata}).Metadata
	spec := r.Object.Spec
	result := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: metadata.Name, GenerateName: metadata.GenerateName,
			Namespace: metadata.Namespace, Annotations: metadata.Annotations, Labels: metadata.Labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources:  corev1.VolumeResourceRequirements{Limits: limits, Requests: requests},
			VolumeName: ptr.Deref(spec.VolumeName, ""),
		},
	}
	for _, mode := range pvcSet(spec.AccessModes) {
		result.Spec.AccessModes = append(result.Spec.AccessModes, corev1.PersistentVolumeAccessMode(mode))
	}
	if spec.StorageClassName != nil && *spec.StorageClassName != "" {
		result.Spec.StorageClassName = ptr.To(*spec.StorageClassName)
	}
	if spec.VolumeMode != nil {
		result.Spec.VolumeMode = ptr.To(corev1.PersistentVolumeMode(
			*spec.VolumeMode))
	}
	if spec.Selector != nil {
		result.Spec.Selector = &metav1.LabelSelector{MatchLabels: maps.Clone(
			ptr.Deref(spec.Selector.MatchLabels, nil))}
		for _, expression := range ptr.Deref(spec.Selector.MatchExpressions, nil) {
			result.Spec.Selector.MatchExpressions = append(result.Spec.Selector.MatchExpressions,
				metav1.LabelSelectorRequirement{Key: ptr.Deref(expression.Key, ""),
					Operator: metav1.LabelSelectorOperator(ptr.Deref(expression.Operator, "")),
					Values:   pvcSet(ptr.Deref(expression.Values, nil))})
		}
	}
	return result, nil
}

func pvcSet(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)
	result = slices.Compact(result)
	if result == nil {
		return []string{}
	}
	return result
}

func pvcQuantityStrings(values corev1.ResourceList) map[string]string {
	result := map[string]string{}
	for key, value := range values {
		result[string(key)] = value.String()
	}
	return result
}

func pvcSpecOutput(spec corev1.PersistentVolumeClaimSpec) PersistentVolumeClaimSpecOutput {
	result := PersistentVolumeClaimSpecOutput{
		AccessModes: []string{},
		Resources: PersistentVolumeClaimResourcesOutput{Limits: pvcQuantityStrings(spec.Resources.Limits),
			Requests: pvcQuantityStrings(spec.Resources.Requests)}, VolumeName: spec.VolumeName,
		StorageClassName: ptr.Deref(spec.StorageClassName, ""),
		VolumeMode:       string(ptr.Deref(spec.VolumeMode, corev1.PersistentVolumeMode(""))),
	}
	for _, mode := range spec.AccessModes {
		result.AccessModes = append(result.AccessModes,
			string(mode))
	}
	result.AccessModes = pvcSet(result.AccessModes)
	if spec.Selector != nil {
		labels := maps.Clone(spec.Selector.MatchLabels)
		if labels == nil {
			labels = map[string]string{}
		}
		expressions := []PersistentVolumeClaimExpression{}
		for _, expression := range spec.Selector.MatchExpressions {
			expressions = append(expressions, PersistentVolumeClaimExpression{Key: ptr.To(expression.Key),
				Operator: ptr.To(string(expression.Operator)), Values: ptr.To(pvcSet(expression.Values))})
		}
		result.Selector = &PersistentVolumeClaimSelector{MatchLabels: &labels,
			MatchExpressions: &expressions}
	}
	return result
}

func pvcInternalKey(key string) bool {
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

func pvcMetadata(
	observed, tracked map[string]string, patterns *[]string,
) (map[string]string, error) {
	regexes := []*regexp.Regexp{}
	for _, pattern := range ptr.Deref(patterns, nil) {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		regexes = append(regexes, compiled)
	}
	result := map[string]string{}
	for key, value := range observed {
		if _, exists := tracked[key]; exists {
			result[key] = value
			continue
		}
		ignored := pvcInternalKey(key)
		for _, pattern := range regexes {
			ignored = ignored || pattern.MatchString(key)
		}
		if !ignored {
			result[key] = value
		}
	}
	return result, nil
}

func pvcQuantitiesEqual(before, after map[string]string) bool {
	left, err := pvcQuantities(&before)
	right, other := pvcQuantities(&after)
	if err != nil || other != nil {
		return maps.Equal(before, after)
	}
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		next, exists := right[key]
		if !exists || value.Cmp(next) != 0 {
			return false
		}
	}
	return true
}

func pvcStorageComparison(before, after map[string]string) (int, error) {
	left, err := pvcQuantities(&map[string]string{"storage": before["storage"]})
	if err != nil {
		return 0, err
	}
	right, err := pvcQuantities(&map[string]string{"storage": after["storage"]})
	if err != nil {
		return 0, err
	}
	previous, desired := left[corev1.ResourceStorage], right[corev1.ResourceStorage]
	return previous.Cmp(desired), nil
}

func pvcObjectsEqual(before, after PersistentVolumeClaimObject) bool {
	left := normalizeConfigMap(ConfigMapObject{Metadata: before.Metadata}).Metadata
	right := normalizeConfigMap(ConfigMapObject{Metadata: after.Metadata}).Metadata
	l, err := (&PersistentVolumeClaimResource{Object: before}).prepare()
	r, other := (&PersistentVolumeClaimResource{Object: after}).prepare()
	if err != nil || other != nil {
		return reflect.DeepEqual(before, after)
	}
	ls, rs := pvcSpecOutput(l.Spec), pvcSpecOutput(r.Spec)
	if !pvcQuantitiesEqual(ls.Resources.Limits, rs.Resources.Limits) ||
		!pvcQuantitiesEqual(ls.Resources.Requests, rs.Resources.Requests) {
		return false
	}
	ls.Resources, rs.Resources = PersistentVolumeClaimResourcesOutput{},
		PersistentVolumeClaimResourcesOutput{}
	return reflect.DeepEqual(left, right) && reflect.DeepEqual(ls, rs)
}

func pvcReplacement(r *PersistentVolumeClaimResource, p persistentVolumeClaimPrior) (bool, error) {
	before := p.Inputs.Object
	after := r.Object
	bm := normalizeConfigMap(ConfigMapObject{Metadata: before.Metadata}).Metadata
	am := normalizeConfigMap(ConfigMapObject{Metadata: after.Metadata}).Metadata
	old, err := (&PersistentVolumeClaimResource{Object: before}).prepare()
	if err != nil {
		return false, err
	}
	desired, err := r.prepare()
	if err != nil {
		return false, err
	}
	bs, as := pvcSpecOutput(old.Spec), pvcSpecOutput(desired.Spec)
	actual := p.Outputs
	if actual == nil {
		actual = p.Observed
	}
	if actual != nil {
		if before.Metadata.Name == nil {
			bm.Name = actual.Object.Metadata.Name
		}
		if before.Spec.VolumeName == nil {
			bs.VolumeName = actual.Object.Spec.VolumeName
		}
		if before.Spec.StorageClassName == nil {
			bs.StorageClassName = actual.Object.Spec.StorageClassName
		}
		if before.Spec.VolumeMode == nil {
			bs.VolumeMode = actual.Object.Spec.VolumeMode
		}
	}
	replace := (after.Metadata.Name != nil && bm.Name != am.Name) ||
		bm.GenerateName != am.GenerateName || bm.Namespace != am.Namespace ||
		!slices.Equal(bs.AccessModes, as.AccessModes) || !reflect.DeepEqual(bs.Selector, as.Selector) ||
		!pvcQuantitiesEqual(bs.Resources.Limits, as.Resources.Limits) ||
		(after.Spec.VolumeName != nil && bs.VolumeName != as.VolumeName) ||
		(after.Spec.StorageClassName != nil && bs.StorageClassName != as.StorageClassName) ||
		(after.Spec.VolumeMode != nil && bs.VolumeMode != as.VolumeMode)
	if !pvcQuantitiesEqual(bs.Resources.Requests, as.Resources.Requests) {
		comparison, err := pvcStorageComparison(bs.Resources.Requests, as.Resources.Requests)
		if err != nil {
			return false, err
		}
		replace = replace || comparison > 0
	}
	return replace, nil
}
