package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestServiceAPIAuthority(t *testing.T) {
	for _, spec := range []ServiceSpec{
		{},
		{ClusterIP: new("192.0.2.1"), ClusterIps: &[]string{"192.0.2.2"}},
		{Type: new("ExternalName"), ExternalName: new("BAD /"), Port: &[]ServicePort{{Port: 80}}},
		{Type: new("LoadBalancer"), LoadBalancerClass: new("BAD /"), Port: &[]ServicePort{{Port: 80}}},
		{HealthCheckNodePort: new(int64(31001))},
		{Port: &[]ServicePort{{Port: 80, TargetPort: new("BAD /")}}},
		{Port: &[]ServicePort{{Port: 80, Name: new("duplicate")}, {Port: 81, Name: new("duplicate")}}},
		{Port: &[]ServicePort{{Port: 80, AppProtocol: new("BAD /")}}},
	} {
		calls := 0
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			require.Equal(t, http.MethodPost, req.Method)
			var sent corev1.Service
			require.NoError(t, json.NewDecoder(req.Body).Decode(&sent))
			expected := (&ServiceResource{Spec: spec}).expand(nil).Spec
			require.JSONEq(t, string(mustServiceJSON(t, expected)),
				string(mustServiceJSON(t, sent.Spec)))
			namespaceStatus(t, w, 422)
		})
		r := &ServiceResource{Metadata: ServiceMetadata{Name: new("valid")}, Spec: spec}
		require.Empty(t, checkNativeCoreInputs(t, "service", r))
		out, err := r.Create(t.Context(), c)
		require.Error(t, err)
		require.Nil(t, out)
		require.Equal(t, 1, calls)
	}
}

func TestServiceLoadBalancerCreationFields(t *testing.T) {
	for _, kind := range []string{"ClusterIP", "LoadBalancer"} {
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodPost {
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				s := body["spec"].(map[string]any)
				if kind == "LoadBalancer" {
					require.Equal(t, false, s["allocateLoadBalancerNodePorts"])
					require.Equal(t, "class", s["loadBalancerClass"])
				} else {
					require.NotContains(t, s, "allocateLoadBalancerNodePorts")
					require.NotContains(t, s, "loadBalancerClass")
				}
			}
			obj := liveService()
			obj.Spec.Type = corev1.ServiceType(kind)
			obj.Spec.AllocateLoadBalancerNodePorts = new(false)
			respond(t, w, obj)
		})
		r := &ServiceResource{WaitForLoadBalancer: new(false), Spec: ServiceSpec{Type: &kind,
			AllocateLoadBalancerNodePorts: new(false), LoadBalancerClass: new("class")}}
		out, err := r.Create(t.Context(), c)
		require.NoError(t, err)
		require.Equal(t, kind != "LoadBalancer", out.Spec.AllocateLoadBalancerNodePorts)
	}
}

func TestServiceWaitAndFinalReadErrors(t *testing.T) {
	for _, status := range []int{404, 403, 500} {
		gets, events := 0, 0
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodPost {
				obj := liveService()
				obj.Spec.Type = corev1.ServiceTypeLoadBalancer
				respond(t, w, obj)
				return
			}
			if strings.HasSuffix(req.URL.Path, "/events") {
				events++
				respond(t, w, &corev1.EventList{})
				return
			}
			gets++
			namespaceStatus(t, w, status)
		})
		out, err := (&ServiceResource{}).Create(t.Context(), c)
		require.Error(t, err)
		require.Nil(t, out)
		require.Equal(t, 1, gets)
		require.Equal(t, 1, events)
	}
	gets := 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			gets++
			namespaceStatus(t, w, 500)
			return
		}
		require.Equal(t, http.MethodPost, req.Method)
		respond(t, w, liveService())
	})
	out, err := (&ServiceResource{}).Create(t.Context(), c)
	require.Error(t, err)
	require.Nil(t, out)
	require.Equal(t, 1, gets)
}

func TestServiceExplicitNamespaceAndFinalUpdateRead(t *testing.T) {
	var calls []string
	obj := liveService()
	obj.Namespace = "selected"
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.URL.Path == "/version" {
			respond(t, w, map[string]string{"gitVersion": "v1.33.4"})
			return
		}
		if req.Method == http.MethodGet && len(calls) == 6 {
			namespaceStatus(t, w, 403)
			return
		}
		respond(t, w, obj)
	})
	r := &ServiceResource{Metadata: ServiceMetadata{Namespace: new("selected")},
		Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}}}
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	p := servicePrior{Inputs: *r, Outputs: created}
	r.Spec.ExternalName = new("external.example")
	out, err := r.Update(t.Context(), c, p)
	require.Error(t, err)
	require.Nil(t, out)
	require.Equal(t, []string{
		"POST /api/v1/namespaces/selected/services",
		"GET /api/v1/namespaces/selected/services/generated-actual",
		"GET /api/v1/namespaces/selected/services/generated-actual",
		"GET /version",
		"PATCH /api/v1/namespaces/selected/services/generated-actual",
		"GET /api/v1/namespaces/selected/services/generated-actual",
	}, calls)
}

