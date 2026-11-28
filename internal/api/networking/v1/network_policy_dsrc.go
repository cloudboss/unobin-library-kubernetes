package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type NetworkPolicyDataSource struct {
	Metadata NetworkPolicyLookupMetadata
}

type NetworkPolicyLookupMetadata struct {
	Name      string
	Namespace *string
}

type NetworkPolicyDataSourceOutput struct {
	Metadata NetworkPolicyLookupMetadataOutput
	Spec     *NetworkPolicyLookupSpec
}

type NetworkPolicyLookupMetadataOutput struct {
	Name            string
	Namespace       string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

type NetworkPolicyLookupSpec struct {
	PodSelector NetworkPolicyLookupSelector
	PolicyTypes []string
	Ingress     []NetworkPolicyLookupIngress
	Egress      []NetworkPolicyLookupEgress
}

type NetworkPolicyLookupSelector struct {
	MatchLabels      map[string]string
	MatchExpressions []NetworkPolicyLookupExpression
}

type NetworkPolicyLookupExpression struct {
	Key      string
	Operator string
	Values   []string
}

type NetworkPolicyLookupIngress struct {
	Ports []NetworkPolicyPort
	From  []NetworkPolicyLookupPeer
}

type NetworkPolicyLookupEgress struct {
	Ports []NetworkPolicyPort
	To    []NetworkPolicyLookupPeer
}

type NetworkPolicyLookupPeer struct {
	PodSelector       *NetworkPolicyLookupSelector
	NamespaceSelector *NetworkPolicyLookupSelector
	IPBlock           *NetworkPolicyLookupIPBlock
}

type NetworkPolicyLookupIPBlock struct {
	CIDR   string
	Except []string
}

func (r *NetworkPolicyDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*NetworkPolicyDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace := "default"
	if r.Metadata.Namespace != nil {
		namespace = *r.Metadata.Namespace
	}
	out, err := func() (*NetworkPolicyDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.NetworkingV1().NetworkPolicies(namespace).Get(
			ctx, r.Metadata.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &NetworkPolicyDataSourceOutput{Metadata: NetworkPolicyLookupMetadataOutput{
				Name: r.Metadata.Name, Namespace: namespace,
				Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return networkPolicyLookupOutput(obj), nil
	}()
	if err != nil {
		return nil, fmt.Errorf("get NetworkPolicy %s/%s: %w", namespace, r.Metadata.Name, err)
	}
	return out, nil
}

func (r NetworkPolicyDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r NetworkPolicyDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.Present(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.AtMost(constraint.Length(r.Metadata.Name), 253),
		),
		constraint.When(constraint.Present(r.Metadata.Namespace)).Require(
			constraint.IsMatch(r.Metadata.Namespace, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
			constraint.AtMost(constraint.Length(r.Metadata.Namespace), 63),
		),
	}
}

func networkPolicyLookupOutput(obj *networkingv1.NetworkPolicy) *NetworkPolicyDataSourceOutput {
	out := &NetworkPolicyDataSourceOutput{
		Metadata: NetworkPolicyLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: networkPolicyLookupSpec(obj.Spec),
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	return out
}

func networkPolicyLookupSpec(s networkingv1.NetworkPolicySpec) *NetworkPolicyLookupSpec {
	out := &NetworkPolicyLookupSpec{
		PodSelector: networkPolicyLookupSelector(s.PodSelector),
		PolicyTypes: []string{},
		Ingress:     []NetworkPolicyLookupIngress{}, Egress: []NetworkPolicyLookupEgress{},
	}
	for _, item := range s.PolicyTypes {
		out.PolicyTypes = append(out.PolicyTypes, string(item))
	}
	for _, rule := range s.Ingress {
		out.Ingress = append(out.Ingress, NetworkPolicyLookupIngress{
			Ports: networkPolicyLookupPorts(rule.Ports), From: networkPolicyLookupPeers(rule.From),
		})
	}
	for _, rule := range s.Egress {
		out.Egress = append(out.Egress, NetworkPolicyLookupEgress{
			Ports: networkPolicyLookupPorts(rule.Ports), To: networkPolicyLookupPeers(rule.To),
		})
	}
	return out
}

func networkPolicyLookupSelector(s metav1.LabelSelector) NetworkPolicyLookupSelector {
	out := NetworkPolicyLookupSelector{
		MatchLabels: map[string]string{}, MatchExpressions: []NetworkPolicyLookupExpression{},
	}
	maps.Copy(out.MatchLabels, s.MatchLabels)
	for _, expression := range s.MatchExpressions {
		out.MatchExpressions = append(out.MatchExpressions, NetworkPolicyLookupExpression{
			Key: expression.Key, Operator: string(expression.Operator),
			Values: append([]string{}, expression.Values...),
		})
	}
	return out
}

func networkPolicyLookupPorts(ports []networkingv1.NetworkPolicyPort) []NetworkPolicyPort {
	out := make([]NetworkPolicyPort, 0, len(ports))
	for _, port := range ports {
		item := NetworkPolicyPort{}
		if port.Protocol != nil {
			item.Protocol = new(string(*port.Protocol))
		}
		if port.Port != nil {
			item.Port = new(port.Port.String())
		}
		if port.EndPort != nil {
			item.EndPort = new(int64(*port.EndPort))
		}
		out = append(out, item)
	}
	return out
}

func networkPolicyLookupPeers(peers []networkingv1.NetworkPolicyPeer) []NetworkPolicyLookupPeer {
	out := make([]NetworkPolicyLookupPeer, 0, len(peers))
	for _, peer := range peers {
		item := NetworkPolicyLookupPeer{}
		if peer.PodSelector != nil {
			item.PodSelector = new(networkPolicyLookupSelector(*peer.PodSelector))
		}
		if peer.NamespaceSelector != nil {
			item.NamespaceSelector = new(networkPolicyLookupSelector(*peer.NamespaceSelector))
		}
		if peer.IPBlock != nil {
			item.IPBlock = &NetworkPolicyLookupIPBlock{
				CIDR: peer.IPBlock.CIDR, Except: append([]string{}, peer.IPBlock.Except...),
			}
		}
		out = append(out, item)
	}
	return out
}
