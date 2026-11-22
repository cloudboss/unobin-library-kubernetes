package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func applyServicePatch(t *testing.T, obj *corev1.Service, patch []byte) *corev1.Service {
	t.Helper()
	operations, err := jsonpatch.DecodePatch(patch)
	require.NoError(t, err)
	body, err := json.Marshal(obj)
	require.NoError(t, err)
	result, err := operations.Apply(body)
	require.NoError(t, err, "patch %s to %s", patch, body)
	var updated corev1.Service
	require.NoError(t, json.Unmarshal(result, &updated))
	return &updated
}

func TestServiceMutablePatches(t *testing.T) {
	old := liveService().Spec
	old.Type = corev1.ServiceTypeLoadBalancer
	old.AllocateLoadBalancerNodePorts = new(true)
	old.Ports[0].NodePort = 31001
	r := &ServiceResource{Spec: ServiceSpec{Type: new("LoadBalancer"),
		AllocateLoadBalancerNodePorts: new(false), ExternalName: new("external.example"),
		ExternalIps: &[]string{"192.0.2.1"}, LoadBalancerIP: new("192.0.2.2"),
		LoadBalancerSourceRanges: &[]string{"192.0.2.3/24"},
		Port:                     &[]ServicePort{{Port: 80, AppProtocol: new("http")}},
		Selector:                 &map[string]string{"app": "new"}, SessionAffinity: new("ClientIP"),
		SessionAffinityConfig: &ServiceAffinityConfig{
			ClientIP: &ServiceClientIP{TimeoutSeconds: new(int64(10))}},
		ExternalTrafficPolicy: new("Local"), InternalTrafficPolicy: new("Local"),
		IPFamilies: &[]string{"IPv4", "IPv6"}, IPFamilyPolicy: new("PreferDualStack"),
		PublishNotReadyAddresses: new(true)}}
	after := r.expand(&old).Spec
	operations := serviceSpecPatch(old, after, r.Spec, false)
	body, err := json.Marshal(operations)
	require.NoError(t, err)
	obj := liveService()
	obj.Spec = old
	updated := applyServicePatch(t, obj, body)
	require.Equal(t, after, updated.Spec)
	require.Equal(t, int32(31001), updated.Spec.Ports[0].NodePort)
	require.Contains(t, string(body), `"value":false`)
	r.Spec = ServiceSpec{Type: new("LoadBalancer"), Port: &[]ServicePort{{Port: 80}},
		IPFamilies: &[]string{"IPv4"}}
	after = r.expand(&updated.Spec).Spec
	body, err = json.Marshal(serviceSpecPatch(updated.Spec, after, r.Spec, false))
	require.NoError(t, err)
	cleared := applyServicePatch(t, updated, body)
	require.Empty(t, cleared.Spec.ExternalIPs)
	require.Empty(t, cleared.Spec.LoadBalancerSourceRanges)
	require.Empty(t, cleared.Spec.ExternalName)
	require.Empty(t, cleared.Spec.LoadBalancerIP)
	require.Empty(t, cleared.Spec.Selector)
	require.Equal(t, []corev1.IPFamily{corev1.IPv4Protocol}, cleared.Spec.IPFamilies)
	require.NotContains(t, string(mustServiceJSON(t, cleared)), "publishNotReadyAddresses")
	for _, ports := range []*[]ServicePort{nil, &[]ServicePort{}} {
		r.Spec.Port = ports
		body, err = json.Marshal(serviceSpecPatch(old, r.expand(&old).Spec, r.Spec, false))
		require.NoError(t, err)
		require.Contains(t, string(body), `"path":"/spec/ports","value":[]`)
	}
}

func mustServiceJSON(t *testing.T, obj any) []byte {
	t.Helper()
	body, err := json.Marshal(obj)
	require.NoError(t, err)
	return body
}

