package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *PersistentVolumeClaimResource) ResourceDefinition() runtime.ResourceDefinition[
	PersistentVolumeClaimResource, *PersistentVolumeClaimResourceOutput, *config.Configuration,
] {
	field := runtime.InputField(func(r *PersistentVolumeClaimResource) *PersistentVolumeClaimObject {
		return &r.Object
	})
	return runtime.ResourceDefinition[
		PersistentVolumeClaimResource, *PersistentVolumeClaimResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(
			_ context.Context, r PersistentVolumeClaimResource, _ *config.Configuration,
		) error {
			_, err := r.prepare()
			return err
		},
		Equality: []runtime.InputEqualityRule[PersistentVolumeClaimResource]{
			runtime.EqualBy(field, pvcObjectsEqual),
			runtime.EqualBy(runtime.InputField(func(r *PersistentVolumeClaimResource) **bool {
				return &r.WaitUntilBound
			}), func(before, after *bool) bool { return ptr.Deref(before, true) == ptr.Deref(after, true) }),
			runtime.EqualBy(runtime.InputField(func(r *PersistentVolumeClaimResource) **string {
				return &r.CreateTimeout
			}), func(before, after *string) bool {
				left, err := pvcCreateDuration(before)
				right, other := pvcCreateDuration(after)
				if err != nil || other != nil {
					return ptr.Deref(before, "") == ptr.Deref(after, "")
				}
				return left == right
			}),
			runtime.EqualBy(runtime.InputField(func(r *PersistentVolumeClaimResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *PersistentVolumeClaimResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		StableID: func(_ PersistentVolumeClaimResource, output *PersistentVolumeClaimResourceOutput,
		) (string, error) {
			if output == nil || output.Server == "" || output.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing PersistentVolumeClaim server or UID")
			}
			return output.Server + "/" + output.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			PersistentVolumeClaimResource, *PersistentVolumeClaimResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[PersistentVolumeClaimResource]{
				runtime.ReplaceWhen(field, func(before, after PersistentVolumeClaimObject) bool {
					replace, _ := pvcReplacement(&PersistentVolumeClaimResource{Object: after},
						persistentVolumeClaimPrior{Inputs: PersistentVolumeClaimResource{Object: before}})
					return replace
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.CurrentContext }),
			},
		},
	}
}
