package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func liveService() *corev1.Service {
	return &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "generated-actual", GenerateName: "generated-",
			Namespace: "default", UID: "uid"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP,
			ClusterIP: "10.0.0.1", ClusterIPs: []string{"10.0.0.1"},
			IPFamilies:            []corev1.IPFamily{corev1.IPv4Protocol},
			IPFamilyPolicy:        new(corev1.IPFamilyPolicySingleStack),
			InternalTrafficPolicy: new(corev1.ServiceInternalTrafficPolicyCluster),
			SessionAffinity:       corev1.ServiceAffinityNone,
			Ports: []corev1.ServicePort{{Port: 80, Protocol: corev1.ProtocolTCP,
				TargetPort: intstr.FromInt32(80)}}}}
}

func savedService(t *testing.T, r *ServiceResource, server string) *ServiceResourceOutput {
	t.Helper()
	out, err := r.output(liveService(), server, ServiceMetadataOutput{})
	require.NoError(t, err)
	return out
}

func TestServiceCreate(t *testing.T) {
	for _, ports := range []*[]ServicePort{nil, &[]ServicePort{}, &[]ServicePort{{Port: 80}}} {
		var calls []string
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method+" "+req.URL.Path)
			if req.Method == http.MethodPost {
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				m := body["metadata"].(map[string]any)
				require.Equal(t, "default", m["namespace"])
				require.Equal(t, "generated-", m["generateName"])
				require.Equal(t, map[string]any{"EXAMPLE.COM/Key": ""}, m["annotations"])
				s := body["spec"].(map[string]any)
				require.Equal(t, "ClusterIP", s["type"])
				require.Equal(t, "None", s["sessionAffinity"])
				for _, key := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy",
					"externalTrafficPolicy", "internalTrafficPolicy", "allocateLoadBalancerNodePorts",
					"loadBalancerClass", "sessionAffinityConfig"} {
					require.NotContains(t, s, key)
				}
				if len(serviceValue(ports)) == 0 {
					require.NotContains(t, s, "ports")
				} else {
					require.Equal(t, []any{map[string]any{
						"port": float64(80), "protocol": "TCP", "targetPort": float64(0),
					}}, s["ports"])
				}
			}
			obj := liveService()
			if req.Method == http.MethodGet {
				obj.Spec.Ports[0].TargetPort = intstr.FromInt32(8080)
			}
			respond(t, w, obj)
		})
		r := &ServiceResource{Metadata: ServiceMetadata{GenerateName: new("generated-"),
			Annotations: &map[string]string{"EXAMPLE.COM/Key": ""}}, Spec: ServiceSpec{Port: ports}}
		out, err := r.Create(t.Context(), c)
		require.NoError(t, err)
		require.Equal(t, []string{"POST /api/v1/namespaces/default/services",
			"GET /api/v1/namespaces/default/services/generated-actual"}, calls)
		require.Equal(t, "generated-actual", out.Name)
		require.Equal(t, "uid", out.UID)
		require.Equal(t, "10.0.0.1", out.Spec.ClusterIP)
		require.Equal(t, "8080", out.Spec.Port[0].TargetPort)
	}
}

func TestServiceIngressWait(t *testing.T) {
	for _, tt := range []struct {
		name string
		lb   bool
		wait *bool
		gets int
	}{
		{"pending then ready", true, nil, 3}, {"skip", true, new(false), 1},
		{"non lb", false, new(true), 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gets := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				obj := liveService()
				if tt.lb {
					obj.Spec.Type = corev1.ServiceTypeLoadBalancer
				}
				if req.Method == http.MethodGet {
					gets++
					if gets == 2 {
						obj.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{}}
					}
					if gets == 3 {
						obj.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
							{IP: "192.0.2.1", IPMode: new(corev1.LoadBalancerIPModeVIP)}, {Hostname: "current"}}
					}
				}
				respond(t, w, obj)
			})
			r := &ServiceResource{WaitForLoadBalancer: tt.wait}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, tt.gets, gets)
			if tt.gets == 3 {
				require.Equal(t, []ServiceIngress{
					{IP: "192.0.2.1", IPMode: new("VIP")}, {Hostname: "current"},
				}, out.Status.LoadBalancer.Ingress)
			}
		})
	}
}

