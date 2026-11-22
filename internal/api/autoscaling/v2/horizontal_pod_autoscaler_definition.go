package v2

import (
	"context"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *HorizontalPodAutoscalerResource) ResourceDefinition() runtime.ResourceDefinition[
	HorizontalPodAutoscalerResource, *HorizontalPodAutoscalerResourceOutput, *config.Configuration,
] {
	obj := runtime.InputField(func(r *HorizontalPodAutoscalerResource) *HPAObject { return &r.Object })
	return runtime.ResourceDefinition[HorizontalPodAutoscalerResource,
		*HorizontalPodAutoscalerResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input HorizontalPodAutoscalerResource,
			_ *config.Configuration) error {
			_, err := expandHPASpec(input.Object.Spec)
			return err
		},
		StableID: func(_ HorizontalPodAutoscalerResource, o *HorizontalPodAutoscalerResourceOutput) (
			string, error) {
			if o == nil || o.Server == "" || o.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing HPA server or UID")
			}
			return o.Server + "/" + o.Object.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[HorizontalPodAutoscalerResource]{
			runtime.EqualBy(obj, equalHPAObject),
			runtime.EqualBy(runtime.InputField(func(r *HorizontalPodAutoscalerResource) **[]string {
				return &r.IgnoreLabels
			}), equalHPALists),
			runtime.EqualBy(runtime.InputField(func(r *HorizontalPodAutoscalerResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalHPALists),
		},
		Replace: runtime.Replacement[HorizontalPodAutoscalerResource,
			*HorizontalPodAutoscalerResourceOutput, *config.Configuration]{
			Rules: []runtime.ReplacementRule[HorizontalPodAutoscalerResource]{runtime.ReplaceWhen(obj,
				func(before, after HPAObject) bool {
					left, right := hpaMetadata(before.Metadata), hpaMetadata(after.Metadata)
					return (after.Metadata.Name != nil && left.Name != right.Name) ||
						left.Namespace != right.Namespace || left.GenerateName != right.GenerateName
				})},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.CurrentContext }),
			},
		},
	}
}

func equalHPALists(a, b *[]string) bool {
	return reflect.DeepEqual(hpaValue(a), hpaValue(b)) ||
		len(hpaValue(a)) == 0 && len(hpaValue(b)) == 0
}

func equalHPAObject(before, after HPAObject) bool {
	left, right := hpaMetadata(before.Metadata), hpaMetadata(after.Metadata)
	if after.Metadata.Name == nil {
		right.Name = left.Name
	}
	if !reflect.DeepEqual(left, right) {
		return false
	}
	a, b := before.Spec, after.Spec
	a.MinReplicas = new(hpaMin(a.MinReplicas))
	b.MinReplicas = new(hpaMin(b.MinReplicas))
	if b.Metric == nil {
		b.Metric = a.Metric
	}
	if b.Behavior == nil {
		b.Behavior = a.Behavior
	} else if a.Behavior != nil {
		copy := *b.Behavior
		b.Behavior = &copy
		if b.Behavior.ScaleUp == nil {
			b.Behavior.ScaleUp = a.Behavior.ScaleUp
		}
		if b.Behavior.ScaleDown == nil {
			b.Behavior.ScaleDown = a.Behavior.ScaleDown
		}
	}
	if a.ScaleTargetRef.APIVersion == nil {
		a.ScaleTargetRef.APIVersion = new("")
	}
	if b.ScaleTargetRef.APIVersion == nil {
		b.ScaleTargetRef.APIVersion = new("")
	}
	return reflect.DeepEqual(a, b)
}
