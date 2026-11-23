package v1

import (
	"fmt"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *CSIDriverResource) ResourceDefinition() runtime.ResourceDefinition[
	CSIDriverResource, *CSIDriverResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *CSIDriverResource) *CSIDriverMetadata {
		return &r.Metadata
	})
	spec := runtime.InputField(func(r *CSIDriverResource) **CSIDriverSpec { return &r.Spec })
	observedSpec := runtime.OutputField(func(o *CSIDriverResourceOutput) *CSIDriverSpecOutput {
		return &o.Spec
	})
	return runtime.ResourceDefinition[
		CSIDriverResource, *CSIDriverResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ CSIDriverResource, out *CSIDriverResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Metadata.UID == "" {
				return "", fmt.Errorf("missing CSIDriver server or UID")
			}
			return out.Server + "/" + out.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[CSIDriverResource]{
			runtime.EqualBy(metadata, func(a, b CSIDriverMetadata) bool {
				return equalStorageClassMetadata(StorageClassMetadata(a), StorageClassMetadata(b))
			}),
			runtime.EqualBy(spec, equalCSIDriverSpec),
			runtime.EqualBy(runtime.InputField(func(r *CSIDriverResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalStorageClassPatterns),
			runtime.EqualBy(runtime.InputField(func(r *CSIDriverResource) **[]string {
				return &r.IgnoreLabels
			}), equalStorageClassPatterns),
		},
		Replace: runtime.Replacement[
			CSIDriverResource, *CSIDriverResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[CSIDriverResource]{
				runtime.ReplaceWhen(metadata, csiDriverIdentityChanged),
				runtime.ReplaceWhen(spec, csiDriverAttachChanged),
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
			Drift: []runtime.DriftRule[*CSIDriverResourceOutput]{
				runtime.ReplaceOnDrift(observedSpec, csiDriverAttachDrift),
			},
		},
	}
}

func equalCSIDriverSpec(a, b *CSIDriverSpec) bool {
	return (a == nil) == (b == nil) && !csiDriverAttachChanged(a, b) &&
		csiDriverPodInfo(a) == csiDriverPodInfo(b) && slices.Equal(csiDriverModes(a), csiDriverModes(b))
}

func csiDriverIdentityChanged(a, b CSIDriverMetadata) bool {
	return storageClassIdentityChanged(StorageClassMetadata(a), StorageClassMetadata(b))
}

func csiDriverAttachChanged(a, b *CSIDriverSpec) bool {
	return csiDriverAttach(a) != csiDriverAttach(b)
}

func csiDriverAttachDrift(a, b CSIDriverSpecOutput) bool {
	return runtime.Changed(a.AttachRequired, b.AttachRequired)
}

func (r *CSIDriverResource) requiresReplacement(p csiDriverPrior, c *config.Configuration) bool {
	if csiDriverIdentityChanged(p.Inputs.Metadata, r.Metadata) ||
		csiDriverAttachChanged(p.Inputs.Spec, r.Spec) {
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
	return p.Observed != nil && csiDriverAttachDrift(p.Outputs.Spec, p.Observed.Spec)
}
