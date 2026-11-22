package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func testAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIVersions{Versions: []string{"v1"}}))
		case "/apis":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "networking.k8s.io", Versions: []metav1.GroupVersionForDiscovery{{
					GroupVersion: "networking.k8s.io/v1", Version: "v1",
				}}, PreferredVersion: metav1.GroupVersionForDiscovery{
					GroupVersion: "networking.k8s.io/v1", Version: "v1",
				},
			}}}))
		case "/api/v1":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIResourceList{GroupVersion: "v1"}))
		case "/apis/networking.k8s.io/v1":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIResourceList{
				GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{{
					Name: "ingresses", Kind: "Ingress", Namespaced: true,
				}},
			}))
		default:
			handle(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	return &config.Configuration{Server: &srv.URL, Namespace: new("ignored"),
		InsecureSkipTLSVerify: new(true)}
}

func TestIngressReadTracksPriorKeysAndLiveEndpoints(t *testing.T) {
	r := ingress()
	obj, err := r.expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	known := "app.kubernetes.io/deprecated.daemonset.template.generation"
	obj.Annotations = map[string]string{known: "new", "ordinary": "visible"}
	obj.Labels = map[string]string{known: "new", "ordinary": "visible"}
	obj.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
		{Hostname: "current"}, {}, {IP: "192.0.2.1"},
	}
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "GET", req.Method)
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := r.output(obj, *c.Server, r.Metadata)
	require.NoError(t, err)
	tracked := map[string]string{known: "old", "missing": "old"}
	out.Metadata.Annotations, out.Metadata.Labels = &tracked, &tracked
	r.IgnoreAnnotations, r.IgnoreLabels = new([]string{"daemonset"}), new([]string{"daemonset"})
	read, err := r.Read(t.Context(), c, ingressPrior{Inputs: *ingress(), Outputs: out})
	require.NoError(t, err)
	want := map[string]string{known: "new", "ordinary": "visible"}
	require.Equal(t, &want, read.Metadata.Annotations)
	require.Equal(t, &want, read.Metadata.Labels)
	encoded, err := ub.Marshal(read)
	require.NoError(t, err)
	var decoded IngressResourceOutput
	require.NoError(t, ub.Unmarshal(encoded, &decoded))
	require.Equal(t, []IngressEndpoint{{Hostname: "current"}, {}, {IP: "192.0.2.1"}},
		decoded.Status.LoadBalancer.Ingress)
	obj.Status.LoadBalancer.Ingress = nil
	obj.Spec = networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{}}}
	read, err = r.Read(t.Context(), c, ingressPrior{Outputs: out})
	require.NoError(t, err)
	require.Empty(t, read.Status.LoadBalancer.Ingress)
	require.Nil(t, (*read.Spec.Rule)[0].HTTP)
}

func TestIngressGeneratedIdentityUpdate(t *testing.T) {
	r := ingress()
	r.Metadata.Name, r.Metadata.GenerateName = nil, new("example-")
	obj, err := r.expand(nil)
	require.NoError(t, err)
	obj.Name, obj.UID = "example-generated", "uid"
	var methods []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		require.Equal(t, "/apis/networking.k8s.io/v1/namespaces/default/ingresses/example-generated",
			req.URL.Path)
		if req.Method == "PUT" {
			var body networkingv1.Ingress
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, "example-generated", body.Name)
			require.Equal(t, "example-", body.GenerateName)
			require.Equal(t, "uid", string(body.UID))
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := r.output(obj, *c.Server, r.Metadata)
	require.NoError(t, err)
	p := ingressPrior{Inputs: *r, Outputs: out}
	r.WaitForLoadBalancer = new(true)
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "PUT", "GET"}, methods)
}

