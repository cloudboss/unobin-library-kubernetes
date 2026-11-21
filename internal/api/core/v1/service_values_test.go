package v1

import (
	"encoding/json"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestServiceDefaultsAndExpansion(t *testing.T) {
	r := &ServiceResource{}
	require.NoError(t, r.validate())
	obj := r.expand(nil)
	require.Equal(t, "default", obj.Namespace)
	require.Equal(t, corev1.ServiceTypeClusterIP, obj.Spec.Type)
	require.Equal(t, corev1.ServiceAffinityNone, obj.Spec.SessionAffinity)
	require.Nil(t, obj.Spec.AllocateLoadBalancerNodePorts)
	require.Nil(t, obj.Spec.LoadBalancerClass)
	require.Nil(t, obj.Spec.InternalTrafficPolicy)
	require.Nil(t, obj.Spec.IPFamilyPolicy)
	require.Empty(t, obj.Spec.IPFamilies)
	require.Empty(t, obj.Spec.ClusterIPs)
	require.Empty(t, obj.Spec.Ports)
	require.Nil(t, obj.Spec.SessionAffinityConfig)
	require.True(t, equalServiceSpec(ServiceSpec{}, ServiceSpec{
		Type: new("ClusterIP"), AllocateLoadBalancerNodePorts: new(true),
		PublishNotReadyAddresses: new(false), SessionAffinity: new("None"),
		Port: &[]ServicePort{}, ExternalIps: &[]string{}, Selector: &map[string]string{},
	}))
	require.True(t, equalServiceSpec(ServiceSpec{}, ServiceSpec{LoadBalancerClass: new("")}))
	r.Spec = ServiceSpec{Type: new("LoadBalancer"),
		AllocateLoadBalancerNodePorts: new(false), LoadBalancerClass: new("API authority /"),
		Port: &[]ServicePort{{Port: 80, TargetPort: new("08080"), AppProtocol: new("")},
			{Port: 81, TargetPort: new("http"), NodePort: new(int64(0))}},
		SessionAffinityConfig: &ServiceAffinityConfig{ClientIP: &ServiceClientIP{}},
	}
	require.NoError(t, r.validate())
	obj = r.expand(nil)
	require.Equal(t, new(false), obj.Spec.AllocateLoadBalancerNodePorts)
	require.Equal(t, r.Spec.LoadBalancerClass, obj.Spec.LoadBalancerClass)
	require.Equal(t, intstr.FromInt32(8080), obj.Spec.Ports[0].TargetPort)
	require.Equal(t, intstr.FromString("http"), obj.Spec.Ports[1].TargetPort)
	require.Equal(t, corev1.ProtocolTCP, obj.Spec.Ports[0].Protocol)
	require.Nil(t, obj.Spec.Ports[0].AppProtocol)
	require.Equal(t, new(int32(0)), obj.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds)
	data, err := json.Marshal(obj)
	require.NoError(t, err)
	require.Contains(t, string(data), `"targetPort":8080`)
	require.NotContains(t, string(data), "appProtocol")
}

func TestServiceValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		r    ServiceResource
	}{
		{"name", ServiceResource{Metadata: ServiceMetadata{Name: new("BAD")}}},
		{"prefix", ServiceResource{Metadata: ServiceMetadata{GenerateName: new("bad.prefix-")}}},
		{"conflict", ServiceResource{Metadata: ServiceMetadata{
			Name: new("name"), GenerateName: new("prefix-")}}},
		{"label key", ServiceResource{Metadata: ServiceMetadata{
			Labels: &map[string]string{"bad key": "v"}}}},
		{"label value", ServiceResource{Metadata: ServiceMetadata{
			Labels: &map[string]string{"key": "bad value"}}}},
		{"annotation", ServiceResource{Metadata: ServiceMetadata{
			Annotations: &map[string]string{"bad key": "v"}}}},
		{"regex", ServiceResource{IgnoreLabels: &[]string{"["}}},
		{"type", ServiceResource{Spec: ServiceSpec{Type: new("clusterip")}}},
		{"cluster address", ServiceResource{Spec: ServiceSpec{ClusterIP: new("none")}}},
		{"cluster addresses", ServiceResource{Spec: ServiceSpec{
			ClusterIps: &[]string{"1.2.3.4", "::1", "::2"}}}},
		{"cluster member", ServiceResource{Spec: ServiceSpec{ClusterIps: &[]string{"bad"}}}},
		{"external address", ServiceResource{Spec: ServiceSpec{ExternalIps: &[]string{"bad"}}}},
		{"lb address", ServiceResource{Spec: ServiceSpec{LoadBalancerIP: new("bad")}}},
		{"cidr", ServiceResource{Spec: ServiceSpec{LoadBalancerSourceRanges: &[]string{"1.2.3.4"}}}},
		{"families", ServiceResource{Spec: ServiceSpec{IPFamilies: &[]string{"IPv4", "IPv6", "IPv4"}}}},
		{"family", ServiceResource{Spec: ServiceSpec{IPFamilies: &[]string{"ipv4"}}}},
		{"family policy", ServiceResource{Spec: ServiceSpec{IPFamilyPolicy: new("bad")}}},
		{"external policy", ServiceResource{Spec: ServiceSpec{ExternalTrafficPolicy: new("bad")}}},
		{"internal policy", ServiceResource{Spec: ServiceSpec{InternalTrafficPolicy: new("bad")}}},
		{"affinity", ServiceResource{Spec: ServiceSpec{SessionAffinity: new("bad")}}},
	} {
		t.Run(tt.name, func(t *testing.T) { require.Error(t, tt.r.validate()) })
	}
	for _, port := range []int64{-1, 0, 65536} {
		r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: port}}}}
		require.Error(t, r.validate())
	}
	for _, node := range []int64{-1, 65536} {
		r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 1, NodePort: &node}}}}
		require.Error(t, r.validate())
	}
	r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80, Protocol: new("tcp")}}}}
	require.Error(t, r.validate())
	for _, port := range []int64{0, 65536} {
		r.Spec = ServiceSpec{HealthCheckNodePort: &port}
		require.Error(t, r.validate())
	}
	for _, timeout := range []int64{0, 86401} {
		r.Spec = ServiceSpec{SessionAffinityConfig: &ServiceAffinityConfig{
			ClientIP: &ServiceClientIP{TimeoutSeconds: &timeout}}}
		require.Error(t, r.validate())
	}
	for _, timeout := range []int64{1, 86400} {
		r.Spec = ServiceSpec{SessionAffinityConfig: &ServiceAffinityConfig{
			ClientIP: &ServiceClientIP{TimeoutSeconds: &timeout}}}
		require.NoError(t, r.validate())
	}
	for _, timeout := range []string{"bad", "0s", "-1s"} {
		r = &ServiceResource{CreateTimeout: &timeout}
		require.Error(t, r.validate())
	}
	r = &ServiceResource{Metadata: ServiceMetadata{Name: new("dotted.name"),
		Annotations: &map[string]string{"EXAMPLE.COM/Key": ""}}, Spec: ServiceSpec{
		ClusterIP: new("None"), ClusterIps: &[]string{"None"}, IPFamilies: &[]string{},
		LoadBalancerSourceRanges: &[]string{"192.0.2.3/24"}, HealthCheckNodePort: new(int64(1)),
		Port: &[]ServicePort{{Port: 65535, Protocol: new("SCTP"), NodePort: new(int64(65535)),
			Name: new("BAD /"), AppProtocol: new("BAD /"), TargetPort: new("BAD /")}},
		ExternalName: new("BAD /"), Selector: &map[string]string{"BAD /": "BAD /"},
	}}
	require.NoError(t, r.validate())
	require.Equal(t, r.Metadata.Annotations, &r.expand(nil).Annotations)
}

