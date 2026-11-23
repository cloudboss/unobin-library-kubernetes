package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestServiceInfoRead(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "/api/v1/namespaces/default/services/svc", req.URL.Path)
		require.Empty(t, req.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default", UID: "uid"},
		}))
	}))
	defer server.Close()
	r := &ServiceDataSource{Metadata: ServiceLookupMetadata{Name: new("svc")}}
	out, err := r.Read(t.Context(), &config.Configuration{
		Server: &server.URL, Namespace: new("other"),
	})
	require.NoError(t, err)
	require.Equal(t, "svc", out.Metadata.Name)
	require.Equal(t, "uid", out.Metadata.UID)
	require.NotNil(t, out.Spec)
	require.NotNil(t, out.Status)
	require.Equal(t, 1, calls)
}

func TestServiceInfoLookupMetadata(t *testing.T) {
	for _, tc := range []struct {
		name      string
		metadata  ServiceLookupMetadata
		path      string
		errorText string
	}{
		{name: "default", metadata: ServiceLookupMetadata{Name: new("svc")},
			path: "/api/v1/namespaces/default/services/svc"},
		{name: "explicit", metadata: ServiceLookupMetadata{Name: new("svc"), Namespace: new("other")},
			path: "/api/v1/namespaces/other/services/svc"},
		{name: "empty namespace", metadata: ServiceLookupMetadata{Name: new("svc"), Namespace: new("")},
			path: "/api/v1/services/svc"},
		{name: "unvalidated namespace", metadata: ServiceLookupMetadata{
			Name: new("svc"), Namespace: new("UPPER")}, path: "/api/v1/namespaces/UPPER/services/svc"},
		{name: "omitted name", errorText: "resource name may not be empty"},
		{name: "maps are not selectors", metadata: ServiceLookupMetadata{
			Name: new("svc"), Annotations: &map[string]string{"EXAMPLE.COM/Key": "unmatched"},
			Labels: &map[string]string{"app": "unmatched"}},
			path: "/api/v1/namespaces/default/services/svc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, tc.path, req.URL.Path)
				require.Empty(t, req.URL.RawQuery)
				namespaceStatus(t, w, http.StatusNotFound)
			})
			out, err := (&ServiceDataSource{Metadata: tc.metadata}).Read(t.Context(), c)
			if tc.errorText != "" {
				require.ErrorContains(t, err, tc.errorText)
				require.Nil(t, out)
				require.Zero(t, calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, "svc", out.Metadata.Name)
			require.Equal(t, serviceDefault(tc.metadata.Namespace, "default"), out.Metadata.Namespace)
			require.Equal(t, serviceMap(tc.metadata.Annotations), out.Metadata.Annotations)
			require.Equal(t, serviceMap(tc.metadata.Labels), out.Metadata.Labels)
			require.Nil(t, out.Spec)
			require.Nil(t, out.Status)
		})
	}
}

func TestServiceInfoErrorsAndFreshReads(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				namespaceStatus(t, w, code)
			})
			out, err := (&ServiceDataSource{Metadata: ServiceLookupMetadata{Name: new("svc")}}).
				Read(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			var status apierrors.APIStatus
			require.ErrorAs(t, err, &status)
			require.Equal(t, int32(code), status.Status().Code)
			require.Equal(t, 1, calls)
		})
	}
	calls := 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, req.Method)
		if calls == 3 {
			namespaceStatus(t, w, http.StatusNotFound)
			return
		}
		respond(t, w, &corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "default", ResourceVersion: fmt.Sprint(calls),
		}, Spec: corev1.ServiceSpec{ExternalName: fmt.Sprint(calls)},
			Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{Hostname: fmt.Sprint(calls)}},
			}}})
	})
	r := &ServiceDataSource{Metadata: ServiceLookupMetadata{Name: new("svc")}}
	first, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	second, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "1", first.Metadata.ResourceVersion)
	require.Equal(t, "2", second.Metadata.ResourceVersion)
	require.Equal(t, "1", first.Spec.ExternalName)
	require.Equal(t, "2", second.Spec.ExternalName)
	require.Equal(t, "1", first.Status.LoadBalancer.Ingress[0].Hostname)
	require.Equal(t, "2", second.Status.LoadBalancer.Ingress[0].Hostname)
	missing, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Nil(t, missing.Spec)
	require.Nil(t, missing.Status)
	require.Empty(t, missing.Metadata.ResourceVersion)
	require.Equal(t, 3, calls)
	out, err := r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("!")})
	require.ErrorContains(t, err, "base64")
	require.Nil(t, out)
	c = configMapAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, err := fmt.Fprint(w, "not JSON")
		require.NoError(t, err)
	})
	out, err = r.Read(t.Context(), c)
	require.Error(t, err)
	require.Nil(t, out)
}