func TestIngressFullUpdateAndRefresh(t *testing.T) {
	for _, change := range []string{"metadata", "spec", "wait", "status", "computed class", "controls",
		"writable drift", "clear", "explicit class", "empty class"} {
		t.Run(change, func(t *testing.T) {
			r := ingress()
			r.Spec.TLS = new([]IngressTLS{{Hosts: new([]string{"old"})}})
			r.Spec.DefaultBackend = &IngressBackend{Resource: &IngressResourceReference{
				APIGroup: "example.com", Kind: "Bucket", Name: "old",
			}}
			stored, err := r.expand(nil)
			require.NoError(t, err)
			stored.UID = "uid"
			stored.Spec.IngressClassName = new("assigned")
			var methods []string
			var rawPUT map[string]any
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
				require.Equal(t, "/apis/networking.k8s.io/v1/namespaces/default/ingresses/example",
					req.URL.Path)
				if req.Method == "PUT" {
					require.Empty(t, req.URL.RawQuery)
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, &rawPUT))
					require.NotContains(t, rawPUT, "status")
					metadata := rawPUT["metadata"].(map[string]any)
					require.Equal(t, "uid", metadata["uid"])
					for _, field := range []string{"resourceVersion", "ownerReferences", "finalizers"} {
						require.NotContains(t, metadata, field)
					}
					stored = &networkingv1.Ingress{}
					require.NoError(t, json.Unmarshal(body, stored))
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			out, err := r.output(stored, *c.Server, r.Metadata)
			require.NoError(t, err)
			p := ingressPrior{Inputs: *r, Outputs: out, Configuration: c}
			stored.ResourceVersion = "volatile"
			stored.Finalizers = []string{"not-merged"}
			stored.Annotations = map[string]string{"kubernetes.io/internal": "not-merged"}
			switch change {
			case "metadata":
				r.Metadata.Labels = new(map[string]string{"desired": "only"})
			case "spec":
				r.Spec.TLS = new([]IngressTLS{{SecretName: new("new")}})
			case "wait":
				r.WaitForLoadBalancer = new(true)
			case "status":
				stored.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
					{IP: "192.0.2.1"}, {Hostname: "current"}, {},
				}
			case "computed class":
				stored.Spec.IngressClassName = new("changed")
			case "controls":
				r.CreateTimeout = new("1s")
				r.IgnoreLabels = new([]string{"hidden"})
			case "writable drift":
				stored.Spec.TLS = nil
			case "clear":
				r.Spec = IngressSpec{}
			case "explicit class":
				r.Spec.IngressClassName = new("changed")
			case "empty class":
				r.Spec.IngressClassName = new("")
			}
			current := *c
			current.Server = new("https://unused.invalid")
			current.Token = new("current")
			updated, err := r.Update(t.Context(), &current, p)
			require.NoError(t, err)
			if change == "status" || change == "computed class" || change == "controls" {
				require.Equal(t, []string{"GET"}, methods)
				if change == "status" {
					require.Equal(t, []IngressEndpoint{{IP: "192.0.2.1"}, {Hostname: "current"}, {}},
						updated.Status.LoadBalancer.Ingress)
				} else {
					require.Empty(t, updated.Status.LoadBalancer.Ingress)
				}
			} else {
				require.Equal(t, []string{"GET", "PUT", "GET"}, methods)
				require.NotNil(t, rawPUT)
				if change == "empty class" {
					require.Nil(t, stored.Spec.IngressClassName)
				} else {
					want := "assigned"
					if change == "explicit class" {
						want = "changed"
					}
					require.Equal(t, &want, stored.Spec.IngressClassName)
				}
				if change == "clear" {
					require.Nil(t, stored.Spec.DefaultBackend)
					require.Empty(t, stored.Spec.Rules)
					require.Empty(t, stored.Spec.TLS)
				}
			}
		})
	}
}

