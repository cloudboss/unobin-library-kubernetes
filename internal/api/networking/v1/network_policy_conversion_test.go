package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func networkPolicy() *NetworkPolicyResource {
	return &NetworkPolicyResource{Metadata: NetworkPolicyMetadata{Name: new("example")},
		Spec: NetworkPolicySpec{PolicyTypes: []string{"Ingress"}}}
}

func TestNetworkPolicyConversions(t *testing.T) {
	r := networkPolicy()
	encoded, err := json.Marshal(expandNetworkPolicySpec(r.Spec))
	require.NoError(t, err)
	require.JSONEq(t, `{"podSelector":{},"policyTypes":["Ingress"]}`, string(encoded))
	r.Spec.Ingress = new([]NetworkPolicyIngress{{}})
	encoded, err = json.Marshal(expandNetworkPolicySpec(r.Spec))
	require.NoError(t, err)
	require.JSONEq(t, `{"podSelector":{},"policyTypes":["Ingress"],"ingress":[{}]}`,
		string(encoded))
	r.Spec.Ingress = new([]NetworkPolicyIngress{{From: new([]NetworkPolicyPeer{
		{}, {NamespaceSelector: &NetworkPolicySelector{}},
		{NamespaceSelector: &NetworkPolicySelector{}, PodSelector: &NetworkPolicySelector{},
			IPBlock: &NetworkPolicyIPBlock{}},
	})}})
	s := expandNetworkPolicySpec(r.Spec)
	require.Nil(t, s.Ingress[0].From[0].NamespaceSelector)
	require.NotNil(t, s.Ingress[0].From[1].NamespaceSelector)
	require.NotNil(t, s.Ingress[0].From[2].PodSelector)
	require.NotNil(t, s.Ingress[0].From[2].IPBlock)
	require.Empty(t, s.Ingress[0].From[2].IPBlock.CIDR)
	require.Equal(t, r.Spec, readNetworkPolicySpec(s))
	for _, tc := range []struct {
		text string
		kind intstr.Type
		num  int32
	}{
		{"0080", intstr.Int, 80}, {"+80", intstr.Int, 80}, {"-1", intstr.Int, -1},
		{"0", intstr.Int, 0}, {"http", intstr.String, 0}, {"2147483648", intstr.String, 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			r.Spec.Ingress = new([]NetworkPolicyIngress{{Ports: new([]NetworkPolicyPort{
				{Port: &tc.text},
			})}})
			s := expandNetworkPolicySpec(r.Spec)
			p := s.Ingress[0].Ports[0]
			require.Equal(t, tc.kind, p.Port.Type)
			require.Equal(t, tc.num, p.Port.IntVal)
			require.Equal(t, corev1.ProtocolTCP, *p.Protocol)
			if tc.kind == intstr.String {
				require.Equal(t, tc.text, p.Port.StrVal)
			}
		})
	}
	for _, end := range []int64{0, 90, 4294967297, 4294967296} {
		r.Spec.Egress = new([]NetworkPolicyEgress{{Ports: new([]NetworkPolicyPort{
			{EndPort: &end, Port: new(""), Protocol: new("")},
		})}})
		p := expandNetworkPolicySpec(r.Spec).Egress[0].Ports[0]
		require.Nil(t, p.Port)
		require.Nil(t, p.Protocol)
		if end == 0 {
			require.Nil(t, p.EndPort)
		} else {
			require.Equal(t, int32(end), *p.EndPort)
		}
	}
}

