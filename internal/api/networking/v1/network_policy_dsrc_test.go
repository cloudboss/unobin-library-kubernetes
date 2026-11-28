package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestNetworkPolicyLookupRead(t *testing.T) {
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		require.NoError(t, json.NewEncoder(w).Encode(&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default",
				DeletionTimestamp: new(metav1.Now())},
		}))
	})
	out, err := (&NetworkPolicyDataSource{Metadata: NetworkPolicyLookupMetadata{Name: "example"}}).
		Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "example", out.Metadata.Name)
	require.NotNil(t, out.Spec)
	require.Equal(t, []string{
		"GET /apis/networking.k8s.io/v1/namespaces/default/networkpolicies/example ",
	}, calls)
}

func TestNetworkPolicyLookupSparseAndMissing(t *testing.T) {
	for _, namespace := range []*string{nil, new("selected")} {
		var calls []string
		c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.Method+" "+r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Reason: metav1.StatusReasonNotFound, Code: 404,
			}))
		})
		c.Namespace = new("elsewhere")
		r := &NetworkPolicyDataSource{Metadata: NetworkPolicyLookupMetadata{
			Name: "example", Namespace: namespace,
		}}
		out, err := r.Read(t.Context(), c)
		require.NoError(t, err)
		selected := "default"
		if namespace != nil {
			selected = *namespace
		}
		require.Equal(t, &NetworkPolicyDataSourceOutput{Metadata: NetworkPolicyLookupMetadataOutput{
			Name: "example", Namespace: selected,
			Labels: map[string]string{}, Annotations: map[string]string{},
		}}, out)
		require.Equal(t, []string{
			"GET /apis/networking.k8s.io/v1/namespaces/" + selected + "/networkpolicies/example",
		}, calls)
	}
	out := networkPolicyLookupOutput(&networkingv1.NetworkPolicy{})
	require.Equal(t, &NetworkPolicyDataSourceOutput{
		Metadata: NetworkPolicyLookupMetadataOutput{
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &NetworkPolicyLookupSpec{
			PodSelector: NetworkPolicyLookupSelector{
				MatchLabels: map[string]string{}, MatchExpressions: []NetworkPolicyLookupExpression{},
			},
			PolicyTypes: []string{},
			Ingress:     []NetworkPolicyLookupIngress{}, Egress: []NetworkPolicyLookupEgress{},
		},
	}, out)
}

func TestNetworkPolicyLookupErrors(t *testing.T) {
	for _, code := range []int{403, 500, 200} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				w.WriteHeader(code)
				if code == 200 {
					_, _ = w.Write([]byte("invalid json"))
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					Code: int32(code), Reason: metav1.StatusReasonForbidden,
				}))
			})
			out, err := (&NetworkPolicyDataSource{Metadata: NetworkPolicyLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get NetworkPolicy default/example")
			if code != 200 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
			}
			require.Equal(t, []string{"GET"}, calls)
		})
	}
	r := &NetworkPolicyDataSource{Metadata: NetworkPolicyLookupMetadata{Name: "example"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Read(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("invalid!")})
	require.ErrorContains(t, err, "get NetworkPolicy default/example")
	var mu sync.Mutex
	var calls []string
	ctx, cancel = context.WithCancel(t.Context())
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.Method)
		mu.Unlock()
		cancel()
		<-req.Context().Done()
	})
	_, err = r.Read(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"GET"}, calls)
}

