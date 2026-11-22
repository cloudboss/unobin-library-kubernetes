package v1

import (
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func expandNetworkPolicySpec(s NetworkPolicySpec) networkingv1.NetworkPolicySpec {
	out := networkingv1.NetworkPolicySpec{PodSelector: expandPolicySelector(s.PodSelector)}
	for _, policy := range s.PolicyTypes {
		out.PolicyTypes = append(out.PolicyTypes, networkingv1.PolicyType(policy))
	}
	for _, rule := range value(s.Ingress) {
		out.Ingress = append(out.Ingress, networkingv1.NetworkPolicyIngressRule{
			Ports: expandPolicyPorts(rule.Ports), From: expandPolicyPeers(rule.From),
		})
	}
	for _, rule := range value(s.Egress) {
		out.Egress = append(out.Egress, networkingv1.NetworkPolicyEgressRule{
			Ports: expandPolicyPorts(rule.Ports), To: expandPolicyPeers(rule.To),
		})
	}
	return out
}

func readNetworkPolicySpec(s networkingv1.NetworkPolicySpec) NetworkPolicySpec {
	out := NetworkPolicySpec{PodSelector: readPolicySelector(s.PodSelector), PolicyTypes: []string{}}
	for _, policy := range s.PolicyTypes {
		out.PolicyTypes = append(out.PolicyTypes, string(policy))
	}
	for _, rule := range s.Ingress {
		if out.Ingress == nil {
			out.Ingress = new([]NetworkPolicyIngress{})
		}
		*out.Ingress = append(*out.Ingress, NetworkPolicyIngress{
			Ports: readPolicyPorts(rule.Ports), From: readPolicyPeers(rule.From),
		})
	}
	for _, rule := range s.Egress {
		if out.Egress == nil {
			out.Egress = new([]NetworkPolicyEgress{})
		}
		*out.Egress = append(*out.Egress, NetworkPolicyEgress{
			Ports: readPolicyPorts(rule.Ports), To: readPolicyPeers(rule.To),
		})
	}
	return out
}

func policyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}

func expandPolicySelector(s NetworkPolicySelector) metav1.LabelSelector {
	out := metav1.LabelSelector{}
	if len(value(s.MatchLabels)) > 0 {
		out.MatchLabels = maps.Clone(*s.MatchLabels)
	}
	for _, expr := range value(s.MatchExpressions) {
		out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: value(expr.Key), Operator: metav1.LabelSelectorOperator(value(expr.Operator)),
			Values: policyStrings(value(expr.Values)),
		})
	}
	return out
}

func readPolicySelector(s metav1.LabelSelector) NetworkPolicySelector {
	out := NetworkPolicySelector{}
	if len(s.MatchLabels) > 0 {
		out.MatchLabels = new(maps.Clone(s.MatchLabels))
	}
	for _, expr := range s.MatchExpressions {
		if out.MatchExpressions == nil {
			out.MatchExpressions = new([]NetworkPolicyExpression{})
		}
		r := NetworkPolicyExpression{Key: nonempty(expr.Key), Operator: nonempty(string(expr.Operator))}
		if len(expr.Values) > 0 {
			r.Values = new(policyStrings(expr.Values))
		}
		*out.MatchExpressions = append(*out.MatchExpressions, r)
	}
	return out
}

func expandPolicyPorts(ports *[]NetworkPolicyPort) []networkingv1.NetworkPolicyPort {
	var out []networkingv1.NetworkPolicyPort
	for _, port := range value(ports) {
		p := networkingv1.NetworkPolicyPort{Protocol: new(corev1.ProtocolTCP)}
		if port.Protocol != nil {
			p.Protocol = nil
			if *port.Protocol != "" {
				p.Protocol = new(corev1.Protocol(*port.Protocol))
			}
		}
		if text := value(port.Port); text != "" {
			p.Port = new(intstr.Parse(text))
		}
		if value(port.EndPort) != 0 {
			p.EndPort = new(int32(*port.EndPort))
		}
		out = append(out, p)
	}
	return out
}

func readPolicyPorts(ports []networkingv1.NetworkPolicyPort) *[]NetworkPolicyPort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]NetworkPolicyPort, 0, len(ports))
	for _, port := range ports {
		p := NetworkPolicyPort{Protocol: nonempty(string(value(port.Protocol)))}
		if port.Port != nil {
			p.Port = nonempty(port.Port.String())
		}
		if value(port.EndPort) != 0 {
			p.EndPort = new(int64(*port.EndPort))
		}
		out = append(out, p)
	}
	return &out
}

func expandPolicyPeers(peers *[]NetworkPolicyPeer) []networkingv1.NetworkPolicyPeer {
	var out []networkingv1.NetworkPolicyPeer
	for _, peer := range value(peers) {
		p := networkingv1.NetworkPolicyPeer{}
		if peer.NamespaceSelector != nil {
			p.NamespaceSelector = new(expandPolicySelector(*peer.NamespaceSelector))
		}
		if peer.PodSelector != nil {
			p.PodSelector = new(expandPolicySelector(*peer.PodSelector))
		}
		if peer.IPBlock != nil {
			p.IPBlock = &networkingv1.IPBlock{CIDR: value(peer.IPBlock.CIDR),
				Except: append([]string(nil), value(peer.IPBlock.Except)...)}
		}
		out = append(out, p)
	}
	return out
}

func readPolicyPeers(peers []networkingv1.NetworkPolicyPeer) *[]NetworkPolicyPeer {
	if len(peers) == 0 {
		return nil
	}
	out := make([]NetworkPolicyPeer, 0, len(peers))
	for _, peer := range peers {
		p := NetworkPolicyPeer{}
		if peer.NamespaceSelector != nil {
			p.NamespaceSelector = new(readPolicySelector(*peer.NamespaceSelector))
		}
		if peer.PodSelector != nil {
			p.PodSelector = new(readPolicySelector(*peer.PodSelector))
		}
		if peer.IPBlock != nil {
			p.IPBlock = &NetworkPolicyIPBlock{CIDR: nonempty(peer.IPBlock.CIDR)}
			if len(peer.IPBlock.Except) > 0 {
				p.IPBlock.Except = new(slices.Clone(peer.IPBlock.Except))
			}
		}
		out = append(out, p)
	}
	return &out
}
