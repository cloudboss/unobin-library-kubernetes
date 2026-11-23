package v1

import (
	"fmt"
	"maps"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (*PriorityClassResource) ResourceDefinition() runtime.ResourceDefinition[
	PriorityClassResource, *PriorityClassResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *PriorityClassResource) *PriorityClassMetadata {
		return &r.Metadata
	})
	policy := runtime.InputField(func(r *PriorityClassResource) **string {
		return &r.PreemptionPolicy
	})
	patternsEqual := func(a, b *[]string) bool {
		return slices.Equal(ptr.Deref(a, nil), ptr.Deref(b, nil))
	}
	return runtime.ResourceDefinition[
		PriorityClassResource, *PriorityClassResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ PriorityClassResource, out *PriorityClassResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Metadata.UID == "" {
				return "", fmt.Errorf("missing PriorityClass server or UID")
			}
			return out.Server + "/" + out.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[PriorityClassResource]{
			runtime.EqualBy(metadata, metadataEqual),
			runtime.EqualBy(policy, func(a, b *string) bool {
				return effectivePolicy(a) == effectivePolicy(b)
			}),
			runtime.EqualBy(runtime.InputField(func(r *PriorityClassResource) **string {
				return &r.Description
			}), func(a, b *string) bool { return ptr.Deref(a, "") == ptr.Deref(b, "") }),
			runtime.EqualBy(runtime.InputField(func(r *PriorityClassResource) **bool {
				return &r.GlobalDefault
			}), func(a, b *bool) bool { return ptr.Deref(a, false) == ptr.Deref(b, false) }),
			runtime.EqualBy(runtime.InputField(func(r *PriorityClassResource) **[]string {
				return &r.IgnoreAnnotations
			}), patternsEqual),
			runtime.EqualBy(runtime.InputField(func(r *PriorityClassResource) **[]string {
				return &r.IgnoreLabels
			}), patternsEqual),
		},
		Replace: runtime.Replacement[
			PriorityClassResource, *PriorityClassResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[PriorityClassResource]{
				runtime.InputField(func(r *PriorityClassResource) *int { return &r.Value }),
				policy,
			},
			Rules: []runtime.ReplacementRule[PriorityClassResource]{
				runtime.ReplaceWhen(metadata, identityChanged),
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
			Drift: []runtime.DriftRule[*PriorityClassResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(func(o *PriorityClassResourceOutput) *int {
					return &o.Value
				}), runtime.Changed[int]),
				runtime.ReplaceOnDrift(runtime.OutputField(func(o *PriorityClassResourceOutput) *string {
					return &o.PreemptionPolicy
				}), runtime.Changed[string]),
			},
		},
	}
}

func metadataEqual(a, b PriorityClassMetadata) bool {
	return !identityChanged(a, b) &&
		maps.Equal(ptr.Deref(a.Annotations, nil), ptr.Deref(b.Annotations, nil)) &&
		maps.Equal(ptr.Deref(a.Labels, nil), ptr.Deref(b.Labels, nil))
}

func identityChanged(a, b PriorityClassMetadata) bool {
	return ptr.Deref(a.Name, "") != ptr.Deref(b.Name, "") ||
		ptr.Deref(a.GenerateName, "") != ptr.Deref(b.GenerateName, "")
}
