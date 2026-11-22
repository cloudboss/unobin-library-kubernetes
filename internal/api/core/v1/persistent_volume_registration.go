package v1

import (
	"context"
	"fmt"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *PersistentVolumeResource) ResourceDefinition() runtime.ResourceDefinition[
	PersistentVolumeResource, *PersistentVolumeResourceOutput, *config.Configuration,
] {
	field := runtime.InputField(func(r *PersistentVolumeResource) *PersistentVolumeObject {
		return &r.Object
	})
	return runtime.ResourceDefinition[
		PersistentVolumeResource, *PersistentVolumeResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[PersistentVolumeResource]{
			runtime.EqualBy(field, pvObjectsEqual),
			runtime.EqualBy(runtime.InputField(func(r *PersistentVolumeResource) **string {
				return &r.CreateTimeout
			}), func(before, after *string) bool {
				left, err := pvcCreateDuration(before)
				right, other := pvcCreateDuration(after)
				if err != nil || other != nil {
					return ptr.Equal(before, after)
				}
				return left == right
			}),
			runtime.EqualBy(runtime.InputField(func(r *PersistentVolumeResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *PersistentVolumeResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		StableID: func(_ PersistentVolumeResource, out *PersistentVolumeResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing PersistentVolume server or UID")
			}
			return out.Server + "/" + out.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			PersistentVolumeResource, *PersistentVolumeResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[PersistentVolumeResource]{
				runtime.ReplaceWhen(field, func(before, after PersistentVolumeObject) bool {
					return pvReplacement(&PersistentVolumeResource{Object: after},
						persistentVolumePrior{Inputs: PersistentVolumeResource{Object: before}})
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

// PersistentVolumeRegistration exposes schema types and uses the observed PV during planning.
func PersistentVolumeRegistration[
	In PersistentVolumeResource, Out *PersistentVolumeResourceOutput, P any,
](base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) persistentVolumeRegistration[P] {
	return persistentVolumeRegistration[P]{base, read, known}
}

type persistentVolumeRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r persistentVolumeRegistration[P]) ValidateInputs(_ context.Context, receiver, _ any) error {
	_, _, err := receiver.(*PersistentVolumeResource).prepare()
	return err
}

func (r persistentVolumeRegistration[P]) ReplacementReasons(receiver, cfg any, p P,
) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r persistentVolumeRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	if _, unknown := pending["object"]; unknown {
		return reasons, nil
	}
	var prior persistentVolumePrior
	capture := &PersistentVolumeResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(slices.Clone(reasons), func(reason string) bool {
		return reason == "object"
	})
	if pvReplacement(receiver.(*PersistentVolumeResource), prior) {
		reasons = append(reasons, "object")
	}
	slices.Sort(reasons)
	return slices.Compact(reasons), nil
}
