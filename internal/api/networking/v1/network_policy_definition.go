package v1

import (
	"fmt"
	"maps"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func (r *NetworkPolicyResource) ResourceDefinition() runtime.ResourceDefinition[
	NetworkPolicyResource, *NetworkPolicyResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		NetworkPolicyResource, *NetworkPolicyResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ NetworkPolicyResource, out *NetworkPolicyResourceOutput) (string, error) {
			if err := requireNetworkPolicyIdentity(out); err != nil {
				return "", err
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[NetworkPolicyResource]{
			runtime.EqualBy(runtime.InputField(func(r *NetworkPolicyResource) *NetworkPolicyMetadata {
				return &r.Metadata
			}), equalPolicyMetadata),
			runtime.EqualBy(runtime.InputField(func(r *NetworkPolicyResource) *NetworkPolicySpec {
				return &r.Spec
			}), equalPolicySpec),
		},
		Replace: runtime.Replacement[
			NetworkPolicyResource, *NetworkPolicyResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[NetworkPolicyResource]{
				runtime.ReplaceWhen(runtime.InputField(
					func(r *NetworkPolicyResource) *NetworkPolicyMetadata { return &r.Metadata },
				), replacePolicyMetadata),
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

func policyNamespace(m NetworkPolicyMetadata) string {
	if m.Namespace == nil {
		return "default"
	}
	return *m.Namespace
}

func replacePolicyMetadata(a, b NetworkPolicyMetadata) bool {
	return value(a.Name) != value(b.Name) || value(a.GenerateName) != value(b.GenerateName) ||
		policyNamespace(a) != policyNamespace(b)
}

func equalPolicyMetadata(a, b NetworkPolicyMetadata) bool {
	return !replacePolicyMetadata(a, b) && maps.Equal(value(a.Labels), value(b.Labels)) &&
		maps.Equal(value(a.Annotations), value(b.Annotations))
}

func equalPolicySpec(a, b NetworkPolicySpec) bool {
	return reflect.DeepEqual(policyComparisonSpec(a), policyComparisonSpec(b))
}

func policyComparisonSpec(s NetworkPolicySpec) networkingv1.NetworkPolicySpec {
	out := expandNetworkPolicySpec(s)
	for i := range out.Ingress {
		for j := range out.Ingress[i].Ports {
			if out.Ingress[i].Ports[j].Protocol == nil {
				out.Ingress[i].Ports[j].Protocol = new(corev1.ProtocolTCP)
			}
		}
	}
	for i := range out.Egress {
		for j := range out.Egress[i].Ports {
			if out.Egress[i].Ports[j].Protocol == nil {
				out.Egress[i].Ports[j].Protocol = new(corev1.ProtocolTCP)
			}
		}
	}
	return out
}

func requireNetworkPolicyIdentity(out *NetworkPolicyResourceOutput) error {
	if out == nil || out.Server == "" || out.APIVersion != "networking.k8s.io/v1" ||
		out.Kind != "NetworkPolicy" || out.Name == "" || out.Namespace == "" || out.UID == "" {
		return fmt.Errorf("missing network policy identity in prior state")
	}
	return nil
}

func (out *NetworkPolicyResourceOutput) target() object.Target {
	return object.Target{Server: out.Server, APIVersion: out.APIVersion, Kind: out.Kind,
		Name: out.Name, Namespace: out.Namespace, UID: out.UID}
}