func TestServiceMetadataOnlyAndLoadBalancerUpdate(t *testing.T) {
	for _, transition := range []bool{false, true} {
		var calls []string
		obj := liveService()
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method+" "+req.URL.Path)
			if req.URL.Path == "/version" {
				respond(t, w, map[string]string{"gitVersion": "v1.33.4"})
				return
			}
			if req.Method == http.MethodPatch {
				var ops []servicePatchOperation
				require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
				obj = applyServicePatch(t, obj, mustServiceJSON(t, ops))
			}
			respond(t, w, obj)
		})
		r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}}}
		p := servicePrior{Inputs: *r, Outputs: savedService(t, r, *c.Server)}
		if transition {
			r.Spec.Type = new("LoadBalancer")
		} else {
			r.Metadata.Labels = &map[string]string{"app": "updated"}
		}
		out, err := r.Update(t.Context(), c, p)
		require.NoError(t, err)
		require.Empty(t, out.Status.LoadBalancer.Ingress)
		require.Equal(t, "uid", out.UID)
		want := []string{"GET /api/v1/namespaces/default/services/generated-actual"}
		if transition {
			want = append(want, "GET /version")
			require.Equal(t, "LoadBalancer", out.Spec.Type)
		}
		want = append(want, "PATCH /api/v1/namespaces/default/services/generated-actual",
			"GET /api/v1/namespaces/default/services/generated-actual")
		require.Equal(t, want, calls)
	}
	require.Equal(t, 20*time.Minute, serviceOperationTimeout)
}

func TestServiceNonLoadBalancerAllocationError(t *testing.T) {
	patches := 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/version" {
			respond(t, w, map[string]string{"gitVersion": "v1.33.4"})
			return
		}
		if req.Method == http.MethodPatch {
			patches++
			namespaceStatus(t, w, 422)
			return
		}
		respond(t, w, liveService())
	})
	r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}}}
	p := servicePrior{Inputs: *r, Outputs: savedService(t, r, *c.Server)}
	r.Spec.AllocateLoadBalancerNodePorts = new(false)
	out, err := r.Update(t.Context(), c, p)
	require.Error(t, err)
	require.Nil(t, out)
	require.Equal(t, 1, patches)
}

func TestServiceVersionDiscoveryCancellation(t *testing.T) {
	canceled := make(chan struct{})
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/version" {
			<-req.Context().Done()
			close(canceled)
			return
		}
		require.Equal(t, http.MethodGet, req.Method)
		respond(t, w, liveService())
	})
	r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}}}
	p := servicePrior{Inputs: *r, Outputs: savedService(t, r, *c.Server)}
	r.Spec.ExternalName = new("external.example")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out, err := r.Update(ctx, c, p)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, out)
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("version request did not receive caller cancellation")
	}
}

func TestServiceTransitionRefreshesClearedFields(t *testing.T) {
	obj := liveService()
	obj.Spec.Type = corev1.ServiceTypeNodePort
	obj.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyCluster
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/version" {
			respond(t, w, map[string]string{"gitVersion": "v1.33.4"})
			return
		}
		if req.Method == http.MethodPatch {
			var ops []servicePatchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
			obj = applyServicePatch(t, obj, mustServiceJSON(t, ops))
			obj.Spec.ClusterIPs, obj.Spec.IPFamilies, obj.Spec.IPFamilyPolicy = nil, nil, nil
			obj.Spec.InternalTrafficPolicy = nil
			obj.Spec.ExternalTrafficPolicy = ""
		}
		respond(t, w, obj)
	})
	r := &ServiceResource{Spec: ServiceSpec{Type: new("NodePort"),
		Port: &[]ServicePort{{Port: 80}}}}
	prior, err := r.output(obj, *c.Server, ServiceMetadataOutput{})
	require.NoError(t, err)
	p := servicePrior{Inputs: *r, Outputs: prior}
	r.Spec.Type, r.Spec.ExternalName = new("ExternalName"), new("external.example")
	out, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Empty(t, out.Spec.ClusterIP)
	require.Equal(t, []string{}, out.Spec.ClusterIps)
	require.Equal(t, []string{}, out.Spec.IPFamilies)
	require.Empty(t, out.Spec.IPFamilyPolicy)
	require.Empty(t, out.Spec.InternalTrafficPolicy)
	require.Empty(t, out.Spec.ExternalTrafficPolicy)
}

func TestServiceEmptyFamilyUpdateAndSparseAffinity(t *testing.T) {
	old := liveService().Spec
	r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}, IPFamilies: &[]string{}}}
	ops := serviceSpecPatch(old, r.expand(&old).Spec, r.Spec, false)
	var family any
	for _, op := range ops {
		if op.Path == "/spec/ipFamilies" {
			family = op.Value
		}
	}
	require.Equal(t, []corev1.IPFamily{}, family)
	for _, config := range []*corev1.SessionAffinityConfig{
		nil, {}, {ClientIP: &corev1.ClientIPConfig{}},
		{ClientIP: &corev1.ClientIPConfig{TimeoutSeconds: new(int32(10800))}}} {
		obj := liveService()
		obj.Spec.SessionAffinityConfig = config
		out, err := r.output(obj, "server", ServiceMetadataOutput{})
		require.NoError(t, err)
		if config == nil {
			require.Nil(t, out.Spec.SessionAffinityConfig)
		} else if config.ClientIP != nil {
			require.Equal(t, config.ClientIP.TimeoutSeconds,
				out.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds)
		}
	}
	obj := liveService()
	obj.Spec.Type = corev1.ServiceTypeLoadBalancer
	obj.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyLocal
	obj.Spec.HealthCheckNodePort = 31001
	out, err := r.output(obj, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	require.Equal(t, int32(31001), out.Spec.HealthCheckNodePort)
	obj.Spec.Type = corev1.ServiceTypeClusterIP
	out, err = r.output(obj, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	require.Zero(t, out.Spec.HealthCheckNodePort)
}
