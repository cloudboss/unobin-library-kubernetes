package v1

import (
	"fmt"
	"maps"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *LimitRangeResource) prepare() (*corev1.LimitRange, error) {
	spec, err := prepareLimitRangeSpec(r.Object.Spec)
	if err != nil {
		return nil, err
	}
	return &corev1.LimitRange{
		ObjectMeta: limitRangeMetadata(r.Object.Metadata), Spec: spec,
	}, nil
}

func limitRangeMetadata(input ConfigMapMetadata) metav1.ObjectMeta {
	namespace := "default"
	if input.Namespace != nil {
		namespace = *input.Namespace
	}
	return metav1.ObjectMeta{
		Name: configMapValue(input.Name), GenerateName: configMapValue(input.GenerateName),
		Namespace: namespace, Annotations: configMapValue(input.Annotations),
		Labels: configMapValue(input.Labels),
	}
}

func limitRangeItems(spec *LimitRangeSpec) []LimitRangeItem {
	return configMapValue(configMapValue(spec).Limit)
}

func prepareLimitRangeSpec(input *LimitRangeSpec) (corev1.LimitRangeSpec, error) {
	var spec corev1.LimitRangeSpec
	for i, item := range limitRangeItems(input) {
		result := corev1.LimitRangeItem{Type: corev1.LimitType(configMapValue(item.Type))}
		for _, field := range []struct {
			name   string
			values *map[string]string
			result *corev1.ResourceList
		}{
			{"default", item.Default, &result.Default},
			{"default-request", item.DefaultRequest, &result.DefaultRequest},
			{"max", item.Max, &result.Max},
			{"max-limit-request-ratio", item.MaxLimitRequestRatio, &result.MaxLimitRequestRatio},
			{"min", item.Min, &result.Min},
		} {
			quantities, err := pvcQuantities(field.values)
			if err != nil {
				return corev1.LimitRangeSpec{}, fmt.Errorf(
					"LimitRange object.spec.limit[%d].%s: %w", i, field.name, err)
			}
			*field.result = quantities
		}
		spec.Limits = append(spec.Limits, result)
	}
	return spec, nil
}

func (r *LimitRangeResource) project(
	live *corev1.LimitRange, server string, tracked ConfigMapMetadata,
) (*LimitRangeResourceOutput, error) {
	annotations, err := projectConfigMapMetadata(
		live.Annotations, configMapValue(tracked.Annotations), r.IgnoreAnnotations)
	if err != nil {
		return nil, fmt.Errorf("filter LimitRange annotations: %w", err)
	}
	labels, err := projectConfigMapMetadata(
		live.Labels, configMapValue(tracked.Labels), r.IgnoreLabels)
	if err != nil {
		return nil, fmt.Errorf("filter LimitRange labels: %w", err)
	}
	return &LimitRangeResourceOutput{
		Server: server, APIVersion: "v1", Kind: "LimitRange",
		Object: LimitRangeObjectOutput{
			Metadata: ConfigMapMetadataOutput{
				Name: live.Name, GenerateName: live.GenerateName, Namespace: live.Namespace,
				UID: string(live.UID), Annotations: annotations, Labels: labels,
			},
			Spec: projectLimitRangeSpec(live.Spec),
		},
	}, nil
}

func projectLimitRangeSpec(spec corev1.LimitRangeSpec) *LimitRangeSpecOutput {
	if len(spec.Limits) == 0 {
		return nil
	}
	output := &LimitRangeSpecOutput{Limit: make([]LimitRangeItemOutput, 0, len(spec.Limits))}
	for _, item := range spec.Limits {
		output.Limit = append(output.Limit, LimitRangeItemOutput{
			Type:                 string(item.Type),
			Default:              pvcQuantityStrings(item.Default),
			DefaultRequest:       pvcQuantityStrings(item.DefaultRequest),
			Max:                  pvcQuantityStrings(item.Max),
			MaxLimitRequestRatio: pvcQuantityStrings(item.MaxLimitRequestRatio),
			Min:                  pvcQuantityStrings(item.Min),
		})
	}
	return output
}

func observedLimitRangeSpec(output *LimitRangeSpecOutput) (corev1.LimitRangeSpec, error) {
	if output == nil {
		return corev1.LimitRangeSpec{}, nil
	}
	items := make([]LimitRangeItem, 0, len(output.Limit))
	for _, item := range output.Limit {
		items = append(items, LimitRangeItem{
			Type: &item.Type, Default: &item.Default, DefaultRequest: &item.DefaultRequest,
			Max: &item.Max, MaxLimitRequestRatio: &item.MaxLimitRequestRatio, Min: &item.Min,
		})
	}
	return prepareLimitRangeSpec(&LimitRangeSpec{Limit: &items})
}

func reconcileLimitRangeSpec(
	desired *corev1.LimitRangeSpec, input, prior *LimitRangeSpec, output *LimitRangeSpecOutput,
) (bool, error) {
	observed, err := observedLimitRangeSpec(output)
	if err != nil {
		return false, fmt.Errorf("decode observed spec: %w", err)
	}
	before := limitRangeItems(prior)
	for i, item := range limitRangeItems(input) {
		if i >= len(observed.Limits) {
			break
		}
		want, have := &desired.Limits[i], &observed.Limits[i]
		switch {
		case want.Type == corev1.LimitTypePod:
			have.DefaultRequest = nil
		case item.DefaultRequest == nil:
			want.DefaultRequest = have.DefaultRequest
		case len(want.DefaultRequest) == 0 && i < len(before) &&
			before[i].DefaultRequest != nil && len(*before[i].DefaultRequest) == 0:
			// An explicitly empty request map still allows server defaults after omission.
			have.DefaultRequest = nil
		}
	}
	return !apiequality.Semantic.DeepEqual(observed, *desired), nil
}

func equalLimitRangeObjects(before, after LimitRangeObject) bool {
	if limitRangeIdentity(before.Metadata) != limitRangeIdentity(after.Metadata) ||
		!maps.Equal(configMapValue(before.Metadata.Annotations),
			configMapValue(after.Metadata.Annotations)) ||
		!maps.Equal(configMapValue(before.Metadata.Labels), configMapValue(after.Metadata.Labels)) {
		return false
	}
	left, err := prepareLimitRangeSpec(before.Spec)
	right, other := prepareLimitRangeSpec(after.Spec)
	if err != nil || other != nil {
		return reflect.DeepEqual(before.Spec, after.Spec)
	}
	if !apiequality.Semantic.DeepEqual(left, right) {
		return false
	}
	prior := limitRangeItems(before.Spec)
	for i, item := range limitRangeItems(after.Spec) {
		if (item.DefaultRequest == nil) != (prior[i].DefaultRequest == nil) {
			return false
		}
	}
	return true
}

func limitRangeIdentity(metadata ConfigMapMetadata) (identity struct {
	Name, GenerateName, Namespace string
}) {
	normalized := limitRangeMetadata(metadata)
	identity.Name = normalized.Name
	identity.GenerateName = normalized.GenerateName
	identity.Namespace = normalized.Namespace
	return identity
}