func TestServiceInfoFullResponse(t *testing.T) {
	annotations := map[string]string{"kubernetes.io/internal": "yes",
		"service.beta.kubernetes.io/lb": "yes", "EXAMPLE.COM/Key": "unchanged",
		"deprecated.daemonset.template.generation": "7", "controller.example/key": "all"}
	labels := map[string]string{"kubernetes.io/internal": "yes", "app.kubernetes.io/name": "svc",
		"deprecated.daemonset.template.generation": "7", "controller.example/key": "all"}
	obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "remote-ns",
		GenerateName: "generated-", UID: "uid", Generation: 7, ResourceVersion: "rv",
		Annotations: annotations, Labels: labels, Finalizers: []string{"ignored"},
		ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "ignored"}}, CreationTimestamp: metav1.Now(),
	}, Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeLoadBalancer, AllocateLoadBalancerNodePorts: new(false),
		ClusterIP: "None", ClusterIPs: []string{"2001:db8::1", "10.0.0.1"},
		ExternalIPs:  []string{"2001:db8::01", "192.0.2.2", "2001:db8::1", "192.0.2.2"},
		ExternalName: "name.example", ExternalTrafficPolicy: "UnknownExternal",
		InternalTrafficPolicy: new(corev1.ServiceInternalTrafficPolicy("UnknownInternal")),
		IPFamilies:            []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol},
		IPFamilyPolicy:        new(corev1.IPFamilyPolicy("UnknownPolicy")),
		LoadBalancerClass:     new("class"), LoadBalancerIP: "2001:db8::3",
		LoadBalancerSourceRanges: []string{"192.0.2.7/24", "192.0.2.0/24", "192.0.2.7/24"},
		HealthCheckNodePort:      31000, PublishNotReadyAddresses: true,
		Selector: map[string]string{"app": "remote"}, SessionAffinity: "UnknownAffinity",
		SessionAffinityConfig: &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{
			TimeoutSeconds: new(int32(42)),
		}}, Ports: []corev1.ServicePort{
			{Name: "first", Port: 80, Protocol: "UnknownProtocol", TargetPort: intstr.FromInt32(8080)},
			{Name: "second", Port: 81, Protocol: corev1.ProtocolUDP, NodePort: 31001,
				AppProtocol: new("http"), TargetPort: intstr.FromString("http")},
		},
	}, Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
		Ingress: []corev1.LoadBalancerIngress{
			{IP: "192.0.2.1", IPMode: new(corev1.LoadBalancerIPModeVIP)},
			{Hostname: "host", IPMode: new(corev1.LoadBalancerIPModeProxy)}, {IP: "2001:db8::1"}, {},
		},
	}}}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/namespaces/default/services/svc", req.URL.Path)
		require.Equal(t, http.MethodGet, req.Method)
		require.Empty(t, req.URL.RawQuery)
		respond(t, w, obj)
	})
	r := &ServiceDataSource{Metadata: ServiceLookupMetadata{Name: new("svc"),
		Annotations: &map[string]string{"input": "unused"},
		Labels:      &map[string]string{"input": "unused"}}}
	out, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, ServiceLookupMetadataOutput{Name: "remote", Namespace: "remote-ns", UID: "uid",
		Generation: 7, ResourceVersion: "rv", Annotations: annotations, Labels: labels}, out.Metadata)
	require.Equal(t, &ServiceLookupSpec{
		Type: "LoadBalancer", AllocateLoadBalancerNodePorts: false,
		ClusterIP: "None", ClusterIps: []string{"2001:db8::1", "10.0.0.1"},
		ExternalIps:  []string{"192.0.2.2", "2001:db8::01", "2001:db8::1"},
		ExternalName: "name.example", ExternalTrafficPolicy: "UnknownExternal",
		InternalTrafficPolicy: "UnknownInternal", IPFamilies: []string{"IPv6", "IPv4"},
		IPFamilyPolicy: "UnknownPolicy", LoadBalancerClass: "class", LoadBalancerIP: "2001:db8::3",
		LoadBalancerSourceRanges: []string{"192.0.2.0/24", "192.0.2.7/24"},
		HealthCheckNodePort:      31000, PublishNotReadyAddresses: true,
		Selector: map[string]string{"app": "remote"}, SessionAffinity: "UnknownAffinity",
		SessionAffinityConfig: &ServiceLookupAffinityConfig{ClientIP: &ServiceLookupClientIP{
			TimeoutSeconds: 42,
		}}, Port: []ServicePortOutput{
			{Name: "first", Port: 80, Protocol: "UnknownProtocol", TargetPort: "8080"},
			{Name: "second", Port: 81, Protocol: "UDP", NodePort: 31001,
				AppProtocol: "http", TargetPort: "http"},
		},
	}, out.Spec)
	require.Equal(t, []ServiceLookupIngress{
		{IP: "192.0.2.1", IPMode: "VIP"}, {Hostname: "host", IPMode: "Proxy"},
		{IP: "2001:db8::1"}, {},
	}, out.Status.LoadBalancer.Ingress)
	before, err := ub.Marshal(out)
	require.NoError(t, err)
	obj.Spec.ExternalIPs = []string{"2001:db8::1", "192.0.2.2", "2001:db8::01"}
	obj.Spec.LoadBalancerSourceRanges = []string{"192.0.2.0/24", "192.0.2.7/24"}
	out, err = r.Read(t.Context(), c)
	require.NoError(t, err)
	after, err := ub.Marshal(out)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	for _, serviceType := range []corev1.ServiceType{
		corev1.ServiceTypeClusterIP, corev1.ServiceTypeNodePort, corev1.ServiceTypeExternalName,
		corev1.ServiceType("UnknownType"),
	} {
		obj.Spec.Type = serviceType
		current, err := r.Read(t.Context(), c)
		require.NoError(t, err)
		require.Equal(t, string(serviceType), current.Spec.Type)
		require.True(t, current.Spec.AllocateLoadBalancerNodePorts)
		require.Empty(t, current.Spec.LoadBalancerClass)
		require.Equal(t, out.Status, current.Status)
	}
	for _, excluded := range []string{"generate-name:", "finalizers:", "managed-fields:",
		"creation-timestamp:", "traffic-distribution:", "conditions:"} {
		require.NotContains(t, string(after), excluded)
	}
}