func TestServiceComputedValuesAndSets(t *testing.T) {
	omitted := ServiceSpec{Port: &[]ServicePort{{Port: 80}}}
	for _, configured := range []ServiceSpec{
		{Port: &[]ServicePort{{Port: 80, NodePort: new(int64(0))}}},
		{Port: &[]ServicePort{{Port: 80, TargetPort: new("0")}}},
	} {
		require.False(t, equalServiceSpec(omitted, configured))
	}
	previous := &corev1.ServiceSpec{ClusterIP: "10.0.0.1", ClusterIPs: []string{"10.0.0.1", "::1"},
		IPFamilies:            []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol},
		IPFamilyPolicy:        new(corev1.IPFamilyPolicyPreferDualStack),
		InternalTrafficPolicy: new(corev1.ServiceInternalTrafficPolicyCluster),
		ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
		HealthCheckNodePort:   30001, LoadBalancerClass: new("class"),
		SessionAffinityConfig: &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{
			TimeoutSeconds: new(int32(10800))}},
		Ports: []corev1.ServicePort{{Port: 80, NodePort: 31001, TargetPort: intstr.FromInt32(8080)}},
	}
	r := &ServiceResource{Spec: ServiceSpec{Type: new("LoadBalancer"),
		Port: &[]ServicePort{{Port: 80, Name: new("changed")}, {Port: 81}}}}
	result := r.expand(previous).Spec
	require.Equal(t, previous.ClusterIPs, result.ClusterIPs)
	require.Equal(t, previous.IPFamilies, result.IPFamilies)
	require.Equal(t, previous.IPFamilyPolicy, result.IPFamilyPolicy)
	require.Equal(t, previous.InternalTrafficPolicy, result.InternalTrafficPolicy)
	require.Equal(t, previous.ExternalTrafficPolicy, result.ExternalTrafficPolicy)
	require.Equal(t, previous.SessionAffinityConfig, result.SessionAffinityConfig)
	require.Equal(t, int32(30001), result.HealthCheckNodePort)
	require.Nil(t, result.LoadBalancerClass)
	require.Equal(t, int32(31001), result.Ports[0].NodePort)
	require.Equal(t, intstr.FromInt32(8080), result.Ports[0].TargetPort)
	require.Equal(t, int32(0), result.Ports[1].NodePort)
	(*r.Spec.Port)[0].NodePort = new(int64(0))
	require.Zero(t, r.expand(previous).Spec.Ports[0].NodePort)
	a := ServiceSpec{ExternalIps: &[]string{"::1", "192.0.2.1", "::1"},
		LoadBalancerSourceRanges: &[]string{"192.0.2.3/24", "::1/64"}}
	b := ServiceSpec{ExternalIps: &[]string{"192.0.2.1", "::1"},
		LoadBalancerSourceRanges: &[]string{"::1/64", "192.0.2.3/24"}}
	require.True(t, equalServiceSpec(a, b))
	require.Equal(t, []string{"2001:0db8::1", "2001:db8::1"},
		serviceSet([]string{"2001:db8::1", "2001:0db8::1"}))
	require.False(t, equalServiceSpec(ServiceSpec{IPFamilies: &[]string{"IPv4", "IPv6"}},
		ServiceSpec{IPFamilies: &[]string{"IPv6", "IPv4"}}))
	before := ServiceSpec{ClusterIP: new("10.0.0.1"), ClusterIps: &[]string{"10.0.0.1"},
		LoadBalancerClass: new("old"), HealthCheckNodePort: new(int64(32000))}
	for _, change := range []func(*ServiceSpec){
		func(s *ServiceSpec) { s.ClusterIP = new("10.0.0.2") },
		func(s *ServiceSpec) { s.ClusterIps = &[]string{} },
		func(s *ServiceSpec) { s.LoadBalancerClass = new("changed") },
		func(s *ServiceSpec) { s.HealthCheckNodePort = new(int64(32001)) },
	} {
		after := before
		change(&after)
		require.True(t, replaceServiceSpec(before, after))
	}
	require.False(t, replaceServiceSpec(ServiceSpec{ClusterIP: new("10.0.0.1"),
		ClusterIps: &[]string{"10.0.0.1"}, HealthCheckNodePort: new(int64(31001))}, ServiceSpec{}))
	empty := (&ServiceResource{Spec: ServiceSpec{ClusterIps: &[]string{}}}).expand(nil)
	require.NotContains(t, string(mustServiceJSON(t, empty)), "clusterIPs")
}

