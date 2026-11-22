package v1

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *StorageClassResource) ResourceDefinition() runtime.ResourceDefinition[
	StorageClassResource, *StorageClassResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *StorageClassResource) *StorageClassMetadata {
		return &r.Metadata
	})
	parameters := runtime.InputField(func(r *StorageClassResource) **map[string]string {
		return &r.Parameters
	})
	binding := runtime.InputField(func(r *StorageClassResource) **string {
		return &r.VolumeBindingMode
	})
	mountOptions := runtime.InputField(func(r *StorageClassResource) **[]string {
		return &r.MountOptions
	})
	topologies := runtime.InputField(func(r *StorageClassResource) **[]StorageClassTopologyTerm {
		return &r.AllowedTopologies
	})
	return runtime.ResourceDefinition[
		StorageClassResource, *StorageClassResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ StorageClassResource, out *StorageClassResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Metadata.UID == "" {
				return "", fmt.Errorf("missing StorageClass server or UID")
			}
			return out.Server + "/" + out.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[StorageClassResource]{
			runtime.EqualBy(metadata, equalStorageClassMetadata),
			runtime.EqualBy(parameters, equalStorageClassParameters),
			runtime.EqualBy(binding, equalStorageClassBinding),
			runtime.EqualBy(mountOptions, func(a, b *[]string) bool {
				return equalStorageClassSet(ptr.Deref(a, nil), ptr.Deref(b, nil))
			}),
			runtime.EqualBy(topologies, equalStorageClassTopologies),
			runtime.EqualBy(runtime.InputField(func(r *StorageClassResource) **string {
				return &r.ReclaimPolicy
			}), func(a, b *string) bool { return storageClassPolicy(a) == storageClassPolicy(b) }),
			runtime.EqualBy(runtime.InputField(func(r *StorageClassResource) **bool {
				return &r.AllowVolumeExpansion
			}), func(a, b *bool) bool { return storageClassExpansion(a) == storageClassExpansion(b) }),
			runtime.EqualBy(runtime.InputField(func(r *StorageClassResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalStorageClassPatterns),
			runtime.EqualBy(runtime.InputField(func(r *StorageClassResource) **[]string {
				return &r.IgnoreLabels
			}), equalStorageClassPatterns),
		},
		Replace: runtime.Replacement[
			StorageClassResource, *StorageClassResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[StorageClassResource]{
				runtime.InputField(func(r *StorageClassResource) *string { return &r.Provisioner }),
				parameters, binding, mountOptions, topologies,
			},
			Rules: []runtime.ReplacementRule[StorageClassResource]{
				runtime.ReplaceWhen(metadata, storageClassIdentityChanged),
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
			Drift: storageClassDriftRules(),
		},
	}
}

func storageClassDriftRules() []runtime.DriftRule[*StorageClassResourceOutput] {
	return []runtime.DriftRule[*StorageClassResourceOutput]{
		runtime.ReplaceOnDrift(runtime.OutputField(func(o *StorageClassResourceOutput) *string {
			return &o.Provisioner
		}), storageClassStringChanged),
		runtime.ReplaceOnDrift(runtime.OutputField(func(
			o *StorageClassResourceOutput,
		) *map[string]string {
			return &o.Parameters
		}), func(a, b map[string]string) bool { return !maps.Equal(a, b) }),
		runtime.ReplaceOnDrift(runtime.OutputField(func(o *StorageClassResourceOutput) *string {
			return &o.VolumeBindingMode
		}), storageClassStringChanged),
		runtime.ReplaceOnDrift(runtime.OutputField(func(o *StorageClassResourceOutput) *[]string {
			return &o.MountOptions
		}), func(a, b []string) bool { return !equalStorageClassSet(a, b) }),
		runtime.ReplaceOnDrift(runtime.OutputField(func(
			o *StorageClassResourceOutput,
		) *[]StorageClassTopologyTermOutput {
			return &o.AllowedTopologies
		}), func(a, b []StorageClassTopologyTermOutput) bool {
			return !equalStorageClassTopologyOutput(a, b)
		}),
	}
}

func (r *StorageClassResource) requiresReplacement(
	p storageClassPrior, c *config.Configuration,
) bool {
	before := p.Inputs
	if storageClassIdentityChanged(before.Metadata, r.Metadata) ||
		before.Provisioner != r.Provisioner ||
		!equalStorageClassParameters(before.Parameters, r.Parameters) ||
		!equalStorageClassBinding(before.VolumeBindingMode, r.VolumeBindingMode) ||
		!equalStorageClassSet(ptr.Deref(before.MountOptions, nil), ptr.Deref(r.MountOptions, nil)) ||
		!equalStorageClassTopologies(before.AllowedTopologies, r.AllowedTopologies) {
		return true
	}
	if p.Configuration != nil {
		current := ptr.Deref(c, config.Configuration{})
		if runtime.Changed(p.Configuration.Server, current.Server) ||
			runtime.Changed(p.Configuration.Kubeconfig, current.Kubeconfig) ||
			runtime.Changed(p.Configuration.CurrentContext, current.CurrentContext) {
			return true
		}
	}
	a, b := p.Outputs, p.Observed
	return b != nil && (a.Provisioner != b.Provisioner ||
		!maps.Equal(a.Parameters, b.Parameters) || a.VolumeBindingMode != b.VolumeBindingMode ||
		!equalStorageClassSet(a.MountOptions, b.MountOptions) ||
		!equalStorageClassTopologyOutput(a.AllowedTopologies, b.AllowedTopologies))
}

func equalStorageClassMetadata(a, b StorageClassMetadata) bool {
	return !storageClassIdentityChanged(a, b) &&
		equalStorageClassParameters(a.Labels, b.Labels) &&
		equalStorageClassParameters(a.Annotations, b.Annotations)
}

func storageClassIdentityChanged(a, b StorageClassMetadata) bool {
	return ptr.Deref(a.Name, "") != ptr.Deref(b.Name, "") ||
		ptr.Deref(a.GenerateName, "") != ptr.Deref(b.GenerateName, "")
}

func equalStorageClassParameters(a, b *map[string]string) bool {
	return maps.Equal(ptr.Deref(a, nil), ptr.Deref(b, nil))
}

func equalStorageClassBinding(a, b *string) bool {
	return storageClassBinding(a) == storageClassBinding(b)
}

func equalStorageClassPatterns(a, b *[]string) bool {
	return slices.Equal(ptr.Deref(a, nil), ptr.Deref(b, nil))
}

func equalStorageClassSet(a, b []string) bool {
	return slices.Equal(storageClassSet(a), storageClassSet(b))
}

func equalStorageClassTopologies(a, b *[]StorageClassTopologyTerm) bool {
	return reflect.DeepEqual(storageClassTopologies(a), storageClassTopologies(b))
}

func equalStorageClassTopologyOutput(a, b []StorageClassTopologyTermOutput) bool {
	return slices.EqualFunc(a, b, func(a, b StorageClassTopologyTermOutput) bool {
		return slices.EqualFunc(a.MatchLabelExpressions, b.MatchLabelExpressions,
			func(a, b StorageClassTopologyExpressionOutput) bool {
				return a.Key == b.Key && equalStorageClassSet(a.Values, b.Values)
			})
	})
}

func storageClassStringChanged(a, b string) bool {
	return a != b
}