func TestServiceInfoSparseResponses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spec       corev1.ServiceSpec
		allocation bool
		class      string
		affinity   *ServiceLookupAffinityConfig
	}{
		{name: "zero", allocation: true},
		{name: "non LB", spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort,
			AllocateLoadBalancerNodePorts: new(false), LoadBalancerClass: new("ignored")}, allocation: true},
		{name: "external name", spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName,
			ExternalName: "example.com"}, allocation: true},
		{name: "LB nil", spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
			allocation: true},
		{name: "LB true", spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer,
			AllocateLoadBalancerNodePorts: new(true)}, allocation: true},
		{name: "LB false", spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer,
			AllocateLoadBalancerNodePorts: new(false), LoadBalancerClass: new("class")}, class: "class"},
		{name: "empty affinity", spec: corev1.ServiceSpec{
			SessionAffinityConfig: &corev1.SessionAffinityConfig{}}, allocation: true,
			affinity: &ServiceLookupAffinityConfig{}},
		{name: "nil timeout", spec: corev1.ServiceSpec{
			SessionAffinityConfig: &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{}},
		}, allocation: true,
			affinity: &ServiceLookupAffinityConfig{ClientIP: &ServiceLookupClientIP{}}},
		{name: "remote timeout", spec: corev1.ServiceSpec{
			SessionAffinityConfig: &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{
				TimeoutSeconds: new(int32(10800)),
			}},
		}, allocation: true,
			affinity: &ServiceLookupAffinityConfig{ClientIP: &ServiceLookupClientIP{TimeoutSeconds: 10800}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := configMapAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				respond(t, w, &corev1.Service{Spec: tc.spec})
			})
			out, err := (&ServiceDataSource{Metadata: ServiceLookupMetadata{Name: new("svc")}}).
				Read(t.Context(), c)
			require.NoError(t, err)
			require.NotNil(t, out.Spec)
			require.NotNil(t, out.Status)
			if tc.name == "zero" {
				require.Equal(t, &ServiceLookupSpec{
					AllocateLoadBalancerNodePorts: true, ClusterIps: []string{}, ExternalIps: []string{},
					IPFamilies: []string{}, LoadBalancerSourceRanges: []string{},
					Selector: map[string]string{}, Port: []ServicePortOutput{},
				}, out.Spec)
			}
			require.Equal(t, string(tc.spec.Type), out.Spec.Type)
			require.Equal(t, tc.spec.ExternalName, out.Spec.ExternalName)
			require.Equal(t, tc.allocation, out.Spec.AllocateLoadBalancerNodePorts)
			require.Equal(t, tc.class, out.Spec.LoadBalancerClass)
			require.Equal(t, tc.affinity, out.Spec.SessionAffinityConfig)
			require.Equal(t, []string{}, out.Spec.ClusterIps)
			require.Equal(t, []string{}, out.Spec.ExternalIps)
			require.Equal(t, []string{}, out.Spec.IPFamilies)
			require.Equal(t, []string{}, out.Spec.LoadBalancerSourceRanges)
			require.Equal(t, []ServicePortOutput{}, out.Spec.Port)
			require.Equal(t, map[string]string{}, out.Spec.Selector)
			require.Equal(t, map[string]string{}, out.Metadata.Annotations)
			require.Equal(t, map[string]string{}, out.Metadata.Labels)
			require.Equal(t, []ServiceLookupIngress{}, out.Status.LoadBalancer.Ingress)
			require.Empty(t, out.Spec.ClusterIP)
			require.Empty(t, out.Spec.SessionAffinity)
			require.Empty(t, out.Spec.IPFamilyPolicy)
			require.Empty(t, out.Spec.InternalTrafficPolicy)
			require.Zero(t, out.Spec.HealthCheckNodePort)
			require.False(t, out.Spec.PublishNotReadyAddresses)
			require.Equal(t, 1, calls)
		})
	}
}

