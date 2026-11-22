package v1

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func replacePDBMetadata(before, after PodDisruptionBudgetMetadata) bool {
	left, right := normalizePDBMetadata(before), normalizePDBMetadata(after)
	return left.Name != right.Name || left.GenerateName != right.GenerateName ||
		left.Namespace != right.Namespace
}

func (r *PodDisruptionBudgetResource) ResourceDefinition() runtime.ResourceDefinition[
	PodDisruptionBudgetResource, *PodDisruptionBudgetResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *PodDisruptionBudgetResource) *PodDisruptionBudgetMetadata {
		return &r.Metadata
	})
	spec := runtime.InputField(func(r *PodDisruptionBudgetResource) *PodDisruptionBudgetSpec {
		return &r.Spec
	})
	return runtime.ResourceDefinition[
		PodDisruptionBudgetResource, *PodDisruptionBudgetResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ PodDisruptionBudgetResource, out *PodDisruptionBudgetResourceOutput) (
			string, error,
		) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing PodDisruptionBudget server or UID")
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[PodDisruptionBudgetResource]{
			runtime.EqualBy(metadata, func(before, after PodDisruptionBudgetMetadata) bool {
				return reflect.DeepEqual(normalizePDBMetadata(before), normalizePDBMetadata(after))
			}),
			runtime.EqualBy(spec, equalPDBSpec),
			runtime.EqualBy(runtime.InputField(func(r *PodDisruptionBudgetResource) **[]string {
				return &r.IgnoreAnnotations
			}), func(before, after *[]string) bool {
				return slices.Equal(pdbValue(before), pdbValue(after))
			}),
			runtime.EqualBy(runtime.InputField(func(r *PodDisruptionBudgetResource) **[]string {
				return &r.IgnoreLabels
			}), func(before, after *[]string) bool {
				return slices.Equal(pdbValue(before), pdbValue(after))
			}),
		},
		Replace: runtime.Replacement[
			PodDisruptionBudgetResource, *PodDisruptionBudgetResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[PodDisruptionBudgetResource]{spec},
			Rules: []runtime.ReplacementRule[PodDisruptionBudgetResource]{
				runtime.ReplaceWhen(metadata, replacePDBMetadata),
			},
			Drift: []runtime.DriftRule[*PodDisruptionBudgetResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(
					func(out *PodDisruptionBudgetResourceOutput) *PodDisruptionBudgetSpecOutput {
						return &out.Spec
					}), func(before, after PodDisruptionBudgetSpecOutput) bool {
					return !reflect.DeepEqual(before, after)
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
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