func TestServiceMetadataFilters(t *testing.T) {
	for _, host := range []string{"app.kubernetes.io", "service.beta.kubernetes.io"} {
		key := host + "/deprecated.daemonset.template.generation"
		for _, tracked := range []bool{false, true} {
			for _, regex := range []bool{false, true} {
				baseline := map[string]string{}
				if tracked {
					baseline[key] = "old"
				}
				patterns := []string{}
				if regex {
					patterns = []string{"deprecated"}
				}
				got, err := serviceMetadataProject(map[string]string{key: "remote"}, baseline, &patterns)
				require.NoError(t, err)
				want := map[string]string{}
				if tracked || !regex {
					want[key] = "remote"
				}
				require.Equal(t, want, got)
				r := &ServiceResource{IgnoreAnnotations: &patterns, IgnoreLabels: &patterns}
				obj := liveService()
				obj.Annotations, obj.Labels = map[string]string{key: "remote"},
					map[string]string{key: "remote"}
				out, err := r.output(obj, "server", ServiceMetadataOutput{
					Annotations: baseline, Labels: baseline})
				require.NoError(t, err)
				require.Equal(t, want, out.Metadata.Annotations)
				require.Equal(t, want, out.Metadata.Labels)
			}
		}
	}
	actual := map[string]string{"kubernetes.io/key": "hidden", "else/ignored": "hidden",
		"custom/deprecated.daemonset.template.generation": "hidden", "visible": "value"}
	got, err := serviceMetadataProject(actual, nil, &[]string{"ignored"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"visible": "value"}, got)
	got, err = serviceMetadataProject(actual, actual, &[]string{"ignored"})
	require.NoError(t, err)
	require.Equal(t, actual, got)
}