func TestNetworkPolicyLookupFullProjection(t *testing.T) {
	selector := metav1.LabelSelector{
		MatchLabels: map[string]string{"controller.kubernetes.io/app": "selected"},
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "rack", Operator: "Unknown", Values: []string{"z", "a", "a"}}, {},
		},
	}
	obj := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			ResourceVersion: "12", Generation: 9, DeletionTimestamp: new(metav1.Now()),
			Labels:      map[string]string{"controller.kubernetes.io/app": "selected"},
			Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "internal"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: selector,
			PolicyTypes: []networkingv1.PolicyType{"Unknown", "Egress", "Ingress", "Egress"},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{Ports: []networkingv1.NetworkPolicyPort{
					{}, {Protocol: new(corev1.Protocol("")),
						Port: new(intstr.FromString("")), EndPort: new(int32(0))},
					{Protocol: new(corev1.Protocol("Unknown")), Port: new(intstr.FromInt32(0))},
					{Protocol: new(corev1.ProtocolUDP),
						Port: new(intstr.FromString("dns")), EndPort: new(int32(53))},
				}, From: []networkingv1.NetworkPolicyPeer{
					{}, {PodSelector: &metav1.LabelSelector{}, NamespaceSelector: &selector,
						IPBlock: &networkingv1.IPBlock{CIDR: "2001:DB8::/32",
							Except: []string{"2001:DB8:1::/48", "2001:DB8:0::/48", "2001:DB8:1::/48"}}},
					{NamespaceSelector: &metav1.LabelSelector{}, IPBlock: &networkingv1.IPBlock{}},
				}}, {},
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: "192.0.2.0/24", Except: []string{"192.0.2.2/32"}}},
			}}, {}},
		},
	}
	before := obj.DeepCopy()
	out := networkPolicyLookupOutput(obj)
	emptySelector := NetworkPolicyLookupSelector{
		MatchLabels: map[string]string{}, MatchExpressions: []NetworkPolicyLookupExpression{},
	}
	wantSelector := NetworkPolicyLookupSelector{
		MatchLabels: map[string]string{"controller.kubernetes.io/app": "selected"},
		MatchExpressions: []NetworkPolicyLookupExpression{
			{Key: "rack", Operator: "Unknown", Values: []string{"z", "a", "a"}}, {Values: []string{}},
		},
	}
	require.Equal(t, &NetworkPolicyDataSourceOutput{
		Metadata: NetworkPolicyLookupMetadataOutput{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			ResourceVersion: "12", Generation: 9,
			Labels:      map[string]string{"controller.kubernetes.io/app": "selected"},
			Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "internal"},
		},
		Spec: &NetworkPolicyLookupSpec{
			PodSelector: wantSelector, PolicyTypes: []string{"Unknown", "Egress", "Ingress", "Egress"},
			Ingress: []NetworkPolicyLookupIngress{
				{Ports: []NetworkPolicyPort{
					{}, {Protocol: new(""), Port: new(""), EndPort: new(int64(0))},
					{Protocol: new("Unknown"), Port: new("0")},
					{Protocol: new("UDP"), Port: new("dns"), EndPort: new(int64(53))},
				}, From: []NetworkPolicyLookupPeer{
					{}, {PodSelector: &emptySelector, NamespaceSelector: &wantSelector,
						IPBlock: &NetworkPolicyLookupIPBlock{CIDR: "2001:DB8::/32",
							Except: []string{"2001:DB8:1::/48", "2001:DB8:0::/48", "2001:DB8:1::/48"}}},
					{NamespaceSelector: &emptySelector, IPBlock: &NetworkPolicyLookupIPBlock{Except: []string{}}},
				}}, {Ports: []NetworkPolicyPort{}, From: []NetworkPolicyLookupPeer{}},
			},
			Egress: []NetworkPolicyLookupEgress{{Ports: []NetworkPolicyPort{}, To: []NetworkPolicyLookupPeer{
				{IPBlock: &NetworkPolicyLookupIPBlock{CIDR: "192.0.2.0/24", Except: []string{"192.0.2.2/32"}}},
			}}, {Ports: []NetworkPolicyPort{}, To: []NetworkPolicyLookupPeer{}}},
		},
	}, out)
	require.Equal(t, before, obj)
	out.Metadata.Labels["controller.kubernetes.io/app"] = "changed"
	out.Spec.PodSelector.MatchExpressions[0].Values[0] = "changed"
	peer := out.Spec.Ingress[0].From[1]
	peer.NamespaceSelector.MatchLabels["controller.kubernetes.io/app"] = "changed"
	*out.Spec.Ingress[0].Ports[1].Protocol = "changed"
	*out.Spec.Ingress[0].Ports[1].Port = "changed"
	*out.Spec.Ingress[0].Ports[1].EndPort = 7
	out.Spec.Ingress[0].From[1].IPBlock.Except[0] = "changed"
	require.Equal(t, before, obj)
}

func TestNetworkPolicyLookupCallerDeadline(t *testing.T) {
	c := testAPI(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	out, err := (&NetworkPolicyDataSource{Metadata: NetworkPolicyLookupMetadata{Name: "example"}}).
		Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestNetworkPolicyLookupTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	c := &config.Configuration{Server: new(server.URL)}
	out, err := (&NetworkPolicyDataSource{Metadata: NetworkPolicyLookupMetadata{Name: "example"}}).
		Read(t.Context(), c)
	require.Nil(t, out)
	require.ErrorContains(t, err, "get NetworkPolicy default/example")
	var transport *url.Error
	require.ErrorAs(t, err, &transport)
}

func TestNetworkPolicyLookupMissingAfterSuccess(t *testing.T) {
	present := true
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if present {
			require.NoError(t, json.NewEncoder(w).Encode(&networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "returned", UID: "old", GenerateName: "prefix-",
					Labels: map[string]string{"old": "label"}},
			}))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
			Reason: metav1.StatusReasonNotFound, Code: 404,
		}))
	})
	r := &NetworkPolicyDataSource{Metadata: NetworkPolicyLookupMetadata{Name: "example"}}
	out, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "old", out.Metadata.UID)
	present = false
	out, err = r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &NetworkPolicyDataSourceOutput{Metadata: NetworkPolicyLookupMetadataOutput{
		Name: "example", Namespace: "default",
		Labels: map[string]string{}, Annotations: map[string]string{},
	}}, out)
	get := "GET /apis/networking.k8s.io/v1/namespaces/default/networkpolicies/example"
	require.Equal(t, []string{get, get}, calls)
}