func TestIngressAPIValidationRequests(t *testing.T) {
	for _, mode := range []string{"both backends", "neither backend", "both ports", "neither port",
		"bad port", "bad host and path", "bad namespace", "empty namespace"} {
		t.Run(mode, func(t *testing.T) {
			r := ingress()
			path := &(*r.Spec.Rule)[0].HTTP.Path[0]
			switch mode {
			case "both backends":
				path.Backend.Resource = &IngressResourceReference{
					APIGroup: "", Kind: "ConfigMap", Name: "settings",
				}
			case "neither backend":
				path.Backend = nil
			case "both ports":
				path.Backend.Service.Port.Name = new("http")
			case "neither port":
				path.Backend.Service.Port = IngressServicePort{}
			case "bad port":
				path.Backend.Service.Port.Number = new(int64(-1))
			case "bad host and path":
				(*r.Spec.Rule)[0].Host = new("UPPER.Invalid")
				path.Path, path.PathType = new("relative"), new("Exact")
			case "bad namespace":
				r.Metadata.Namespace = new("INVALID NAMESPACE")
			case "empty namespace":
				r.Metadata.Namespace = new("")
			}
			calls := 0
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, "POST", req.Method)
				var obj networkingv1.Ingress
				require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
				p := obj.Spec.Rules[0].HTTP.Paths[0]
				switch mode {
				case "both backends":
					require.NotNil(t, p.Backend.Resource)
					require.NotNil(t, p.Backend.Service)
					require.Equal(t, new(""), p.Backend.Resource.APIGroup)
				case "neither backend":
					require.Equal(t, networkingv1.IngressBackend{}, p.Backend)
				case "both ports":
					require.Equal(t, networkingv1.ServiceBackendPort{
						Name: "http", Number: 80,
					}, p.Backend.Service.Port)
				case "neither port":
					require.Equal(t, networkingv1.ServiceBackendPort{}, p.Backend.Service.Port)
				case "bad port":
					require.Equal(t, int32(-1), p.Backend.Service.Port.Number)
				case "bad host and path":
					require.Equal(t, "UPPER.Invalid", obj.Spec.Rules[0].Host)
					require.Equal(t, "relative", p.Path)
				case "bad namespace":
					require.Equal(t, "INVALID NAMESPACE", obj.Namespace)
				case "empty namespace":
					require.Empty(t, obj.Namespace)
					require.Equal(t, "/apis/networking.k8s.io/v1/ingresses", req.URL.Path)
				}
				apiError(t, w, 422)
			})
			_, err := r.Create(t.Context(), c)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestIngressMetadataClearingAndBackendSwitch(t *testing.T) {
	r := ingress()
	r.Metadata.Annotations = new(map[string]string{"remove": "old"})
	r.Metadata.Labels = new(map[string]string{"remove": "old"})
	r.Spec.DefaultBackend = &IngressBackend{Resource: &IngressResourceReference{
		APIGroup: "example.com", Kind: "Bucket", Name: "old",
	}}
	stored, err := r.expand(nil)
	require.NoError(t, err)
	stored.UID = "uid"
	var bodies []*networkingv1.Ingress
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PUT" {
			stored = &networkingv1.Ingress{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			bodies = append(bodies, stored.DeepCopy())
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	out, err := r.output(stored, *c.Server, r.Metadata)
	require.NoError(t, err)
	p := ingressPrior{Inputs: *r, Outputs: out}
	r.Metadata.Annotations = new(map[string]string{})
	r.Metadata.Labels = nil
	r.Spec.DefaultBackend = &IngressBackend{Service: &IngressServiceBackend{
		Name: "app", Port: IngressServicePort{Name: new("http")},
	}}
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Empty(t, bodies[0].Annotations)
	require.Empty(t, bodies[0].Labels)
	require.Nil(t, bodies[0].Spec.DefaultBackend.Resource)
	require.Equal(t, "http", bodies[0].Spec.DefaultBackend.Service.Port.Name)
	p = ingressPrior{Inputs: *r, Outputs: out}
	r.Spec.DefaultBackend = &IngressBackend{Resource: &IngressResourceReference{
		APIGroup: "example.com", Kind: "Bucket", Name: "new",
	}}
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Nil(t, bodies[1].Spec.DefaultBackend.Service)
	require.Equal(t, "new", bodies[1].Spec.DefaultBackend.Resource.Name)
}

func TestIngressCreationWaitTimeout(t *testing.T) {
	r := ingress()
	r.WaitForLoadBalancer = new(true)
	r.CreateTimeout = new("30ms")
	obj, err := r.expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	var methods []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := r.Create(t.Context(), c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, []string{"POST", "GET"}, methods)
}

func TestIngressCreateWaitAndFinalRead(t *testing.T) {
	for _, kind := range []string{"omitted", "false", "true", "final disappearance", "final error"} {
		t.Run(kind, func(t *testing.T) {
			r := ingress()
			r.Metadata.Name = nil
			r.Metadata.GenerateName = new("example-")
			if kind != "omitted" {
				r.WaitForLoadBalancer = new(kind != "false")
			}
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.Name, obj.UID = "example-generated", "uid"
			var methods []string
			gets := 0
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				if req.Method == "GET" {
					require.Equal(t, "/apis/networking.k8s.io/v1/namespaces/default/ingresses/example-generated",
						req.URL.Path)
					gets++
					if kind != "omitted" && kind != "false" {
						switch gets {
						case 1:
							apiError(t, w, 404)
							return
						case 2:
							obj.Status.LoadBalancer.Ingress = nil
						case 3:
							obj.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{}}
						case 4:
							if kind == "final error" {
								apiError(t, w, 403)
								return
							}
							obj.Spec.IngressClassName = new("final")
							obj.Status.LoadBalancer.Ingress = nil
							if kind != "final disappearance" {
								obj.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
									{Hostname: "final"}, {IP: "192.0.2.1"}, {},
								}
							}
						}
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := r.Create(t.Context(), c)
			if kind == "final error" {
				require.Error(t, err)
				require.Nil(t, out)
				return
			}
			require.NoError(t, err)
			if kind == "omitted" || kind == "false" {
				require.Equal(t, []string{"POST", "GET"}, methods)
			} else {
				require.Equal(t, []string{"POST", "GET", "GET", "GET", "GET"}, methods)
				require.Equal(t, new("final"), out.Spec.IngressClassName)
				if kind == "final disappearance" {
					require.Empty(t, out.Status.LoadBalancer.Ingress)
				} else {
					require.Equal(t, []IngressEndpoint{{Hostname: "final"}, {IP: "192.0.2.1"}, {}},
						out.Status.LoadBalancer.Ingress)
				}
			}
		})
	}
}

func TestIngressErrorsAndUID(t *testing.T) {
	for _, method := range []string{"POST", "GET", "PUT", "DELETE", "wait", "final"} {
		for _, code := range []int{403, 404, 409, 422, 500} {
			if method == "wait" && code == 404 {
				continue
			}
			t.Run(method+http.StatusText(code), func(t *testing.T) {
				r := ingress()
				obj, err := r.expand(nil)
				require.NoError(t, err)
				obj.UID = "uid"
				var methods []string
				c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
					methods = append(methods, req.Method)
					if req.Method == method || method == "wait" && req.Method == "GET" ||
						method == "final" && req.Method == "GET" {
						apiError(t, w, code)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				out, err := r.output(obj, *c.Server, r.Metadata)
				require.NoError(t, err)
				p := ingressPrior{Inputs: *r, Outputs: out}
				switch method {
				case "POST", "final":
					_, err = r.Create(t.Context(), c)
				case "wait":
					r.WaitForLoadBalancer = new(true)
					_, err = r.Create(t.Context(), c)
				case "GET":
					_, err = r.Read(t.Context(), c, p)
				case "PUT":
					r.WaitForLoadBalancer = new(true)
					_, err = r.Update(t.Context(), c, p)
				case "DELETE":
					err = r.Delete(t.Context(), c, p)
				}
				if method == "DELETE" && code == 404 {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					if method == "GET" && code == 404 || method == "final" && code == 404 {
						require.ErrorIs(t, err, runtime.ErrNotFound)
					} else {
						var status apierrors.APIStatus
						require.ErrorAs(t, err, &status)
						require.Equal(t, int32(code), status.Status().Code)
					}
				}
				want := []string{method}
				if method == "wait" || method == "final" {
					want = []string{"POST", "GET"}
				}
				if method == "PUT" {
					want = []string{"GET", "PUT"}
				}
				require.Equal(t, want, methods)
			})
		}
	}
	for _, race := range []bool{false, true} {
		t.Run("uid", func(t *testing.T) {
			r := ingress()
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			var methods []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				if req.Method == "PUT" {
					var body networkingv1.Ingress
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, "uid", string(body.UID))
					require.Equal(t, "replacement", string(obj.UID))
					apiError(t, w, 409)
					return
				}
				before := obj.DeepCopy()
				if race {
					obj.UID = "replacement"
				}
				require.NoError(t, json.NewEncoder(w).Encode(before))
			})
			out, err := r.output(obj, *c.Server, r.Metadata)
			require.NoError(t, err)
			p := ingressPrior{Inputs: *r, Outputs: out}
			if !race {
				obj.UID = "replacement"
			}
			r.WaitForLoadBalancer = new(true)
			_, err = r.Update(t.Context(), c, p)
			require.Error(t, err)
			want := []string{"GET"}
			if race {
				want = append(want, "PUT")
			}
			require.Equal(t, want, methods)
		})
	}
}