func TestServiceCreateErrorsAndWarnings(t *testing.T) {
	for _, status := range []int{403, 409, 422} {
		calls := 0
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			require.Equal(t, http.MethodPost, req.Method)
			namespaceStatus(t, w, status)
		})
		out, err := (&ServiceResource{}).Create(t.Context(), c)
		require.Error(t, err)
		require.Nil(t, out)
		require.Equal(t, 1, calls)
	}
	for _, eventFailure := range []bool{false, true} {
		var calls []string
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method+" "+req.URL.Path)
			if req.Method == http.MethodPost {
				obj := liveService()
				obj.Spec.Type = corev1.ServiceTypeLoadBalancer
				respond(t, w, obj)
				return
			}
			if strings.HasSuffix(req.URL.Path, "/events") {
				require.Equal(t, "involvedObject.kind=Service,"+
					"involvedObject.name=generated-actual,involvedObject.namespace=default",
					req.URL.Query().Get("fieldSelector"))
				if eventFailure {
					namespaceStatus(t, w, 403)
					return
				}
				list := &corev1.EventList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "EventList"}}
				for index, message := range []string{"old", "third", "second", "first", "first", "normal"} {
					kind := "Warning"
					if message == "normal" {
						kind = "Normal"
					}
					list.Items = append(list.Items, corev1.Event{Type: kind, Reason: "Reason", Message: message,
						LastTimestamp: metav1.NewTime(time.Unix(int64(index), 0))})
				}
				respond(t, w, list)
				return
			}
			namespaceStatus(t, w, 500)
		})
		out, err := (&ServiceResource{}).Create(t.Context(), c)
		require.Error(t, err)
		require.Nil(t, out)
		require.Equal(t, []string{
			"POST /api/v1/namespaces/default/services",
			"GET /api/v1/namespaces/default/services/generated-actual",
			"GET /api/v1/namespaces/default/events",
		}, calls)
		if eventFailure {
			require.Contains(t, err.Error(), "list Service warning events")
		} else {
			for _, text := range []string{"Reason: first", "Reason: second", "Reason: third"} {
				require.Contains(t, err.Error(), text)
			}
			require.NotContains(t, err.Error(), "Reason: old")
			require.Less(t, strings.Index(err.Error(), "first"), strings.Index(err.Error(), "second"))
		}
	}
}

func TestServiceReadIdentityAndErrors(t *testing.T) {
	for _, status := range []int{200, 404, 403, 500} {
		calls := 0
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			require.Equal(t, "GET /api/v1/namespaces/default/services/generated-actual",
				req.Method+" "+req.URL.Path)
			if status != 200 {
				namespaceStatus(t, w, status)
				return
			}
			respond(t, w, liveService())
		})
		r := &ServiceResource{}
		saved := savedService(t, r, *c.Server)
		c.Server = new("https://unused.invalid")
		out, err := r.Read(t.Context(), c, servicePrior{Outputs: saved})
		if status == 200 {
			require.NoError(t, err)
			require.Equal(t, saved, out)
		} else {
			require.Error(t, err)
			require.Nil(t, out)
			require.Equal(t, status == 404, errors.Is(err, runtime.ErrNotFound))
		}
		require.Equal(t, 1, calls)
	}
}

func TestServiceControlAndStatusReconciliation(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		obj := liveService()
		obj.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "current"}}
		respond(t, w, obj)
	})
	r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}}}
	p := servicePrior{Inputs: *r, Outputs: savedService(t, r, *c.Server)}
	r.WaitForLoadBalancer = new(false)
	out, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"GET /api/v1/namespaces/default/services/generated-actual"}, calls)
	require.Equal(t, []ServiceIngress{{Hostname: "current"}}, out.Status.LoadBalancer.Ingress)
}

func TestServiceCancellationAndTimeouts(t *testing.T) {
	d, err := serviceCreateDuration(nil)
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, d)
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if strings.HasSuffix(req.URL.Path, "/events") {
			respond(t, w, &corev1.EventList{})
			return
		}
		obj := liveService()
		obj.Spec.Type = corev1.ServiceTypeLoadBalancer
		respond(t, w, obj)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	out, err := (&ServiceResource{}).Create(ctx, c)
	require.Error(t, err)
	require.Nil(t, out)
	for _, call := range calls {
		require.NotContains(t, call, "DELETE")
	}
	ctx, stop := context.WithCancel(t.Context())
	stop()
	_, err = (&ServiceResource{}).Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
}