func TestServiceInfoDeadlineAndTransport(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	r := &ServiceDataSource{Metadata: ServiceLookupMetadata{Name: new("svc")}}
	c := &config.Configuration{Server: new("http://service-info.invalid")}
	for _, earlier := range []bool{false, true} {
		t.Run(fmt.Sprint(earlier), func(t *testing.T) {
			ctx := t.Context()
			want := 20 * time.Minute
			if earlier {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
				want = time.Second
			}
			calls := 0
			sentinel := errors.New("transport failure")
			http.DefaultTransport = secretTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				require.InDelta(t, want.Seconds(), time.Until(deadline).Seconds(), 0.2)
				return nil, sentinel
			})
			out, err := r.Read(ctx, c)
			require.ErrorIs(t, err, sentinel)
			require.Nil(t, out)
			require.Equal(t, 1, calls)
		})
	}
	for _, cancelEarly := range []bool{false, true} {
		t.Run("cancel/"+fmt.Sprint(cancelEarly), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if cancelEarly {
				cancel()
			}
			calls := 0
			http.DefaultTransport = secretTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				<-req.Context().Done()
				return nil, req.Context().Err()
			})
			out, err := r.Read(ctx, c)
			require.Error(t, err)
			if cancelEarly {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			require.Nil(t, out)
			require.LessOrEqual(t, calls, 1)
		})
	}
}
