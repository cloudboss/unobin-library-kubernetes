package v1

import (
	"fmt"
	"maps"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func (*EndpointsResource) ResourceDefinition() runtime.ResourceDefinition[
	EndpointsResource, *EndpointsResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *EndpointsResource) *EndpointsMetadata {
		return &r.Metadata
	})
	return runtime.ResourceDefinition[
		EndpointsResource, *EndpointsResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ EndpointsResource, out *EndpointsResourceOutput) (string, error) {
			if err := requireEndpointsIdentity(out); err != nil {
				return "", err
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[EndpointsResource]{
			runtime.EqualBy(metadata, equalEndpointsMetadata),
			runtime.EqualBy(runtime.InputField(func(r *EndpointsResource) **[]EndpointsSubset {
				return &r.Subset
			}), func(a, b *[]EndpointsSubset) bool {
				return slices.EqualFunc(
					desiredEndpointsSubsets(a), desiredEndpointsSubsets(b), equalEndpointsSubset,
				)
			}),
			runtime.EqualBy(runtime.InputField(func(r *EndpointsResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *EndpointsResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		Replace: runtime.Replacement[
			EndpointsResource, *EndpointsResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[EndpointsResource]{
				runtime.ReplaceWhen(metadata, replaceEndpointsMetadata),
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

func endpointsNamespace(namespace *string) string {
	return ptr.Deref(namespace, "default")
}

func equalEndpointsMetadata(a, b EndpointsMetadata) bool {
	return !replaceEndpointsMetadata(a, b) &&
		maps.Equal(endpointsMetadataValues(a.Annotations), endpointsMetadataValues(b.Annotations)) &&
		maps.Equal(endpointsMetadataValues(a.Labels), endpointsMetadataValues(b.Labels))
}

func replaceEndpointsMetadata(a, b EndpointsMetadata) bool {
	return ptr.Deref(a.Name, "") != ptr.Deref(b.Name, "") ||
		ptr.Deref(a.GenerateName, "") != ptr.Deref(b.GenerateName, "") ||
		endpointsNamespace(a.Namespace) != endpointsNamespace(b.Namespace)
}

func requireEndpointsIdentity(out *EndpointsResourceOutput) error {
	if out == nil || out.Server == "" || out.APIVersion != "v1" || out.Kind != "Endpoints" ||
		out.Name == "" || out.Namespace == "" || out.UID == "" {
		return fmt.Errorf("missing Endpoints identity in prior state")
	}
	return nil
}

func (out *EndpointsResourceOutput) target() object.Target {
	return object.Target{Server: out.Server, APIVersion: out.APIVersion, Kind: out.Kind,
		Name: out.Name, Namespace: out.Namespace, UID: out.UID}
}