func TestServiceTypeAndLegacyPatches(t *testing.T) {
	for _, kind := range []string{"ExternalName", "ClusterIP", "LoadBalancer"} {
		old := liveService().Spec
		old.Type = corev1.ServiceTypeNodePort
		old.Ports[0].NodePort = 31001
		r := &ServiceResource{Spec: ServiceSpec{Type: &kind, Port: &[]ServicePort{{Port: 80}}}}
		ops := serviceSpecPatch(old, r.expand(&old).Spec, r.Spec, false)
		typeIndex, allocationIndex, clusterIndex, portsIndex := -1, -1, -1, -1
		for index, op := range ops {
			switch op.Path {
			case "/spec/type":
				typeIndex = index
			case "/spec/allocateLoadBalancerNodePorts":
				allocationIndex = index
				require.Equal(t, true, op.Value)
			case "/spec/clusterIP":
				clusterIndex = index
				require.Equal(t, "remove", op.Op)
			case "/spec/clusterIPs":
				t.Fatal("unexpected clusterIPs patch")
			case "/spec/ports":
				portsIndex = index
				require.Zero(t, op.Value.([]corev1.ServicePort)[0].NodePort)
			}
		}
		require.GreaterOrEqual(t, typeIndex, 0)
		if kind == "ExternalName" {
			require.GreaterOrEqual(t, clusterIndex, 0)
			require.Less(t, clusterIndex, typeIndex)
		}
		if kind == "LoadBalancer" {
			require.GreaterOrEqual(t, allocationIndex, 0)
			require.Less(t, allocationIndex, typeIndex)
		}
		if kind == "ClusterIP" {
			require.GreaterOrEqual(t, portsIndex, 0)
		}
	}
	r := &ServiceResource{Spec: ServiceSpec{ExternalIps: &[]string{"192.0.2.1"}}}
	ops := serviceSpecPatch(corev1.ServiceSpec{}, r.expand(nil).Spec, r.Spec, true)
	var relevant []servicePatchOperation
	for _, op := range ops {
		if op.Path == "/spec/deprecatedPublicIPs" || op.Path == "/spec/externalIPs" {
			relevant = append(relevant, op)
		}
	}
	require.Equal(t, "/spec/deprecatedPublicIPs", relevant[0].Path)
	require.Contains(t, string(mustServiceJSON(t, relevant)), `"value":null`)
	require.Equal(t, "/spec/externalIPs", relevant[1].Path)
}

func TestServiceMetadataPatchBaselines(t *testing.T) {
	for _, field := range []string{"annotations", "labels"} {
		for _, baseline := range []map[string]string{{}, {"example.com/key~": "old", "delete": "old"}} {
			for _, desired := range []map[string]string{{}, {"example.com/key~": "new", "added": "value"}} {
				actual := map[string]string{"kubernetes.io/hidden": "keep"}
				for key, value := range baseline {
					actual[key] = value
				}
				obj := liveService()
				if field == "annotations" {
					obj.Annotations = actual
				} else {
					obj.Labels = actual
				}
				ops := configMapMapPatch("/metadata/"+field, baseline, desired)
				body := mustServiceJSON(t, ops)
				updated := applyServicePatch(t, obj, body)
				got := updated.Annotations
				if field == "labels" {
					got = updated.Labels
				}
				want := map[string]string{}
				if len(baseline) != 0 || len(desired) == 0 {
					want["kubernetes.io/hidden"] = "keep"
				}
				for key, value := range desired {
					want[key] = value
				}
				require.Equal(t, want, got)
				if len(baseline) > 0 {
					require.Contains(t, string(body), "example.com~1key~0")
				}
				if len(baseline) == 0 && len(desired) == 0 {
					require.Empty(t, ops)
				}
			}
		}
	}
}

func TestServiceUpdateHTTP(t *testing.T) {
	for _, version := range []string{"v1.7.9", "v1.33.4"} {
		var calls []string
		obj := liveService()
		obj.Spec.Type = corev1.ServiceTypeNodePort
		obj.Spec.Ports[0].NodePort = 31001
		obj.Annotations = map[string]string{"visible": "old", "kubernetes.io/hidden": "keep"}
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method+" "+req.URL.Path)
			switch {
			case req.URL.Path == "/version":
				respond(t, w, map[string]string{"gitVersion": version})
			case req.Method == http.MethodPatch:
				require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Contains(t, string(body), `{"op":"test","path":"/metadata/uid","value":"uid"}`)
				if version == "v1.7.9" {
					require.Contains(t, string(body), `"/spec/deprecatedPublicIPs","value":null`)
				}
				obj = applyServicePatch(t, obj, body)
				respond(t, w, obj)
			default:
				respond(t, w, obj)
			}
		})
		r := &ServiceResource{Metadata: ServiceMetadata{
			Annotations: &map[string]string{"visible": "old"}},
			Spec: ServiceSpec{Type: new("NodePort"), Port: &[]ServicePort{{Port: 80}}}}
		saved, err := r.output(obj, *c.Server, ServiceMetadataOutput{})
		require.NoError(t, err)
		p := servicePrior{Inputs: *r, Outputs: saved}
		c.Server = new("https://unused.invalid")
		r.Metadata.Annotations = &map[string]string{"visible": "new"}
		r.Spec.ExternalIps = &[]string{"192.0.2.1"}
		out, err := r.Update(t.Context(), c, p)
		require.NoError(t, err)
		require.Equal(t, []string{"GET /api/v1/namespaces/default/services/generated-actual",
			"GET /version", "PATCH /api/v1/namespaces/default/services/generated-actual",
			"GET /api/v1/namespaces/default/services/generated-actual"}, calls)
		require.Equal(t, "uid", out.UID)
		require.Equal(t, int32(31001), out.Spec.Port[0].NodePort)
		require.Equal(t, "keep", obj.Annotations["kubernetes.io/hidden"])
	}
}