func TestServiceOutputNormalization(t *testing.T) {
	r := &ServiceResource{}
	obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name: "name", Namespace: "default", UID: "uid"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1",
			ClusterIPs: []string{"10.0.0.1", "::1"}, ExternalIPs: []string{"::1", "1.2.3.4", "::1"},
			SessionAffinityConfig: &corev1.SessionAffinityConfig{},
			Ports:                 []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}}},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "1.2.3.4", IPMode: new(corev1.LoadBalancerIPModeVIP)},
				{Hostname: "endpoint"}, {}}}}}
	out, err := r.output(obj, "https://saved", ServiceMetadataOutput{})
	require.NoError(t, err)
	require.True(t, out.Spec.AllocateLoadBalancerNodePorts)
	require.Equal(t, obj.Spec.ClusterIPs, out.Spec.ClusterIps)
	require.Equal(t, []string{"1.2.3.4", "::1"}, out.Spec.ExternalIps)
	require.Equal(t, "8080", out.Spec.Port[0].TargetPort)
	require.Equal(t, []ServiceIngress{{IP: "1.2.3.4", IPMode: new("VIP")},
		{Hostname: "endpoint"}, {}}, out.Status.LoadBalancer.Ingress)
	body, err := ub.Marshal(out)
	require.NoError(t, err)
	var decoded ServiceResourceOutput
	require.NoError(t, ub.Unmarshal(body, &decoded))
	require.Equal(t, out, &decoded)
	obj.ResourceVersion, obj.Generation = "new", 17
	changed, err := r.output(obj, "https://saved", ServiceMetadataOutput{})
	require.NoError(t, err)
	require.Equal(t, out, changed)
	obj.Spec.Ports = nil
	a, err := r.output(obj, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	obj.Spec.Ports = []corev1.ServicePort{}
	b, err := r.output(obj, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	require.Equal(t, []ServicePortOutput{}, a.Spec.Port)
	require.Equal(t, a, b)
}
