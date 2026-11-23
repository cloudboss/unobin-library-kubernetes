package v1

import (
	"fmt"
	"maps"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *RuntimeClassResource) ResourceDefinition() runtime.ResourceDefinition[
	RuntimeClassResource, *RuntimeClassResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *RuntimeClassResource) *RuntimeClassMetadata {
		return &r.Metadata
	})
	handler := runtime.InputField(func(r *RuntimeClassResource) *string {
		return &r.Handler
	})
	return runtime.ResourceDefinition[
		RuntimeClassResource, *RuntimeClassResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ RuntimeClassResource, out *RuntimeClassResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Metadata.UID == "" {
				return "", fmt.Errorf("missing RuntimeClass server or UID")
			}
			return out.Server + "/" + out.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[RuntimeClassResource]{
			runtime.EqualBy(metadata, equalRuntimeClassMetadata),
			runtime.EqualBy(runtime.InputField(func(r *RuntimeClassResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalRuntimeClassPatterns),
			runtime.EqualBy(runtime.InputField(func(r *RuntimeClassResource) **[]string {
				return &r.IgnoreLabels
			}), equalRuntimeClassPatterns),
		},
		Replace: runtime.Replacement[
			RuntimeClassResource, *RuntimeClassResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[RuntimeClassResource]{handler},
			Rules: []runtime.ReplacementRule[RuntimeClassResource]{
				runtime.ReplaceWhen(metadata, runtimeClassIdentityChanged),
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
			Drift: []runtime.DriftRule[*RuntimeClassResourceOutput]{
				runtime.ReplaceOnDrift(runtime.OutputField(func(o *RuntimeClassResourceOutput) *string {
					return &o.Handler
				}), func(a, b string) bool { return a != b }),
			},
		},
	}
}

func (r *RuntimeClassResource) requiresReplacement(
	p runtimeClassPrior, c *config.Configuration,
) bool {
	if runtimeClassIdentityChanged(p.Inputs.Metadata, r.Metadata) || p.Inputs.Handler != r.Handler {
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
	return p.Observed != nil && p.Outputs.Handler != p.Observed.Handler
}

func runtimeClassIdentityChanged(a, b RuntimeClassMetadata) bool {
	return ptr.Deref(a.Name, "") != ptr.Deref(b.Name, "") ||
		ptr.Deref(a.GenerateName, "") != ptr.Deref(b.GenerateName, "")
}

func equalRuntimeClassMetadata(a, b RuntimeClassMetadata) bool {
	return !runtimeClassIdentityChanged(a, b) &&
		maps.Equal(ptr.Deref(a.Labels, nil), ptr.Deref(b.Labels, nil)) &&
		maps.Equal(ptr.Deref(a.Annotations, nil), ptr.Deref(b.Annotations, nil))
}

func equalRuntimeClassPatterns(a, b *[]string) bool {
	return slices.Equal(runtimeClassPatternSet(a), runtimeClassPatternSet(b))
}

func runtimeClassPatternSet(patterns *[]string) []string {
	result := slices.Clone(ptr.Deref(patterns, nil))
	slices.Sort(result)
	return slices.Compact(result)
}