func TestIngressDeleteWait(t *testing.T) {
	for _, mode := range []string{"removed", "replacement", "forbidden", "server error", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			r := ingress()
			r.DeleteTimeout = new("600ms")
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			obj.APIVersion, obj.Kind = "networking.k8s.io/v1", "Ingress"
			var methods []string
			gets := 0
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				if req.Method == "DELETE" {
					var options metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
					require.Equal(t, "uid", string(*options.Preconditions.UID))
					require.Nil(t, options.PropagationPolicy)
					require.Nil(t, options.GracePeriodSeconds)
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
					return
				}
				gets++
				if gets > 1 {
					switch mode {
					case "removed":
						apiError(t, w, 404)
						return
					case "replacement":
						obj.UID = "replacement"
					case "forbidden":
						apiError(t, w, 403)
						return
					case "server error":
						apiError(t, w, 500)
						return
					}
				}
				obj.Finalizers = []string{"blocked"}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := r.output(obj, *c.Server, r.Metadata)
			require.NoError(t, err)
			err = r.Delete(t.Context(), c, ingressPrior{Inputs: *r, Outputs: out})
			if mode == "removed" || mode == "replacement" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, "DELETE", methods[0])
			for _, m := range methods[1:] {
				require.Equal(t, "GET", m)
			}
		})
	}
}

