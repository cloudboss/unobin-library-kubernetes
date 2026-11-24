package v1

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (*HorizontalPodAutoscalerResource) ResourceDefinition() runtime.ResourceDefinition[
	HorizontalPodAutoscalerResource, *HorizontalPodAutoscalerResourceOutput, *config.Configuration,
] {
	object := runtime.InputField(func(r *HorizontalPodAutoscalerResource) *HPAObject {
		return &r.Object
	})
	return runtime.ResourceDefinition[
		HorizontalPodAutoscalerResource, *HorizontalPodAutoscalerResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ HorizontalPodAutoscalerResource, o *HorizontalPodAutoscalerResourceOutput) (
			string, error,
		) {
			if o == nil || o.Server == "" || o.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing HPA server or UID")
			}
			return o.Server + "/" + o.Object.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[HorizontalPodAutoscalerResource]{
			runtime.EqualBy(object, equalHPAObject),
			runtime.EqualBy(runtime.InputField(func(r *HorizontalPodAutoscalerResource) **[]string {
				return &r.IgnoreLabels
			}), equalHPALists),
			runtime.EqualBy(runtime.InputField(func(r *HorizontalPodAutoscalerResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalHPALists),
		},
		Replace: runtime.Replacement[HorizontalPodAutoscalerResource,
			*HorizontalPodAutoscalerResourceOutput, *config.Configuration]{
			Rules: []runtime.ReplacementRule[HorizontalPodAutoscalerResource]{
				runtime.ReplaceWhen(object, func(before, after HPAObject) bool {
					return hpaIdentityChanged(after.Metadata, hpaPrior{
						Inputs: HorizontalPodAutoscalerResource{Object: before},
					})
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func equalHPALists(a, b *[]string) bool {
	return slices.Equal(hpaValue(a), hpaValue(b))
}

func equalHPAObject(a, b HPAObject) bool {
	left, right := a.Metadata, b.Metadata
	if hpaValue(left.Name) != hpaValue(right.Name) ||
		hpaValue(left.GenerateName) != hpaValue(right.GenerateName) ||
		hpaNamespace(left.Namespace) != hpaNamespace(right.Namespace) ||
		!maps.Equal(hpaValue(left.Annotations), hpaValue(right.Annotations)) ||
		!maps.Equal(hpaValue(left.Labels), hpaValue(right.Labels)) {
		return false
	}
	before, after := a.Spec, b.Spec
	return before.MaxReplicas == after.MaxReplicas &&
		hpaMin(before.MinReplicas) == hpaMin(after.MinReplicas) &&
		expandHPARef(before.ScaleTargetRef) == expandHPARef(after.ScaleTargetRef) &&
		(after.TargetCPUUtilizationPercentage == nil ||
			before.TargetCPUUtilizationPercentage != nil &&
				*before.TargetCPUUtilizationPercentage == *after.TargetCPUUtilizationPercentage)
}

func HorizontalPodAutoscalerRegistration[In HorizontalPodAutoscalerResource,
	Out *HorizontalPodAutoscalerResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) hpaRegistration[P] {
	return hpaRegistration[P]{base, read, known}
}

type hpaRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r hpaRegistration[P]) ReplacementReasons(receiver, cfg any, p P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r hpaRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	if _, unknown := pending["object"]; unknown {
		return reasons, nil
	}
	// Only Read accepts the opaque SDK prior value and exposes its typed identity.
	var prior hpaPrior
	capture := &HorizontalPodAutoscalerResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(slices.Clone(reasons), func(s string) bool { return s == "object" })
	if hpaIdentityChanged(receiver.(*HorizontalPodAutoscalerResource).Object.Metadata, prior) {
		reasons = append(reasons, "object")
	}
	slices.Sort(reasons)
	return reasons, nil
}

func hpaIdentityChanged(desired HPAMetadata, p hpaPrior) bool {
	before := p.Inputs.Object.Metadata
	if before.Name == nil {
		if p.Observed != nil && p.Observed.Object.Metadata.Name != "" {
			before.Name = &p.Observed.Object.Metadata.Name
		} else if p.Outputs != nil {
			before.Name = &p.Outputs.Object.Metadata.Name
		}
	}
	return (desired.Name != nil && hpaValue(before.Name) != *desired.Name) ||
		hpaNamespace(before.Namespace) != hpaNamespace(desired.Namespace) ||
		hpaValue(before.GenerateName) != hpaValue(desired.GenerateName)
}