func TestServiceUpdateFailures(t *testing.T) {
	for _, failure := range []string{"discovery", "version", "patch", "empty families", "uid"} {
		t.Run(failure, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.URL.Path == "/version" {
					if failure == "discovery" {
						namespaceStatus(t, w, 403)
					} else {
						v := "v1.33.4"
						if failure == "version" {
							v = "invalid"
						}
						respond(t, w, map[string]string{"gitVersion": v})
					}
					return
				}
				if req.Method == http.MethodPatch {
					namespaceStatus(t, w, 422)
					return
				}
				obj := liveService()
				if failure == "uid" {
					obj.UID = "other"
				}
				respond(t, w, obj)
			})
			r := &ServiceResource{Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}}}
			p := servicePrior{Inputs: *r, Outputs: savedService(t, r, *c.Server)}
			r.Spec.IPFamilies = &[]string{"IPv6"}
			if failure == "empty families" {
				r.Spec.IPFamilies = &[]string{}
			}
			out, err := r.Update(t.Context(), c, p)
			require.Error(t, err)
			require.Nil(t, out)
			want := []string{"GET /api/v1/namespaces/default/services/generated-actual"}
			if failure != "uid" {
				want = append(want, "GET /version")
			}
			if failure == "patch" || failure == "empty families" {
				want = append(want, "PATCH /api/v1/namespaces/default/services/generated-actual")
			}
			require.Equal(t, want, calls)
		})
	}
}

func serviceDiscovery(t *testing.T, w http.ResponseWriter, req *http.Request) bool {
	t.Helper()
	switch req.URL.Path {
	case "/api":
		respond(t, w, metav1.APIVersions{Versions: []string{"v1"}})
	case "/apis":
		respond(t, w, metav1.APIGroupList{})
	case "/api/v1":
		respond(t, w, metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "services", Kind: "Service", Namespaced: true, Verbs: []string{"get", "delete"}}}})
	default:
		return false
	}
	return true
}

func TestServiceDelete(t *testing.T) {
	for _, mode := range []string{
		"not found", "gone", "different uid", "forbidden", "poll error", "cancel",
	} {
		t.Run(mode, func(t *testing.T) {
			gets, deletes := 0, 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if serviceDiscovery(t, w, req) {
					return
				}
				if req.Method == http.MethodDelete {
					deletes++
					var options metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
					require.Equal(t, "uid", string(*options.Preconditions.UID))
					require.Nil(t, options.PropagationPolicy)
					require.Nil(t, options.GracePeriodSeconds)
					if mode == "not found" {
						namespaceStatus(t, w, 404)
						return
					}
					if mode == "forbidden" {
						namespaceStatus(t, w, 403)
						return
					}
					respond(t, w, &metav1.Status{Status: "Success"})
					return
				}
				gets++
				if mode == "gone" && gets == 2 {
					namespaceStatus(t, w, 404)
					return
				}
				if mode == "poll error" {
					namespaceStatus(t, w, 403)
					return
				}
				obj := liveService()
				if mode == "different uid" {
					obj.UID = "other"
				}
				respond(t, w, obj)
			})
			r := &ServiceResource{}
			p := servicePrior{Outputs: savedService(t, r, *c.Server)}
			c.Server = new("https://unused.invalid")
			ctx := t.Context()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 40*time.Millisecond)
				defer cancel()
			}
			err := r.Delete(ctx, c, p)
			if mode == "forbidden" || mode == "poll error" || mode == "cancel" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, deletes)
			if mode == "gone" {
				require.Equal(t, 2, gets)
			}
		})
	}
}