func TestIngressTimeoutCancellationAndValidation(t *testing.T) {
	for _, phase := range []string{"create", "read", "update", "delete", "wait"} {
		t.Run(phase, func(t *testing.T) {
			r := ingress()
			r.CreateTimeout, r.DeleteTimeout = new("30ms"), new("30ms")
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			release := make(chan struct{})
			defer close(release)
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if phase == "wait" && req.Method == "POST" {
					require.NoError(t, json.NewEncoder(w).Encode(obj))
					return
				}
				select {
				case <-req.Context().Done():
				case <-release:
				}
			})
			out, err := r.output(obj, *c.Server, r.Metadata)
			require.NoError(t, err)
			p := ingressPrior{Inputs: *r, Outputs: out}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			switch phase {
			case "create":
				_, err = r.Create(ctx, c)
			case "read":
				_, err = r.Read(ctx, c, p)
			case "update":
				_, err = r.Update(ctx, c, p)
			case "delete":
				err = r.Delete(ctx, c, p)
			case "wait":
				r.WaitForLoadBalancer = new(true)
				_, err = r.Create(ctx, c)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("invalid operation reached api: %s", req.Method)
	})
	r := ingress()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Update(t.Context(), c, ingressPrior{})
	require.Error(t, err)
	_, err = r.Read(t.Context(), c, ingressPrior{})
	require.Error(t, err)
	require.Error(t, r.Delete(t.Context(), c, ingressPrior{}))
}

func apiError(t *testing.T, w http.ResponseWriter, code int) {
	t.Helper()
	reason := metav1.StatusReasonForbidden
	switch code {
	case 404:
		reason = metav1.StatusReasonNotFound
	case 409:
		reason = metav1.StatusReasonConflict
	case 422:
		reason = metav1.StatusReasonInvalid
	case 500:
		reason = metav1.StatusReasonInternalError
	}
	w.WriteHeader(code)
	require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   "Failure", Reason: reason, Code: int32(code), Message: "api diagnostic",
	}))
}

func TestIngressLifecycle(t *testing.T) {
	var stored networkingv1.Ingress
	var methods []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		require.Empty(t, req.URL.RawQuery)
		if req.Method == "POST" {
			require.Equal(t, "/apis/networking.k8s.io/v1/namespaces/default/ingresses", req.URL.Path)
			require.NoError(t, json.NewDecoder(req.Body).Decode(&stored))
			stored.UID = "uid"
			stored.Spec.IngressClassName = new("assigned")
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	r := ingress()
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, methods)
	require.Equal(t, "uid", out.UID)
	require.Equal(t, "assigned", *out.Spec.IngressClassName)
	require.Empty(t, out.Status.LoadBalancer.Ingress)
}
