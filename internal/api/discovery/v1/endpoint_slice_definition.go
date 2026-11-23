package v1

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func (*EndpointSliceResource) ResourceDefinition() runtime.ResourceDefinition[
	EndpointSliceResource, *EndpointSliceResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		EndpointSliceResource, *EndpointSliceResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ EndpointSliceResource, out *EndpointSliceResourceOutput) (string, error) {
			if err := requireEndpointSliceIdentity(out); err != nil {
				return "", err
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[EndpointSliceResource]{
			runtime.EqualBy(runtime.InputField(
				func(r *EndpointSliceResource) *EndpointSliceMetadata { return &r.Metadata },
			), equalEndpointSliceMetadata),
			runtime.EqualBy(runtime.InputField(
				func(r *EndpointSliceResource) *[]EndpointSliceEndpoint { return &r.Endpoints },
			), equalEndpointSliceEndpoints),
			runtime.EqualBy(runtime.InputField(
				func(r *EndpointSliceResource) *[]EndpointSlicePort { return &r.Ports },
			), equalEndpointSlicePorts),
		},
		Replace: runtime.Replacement[
			EndpointSliceResource, *EndpointSliceResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[EndpointSliceResource]{
				runtime.InputField(func(r *EndpointSliceResource) *string { return &r.AddressType }),
			},
			Rules: []runtime.ReplacementRule[EndpointSliceResource]{
				runtime.ReplaceWhen(runtime.InputField(
					func(r *EndpointSliceResource) *EndpointSliceMetadata { return &r.Metadata },
				), replaceEndpointSliceMetadata),
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

func replaceEndpointSliceMetadata(a, b EndpointSliceMetadata) bool {
	return value(a.Name) != value(b.Name) || value(a.GenerateName) != value(b.GenerateName) ||
		defaultNamespace(a.Namespace) != defaultNamespace(b.Namespace)
}

func equalEndpointSliceMetadata(a, b EndpointSliceMetadata) bool {
	return !replaceEndpointSliceMetadata(a, b) && maps.Equal(value(a.Labels), value(b.Labels)) &&
		maps.Equal(value(a.Annotations), value(b.Annotations))
}

func equalEndpointSliceEndpoints(a, b []EndpointSliceEndpoint) bool {
	return reflect.DeepEqual(expandEndpoints(a), expandEndpoints(b))
}

func equalEndpointSlicePorts(a, b []EndpointSlicePort) bool {
	return slices.EqualFunc(a, b, func(a, b EndpointSlicePort) bool {
		return strings.TrimLeft(a.Port, "+-0") == strings.TrimLeft(b.Port, "+-0") &&
			value(a.Name) == value(b.Name) && a.AppProtocol == b.AppProtocol &&
			effectiveProtocol(a.Protocol) == effectiveProtocol(b.Protocol)
	})
}

func effectiveProtocol(protocol *string) string {
	if protocol == nil {
		return "TCP"
	}
	return *protocol
}

func requireEndpointSliceIdentity(out *EndpointSliceResourceOutput) error {
	if out == nil || out.Server == "" || out.APIVersion != "discovery.k8s.io/v1" ||
		out.Kind != "EndpointSlice" || out.Name == "" || out.Namespace == "" || out.UID == "" {
		return fmt.Errorf("missing endpoint slice identity in prior state")
	}
	return nil
}

func (out *EndpointSliceResourceOutput) target() object.Target {
	return object.Target{Server: out.Server, APIVersion: out.APIVersion, Kind: out.Kind,
		Name: out.Name, Namespace: out.Namespace, UID: out.UID}
}