func TestNetworkPolicyNormalizationAndOrdering(t *testing.T) {
	a := networkPolicy().Spec
	b := a
	a.PodSelector.MatchExpressions = new([]NetworkPolicyExpression{
		{Key: new("role"), Operator: new("In"), Values: new([]string{"b", "a", "b"})}, {},
	})
	b.PodSelector.MatchExpressions = new([]NetworkPolicyExpression{
		{Key: new("role"), Operator: new("In"), Values: new([]string{"a", "b"})}, {},
	})
	require.True(t, equalPolicySpec(a, b))
	require.Equal(t, []string{"a", "b"},
		expandNetworkPolicySpec(a).PodSelector.MatchExpressions[0].Values)
	flat := readNetworkPolicySpec(expandNetworkPolicySpec(a))
	require.Equal(t, &[]string{"a", "b"}, (*flat.PodSelector.MatchExpressions)[0].Values)
	*b.PodSelector.MatchExpressions = []NetworkPolicyExpression{
		{}, (*b.PodSelector.MatchExpressions)[0],
	}
	require.False(t, equalPolicySpec(a, b))
	a, b = networkPolicy().Spec, networkPolicy().Spec
	b.Ingress = new([]NetworkPolicyIngress{})
	b.PodSelector.MatchLabels = new(map[string]string{})
	require.True(t, equalPolicySpec(a, b))
	b.Ingress = new([]NetworkPolicyIngress{{}})
	require.False(t, equalPolicySpec(a, b))
	a.Ingress = new([]NetworkPolicyIngress{{Ports: new([]NetworkPolicyPort{
		{Port: new("0080")},
	})}})
	b.Ingress = new([]NetworkPolicyIngress{{Ports: new([]NetworkPolicyPort{
		{Port: new("80"), Protocol: new("")},
	})}})
	require.True(t, equalPolicySpec(a, b))
	(*b.Ingress)[0].From = new([]NetworkPolicyPeer{{NamespaceSelector: &NetworkPolicySelector{}}})
	require.False(t, equalPolicySpec(a, b))
	for _, protocol := range []string{"UDP", "SCTP", "arbitrary"} {
		a.Egress = new([]NetworkPolicyEgress{{Ports: new([]NetworkPolicyPort{{Protocol: &protocol}})}})
		require.Equal(t, protocol, string(*expandNetworkPolicySpec(a).Egress[0].Ports[0].Protocol))
	}
	a.PolicyTypes = []string{"unknown", "unknown"}
	a.Egress = new([]NetworkPolicyEgress{{To: new([]NetworkPolicyPeer{{
		IPBlock: &NetworkPolicyIPBlock{CIDR: new("2001:0db8::/64"),
			Except: new([]string{"2001:db8::1/128", "2001:db8::/96", "2001:db8::1/128"})},
	}})}})
	got := readNetworkPolicySpec(expandNetworkPolicySpec(a))
	require.True(t, equalPolicySpec(a, got))
	require.Equal(t, *a.Egress, *got.Egress)
}

func TestNetworkPolicyOrderedLists(t *testing.T) {
	expr := NetworkPolicyExpression{Key: new("app"), Operator: new("In"),
		Values: new([]string{"b", "a", "b"})}
	peer := NetworkPolicyPeer{IPBlock: &NetworkPolicyIPBlock{CIDR: new("192.0.2.0/24"),
		Except: new([]string{"192.0.2.1/32", "192.0.2.2/32", "192.0.2.1/32"})}}
	ports := []NetworkPolicyPort{{Port: new("80")}, {Port: new("http")}, {Port: new("80")}}
	peers := []NetworkPolicyPeer{peer, {}, peer}
	s := NetworkPolicySpec{PolicyTypes: []string{"Ingress", "Egress"},
		PodSelector: NetworkPolicySelector{
			MatchExpressions: new([]NetworkPolicyExpression{expr, {}, expr}),
		},
		Ingress: new([]NetworkPolicyIngress{{Ports: &ports, From: &peers}, {},
			{Ports: &ports, From: &peers}}),
		Egress: new([]NetworkPolicyEgress{{Ports: &ports, To: &peers}, {},
			{Ports: &ports, To: &peers}}),
	}
	for _, change := range []struct {
		name string
		edit func(*NetworkPolicySpec)
	}{
		{"ingress", func(s *NetworkPolicySpec) {
			(*s.Ingress)[0], (*s.Ingress)[1] = (*s.Ingress)[1], (*s.Ingress)[0]
		}},
		{"egress", func(s *NetworkPolicySpec) {
			(*s.Egress)[0], (*s.Egress)[1] = (*s.Egress)[1], (*s.Egress)[0]
		}},
		{"ports", func(s *NetworkPolicySpec) {
			p := (*s.Ingress)[0].Ports
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
		}},
		{"peers", func(s *NetworkPolicySpec) {
			p := (*s.Ingress)[0].From
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
		}},
		{"exceptions", func(s *NetworkPolicySpec) {
			p := (*(*s.Ingress)[0].From)[0].IPBlock.Except
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
		}},
		{"expressions", func(s *NetworkPolicySpec) {
			p := s.PodSelector.MatchExpressions
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
		}},
		{"policy types", func(s *NetworkPolicySpec) {
			s.PolicyTypes[0], s.PolicyTypes[1] = s.PolicyTypes[1], s.PolicyTypes[0]
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			got := readNetworkPolicySpec(expandNetworkPolicySpec(s))
			require.True(t, equalPolicySpec(s, got))
			change.edit(&got)
			require.False(t, equalPolicySpec(s, got))
		})
	}
}
