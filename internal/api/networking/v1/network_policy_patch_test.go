package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
)

func TestNetworkPolicyPatchClears(t *testing.T) {
	r := networkPolicy()
	before := *networkPolicy()
	before.Spec.Ingress = new([]NetworkPolicyIngress{{Ports: new([]NetworkPolicyPort{
		{Port: new("http")},
	})}})
	before.Spec.Egress = new([]NetworkPolicyEgress{{To: new([]NetworkPolicyPeer{{}})}})
	before.Spec.PodSelector.MatchLabels = &map[string]string{"selected": "yes"}
	before.Metadata.Annotations = &map[string]string{"a/b~c": "old", "remove": "old"}
	r.Metadata.Annotations = &map[string]string{"a/b~c": ""}
	r.Metadata.Labels = &map[string]string{"first": ""}
	out := &NetworkPolicyResourceOutput{UID: "uid", Metadata: before.Metadata, Spec: before.Spec}
	patch := r.policyPatch(out)
	require.Equal(t, networkPolicyPatchOperation{Op: "test", Path: "/metadata/uid", Value: "uid"},
		patch[0])
	body, err := json.Marshal(patch)
	require.NoError(t, err)
	p, err := jsonpatch.DecodePatch(body)
	require.NoError(t, err)
	doc := []byte(`{"metadata":{"uid":"uid","annotations":` +
		`{"a/b~c":"old","remove":"old","external":"keep"},"labels":{"hidden":"lost"}},` +
		`"spec":{"ingress":[{}],"egress":[{}],"podSelector":{"matchLabels":{"selected":"yes"}},` +
		`"policyTypes":["Ingress"]}}`)
	got, err := p.Apply(doc)
	require.NoError(t, err)
	require.JSONEq(t, `{"metadata":{"uid":"uid","annotations":{"a/b~c":"","external":"keep"},`+
		`"labels":{"first":""}},"spec":{"ingress":[],"egress":[],"podSelector":{},`+
		`"policyTypes":["Ingress"]}}`, string(got))
	var paths []string
	for _, op := range patch {
		paths = append(paths, op.Path)
	}
	require.Equal(t, []string{"/metadata/uid", "/metadata/annotations/remove",
		"/metadata/annotations/a~1b~0c", "/metadata/labels", "/spec/ingress", "/spec/egress",
		"/spec/podSelector"}, paths)
}

func TestNetworkPolicyNestedClears(t *testing.T) {
	before := networkPolicy()
	before.Spec.Ingress = new([]NetworkPolicyIngress{{
		Ports: new([]NetworkPolicyPort{{Port: new("80")}}),
		From: new([]NetworkPolicyPeer{{NamespaceSelector: &NetworkPolicySelector{},
			IPBlock: &NetworkPolicyIPBlock{CIDR: new("192.0.2.0/24"),
				Except: new([]string{"192.0.2.1/32"})}}}),
	}})
	for _, tc := range []struct {
		name string
		rule NetworkPolicyIngress
		json string
	}{
		{"ports and peers", NetworkPolicyIngress{}, `[{}]`},
		{"selectors", NetworkPolicyIngress{From: new([]NetworkPolicyPeer{{}})}, `[{"from":[{}]}]`},
		{"exceptions", NetworkPolicyIngress{From: new([]NetworkPolicyPeer{{
			IPBlock: &NetworkPolicyIPBlock{CIDR: new("192.0.2.0/24")},
		}})}, `[{"from":[{"ipBlock":{"cidr":"192.0.2.0/24"}}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := networkPolicy()
			r.Spec.Ingress = new([]NetworkPolicyIngress{tc.rule})
			patch := r.policyPatch(&NetworkPolicyResourceOutput{UID: "uid", Spec: before.Spec})
			body, err := json.Marshal(patch)
			require.NoError(t, err)
			require.JSONEq(t,
				`[{"op":"test","path":"/metadata/uid","value":"uid"},`+
					`{"op":"replace","path":"/spec/ingress","value":`+tc.json+`}]`,
				string(body))
		})
	}
}
