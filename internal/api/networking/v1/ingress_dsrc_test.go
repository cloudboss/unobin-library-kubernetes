package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestIngressInfoReads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace *string
		path      string
	}{
		{"default", nil, "/apis/networking.k8s.io/v1/namespaces/default/ingresses/generated-1"},
		{"explicit", new("team"),
			"/apis/networking.k8s.io/v1/namespaces/team/ingresses/generated-1"},
		{"empty", new(""), "/apis/networking.k8s.io/v1/ingresses/generated-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := IngressLookupMetadata{
				Name: new("generated-1"), Namespace: tc.namespace,
				Annotations: &map[string]*string{"EXAMPLE/Key": new("input")},
				Labels:      &map[string]*string{"app": new("input")},
			}
			obj := networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
				Name: "generated-1", GenerateName: "generated-", Namespace: "actual-ns",
				Annotations: map[string]string{
					"EXAMPLE/Key": "", "kubernetes.io/Internal": "visible",
				},
				Labels:     map[string]string{"app": "live", "kubernetes.io/internal": ""},
				Generation: 2, ResourceVersion: "3", UID: "uid",
			}}
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				if len(requests) > 1 {
					writeIngressInfoResponse(t, w, &networkingv1.Ingress{})
					return
				}
				writeIngressInfoResponse(t, w, obj)
			}))
			t.Cleanup(server.Close)
			source := IngressDataSource{Metadata: input}
			out, err := source.Read(t.Context(), &config.Configuration{
				Server: &server.URL, Namespace: new("configured"),
			})
			require.NoError(t, err)
			require.Equal(t, []string{"GET " + tc.path}, requests)
			require.Equal(t, &IngressDataSourceOutput{
				Metadata: IngressLookupMetadataOutput{
					Name: "generated-1", Namespace: "actual-ns",
					Annotations: obj.Annotations, Labels: obj.Labels,
					Generation: 2, ResourceVersion: "3", UID: "uid",
				},
				Spec: &IngressLookupSpec{Rule: []IngressLookupRule{}, TLS: []IngressLookupTLS{}},
				Status: &IngressStatus{
					LoadBalancer: IngressLoadBalancer{Ingress: []IngressEndpoint{}},
				},
			}, out)
			out, err = source.Read(t.Context(), &config.Configuration{
				Server: &server.URL, Namespace: new("configured"),
			})
			require.NoError(t, err)
			require.Equal(t, IngressLookupMetadataOutput{
				Annotations: map[string]string{}, Labels: map[string]string{},
			}, out.Metadata)
			require.Equal(t, []string{"GET " + tc.path, "GET " + tc.path}, requests)
		})
	}
}

func TestIngressInfoMissingAfterHit(t *testing.T) {
	const path = "GET /apis/networking.k8s.io/v1/namespaces/team/ingresses/current"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			writeIngressInfoResponse(t, w, &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
				Name: "previous", Namespace: "previous", Generation: 7,
				UID: "previous", ResourceVersion: "42",
				Labels: map[string]string{"old": "label"}, Annotations: map[string]string{"old": "ann"},
			}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		writeIngressInfoResponse(t, w, apierrors.NewNotFound(
			networkingv1.Resource("ingresses"), "current").ErrStatus)
	}))
	t.Cleanup(server.Close)
	source := IngressDataSource{Metadata: IngressLookupMetadata{
		Name: new("current"), Namespace: new("team"),
		Labels: &map[string]*string{"old-input": new("stale")},
	}}
	cfg := &config.Configuration{Server: &server.URL}
	hit, err := source.Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, "previous", hit.Metadata.Name)
	source.Metadata.Labels = &map[string]*string{"blank": new(""), "bad key": nil}
	source.Metadata.Annotations = &map[string]*string{
		"EXAMPLE.COM/Key": new("annotation"), "drop this": nil,
	}
	miss, err := source.Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, &IngressDataSourceOutput{Metadata: IngressLookupMetadataOutput{
		Name: "current", Namespace: "team",
		Labels:      map[string]string{"blank": ""},
		Annotations: map[string]string{"EXAMPLE.COM/Key": "annotation"},
	}}, miss)
	require.Equal(t, []string{path, path}, requests)
}

func TestIngressInfoProjectsRoutingAndStatus(t *testing.T) {
	obj := networkingv1.Ingress{
		Spec: networkingv1.IngressSpec{
			IngressClassName: new("assigned"),
			DefaultBackend: &networkingv1.IngressBackend{
				Resource: &corev1.TypedLocalObjectReference{Kind: "Pod", Name: "core"},
				Service: &networkingv1.IngressServiceBackend{
					Name: "dual", Port: networkingv1.ServiceBackendPort{Number: 443, Name: "https"},
				},
			},
			Rules: []networkingv1.IngressRule{
				{}, {Host: "without-paths.example", IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{},
				}},
				{IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
						{Path: "/repeat", PathType: new(networkingv1.PathTypeExact),
							Backend: networkingv1.IngressBackend{
								Resource: &corev1.TypedLocalObjectReference{
									APIGroup: new("example.net"), Kind: "Bucket", Name: "content",
								},
							}},
						{Path: "/prefix", PathType: new(networkingv1.PathTypePrefix),
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80},
								},
							}},
						{Path: "/repeat", PathType: new(networkingv1.PathTypeExact),
							Backend: networkingv1.IngressBackend{
								Resource: &corev1.TypedLocalObjectReference{
									APIGroup: new("example.net"), Kind: "Bucket", Name: "content",
								},
							}},
						{PathType: new(networkingv1.PathTypeImplementationSpecific),
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: "api", Port: networkingv1.ServiceBackendPort{Name: "web"},
								},
							}},
						{}, {PathType: new(networkingv1.PathType(""))},
						{PathType: new(networkingv1.PathType("Future"))},
					}},
				}},
			},
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{"b.example", "a.example", "b.example"}, SecretName: "tls"},
				{},
			},
		},
	}
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		writeIngressInfoResponse(t, w, obj)
	}))
	t.Cleanup(server.Close)
	lookup := IngressDataSource{Metadata: IngressLookupMetadata{Name: new("routes")}}
	cfg := &config.Configuration{Server: &server.URL}
	initial, err := lookup.Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, &IngressStatus{
		LoadBalancer: IngressLoadBalancer{Ingress: []IngressEndpoint{}},
	}, initial.Status)
	resource := &IngressResourceReference{APIGroup: "example.net", Kind: "Bucket", Name: "content"}
	repeated := IngressLookupPath{
		Path: "/repeat", PathType: new("Exact"), Backend: IngressBackend{Resource: resource},
	}
	wantSpec := &IngressLookupSpec{
		IngressClassName: new("assigned"),
		DefaultBackend: &IngressBackend{
			Resource: &IngressResourceReference{Kind: "Pod", Name: "core"},
			Service: &IngressServiceBackend{
				Name: "dual", Port: IngressServicePort{Number: new(int64(443)), Name: new("https")},
			},
		},
		Rule: []IngressLookupRule{
			{}, {Host: "without-paths.example", HTTP: &IngressLookupHTTP{Path: []IngressLookupPath{}}},
			{HTTP: &IngressLookupHTTP{Path: []IngressLookupPath{
				repeated,
				{Path: "/prefix", PathType: new("Prefix"), Backend: IngressBackend{
					Service: &IngressServiceBackend{
						Name: "api", Port: IngressServicePort{Number: new(int64(80))},
					},
				}},
				repeated,
				{PathType: new("ImplementationSpecific"), Backend: IngressBackend{
					Service: &IngressServiceBackend{
						Name: "api", Port: IngressServicePort{Name: new("web")},
					},
				}},
				{}, {PathType: new("")}, {PathType: new("Future")},
			}}},
		},
		TLS: []IngressLookupTLS{
			{Hosts: []string{"b.example", "a.example", "b.example"}, SecretName: "tls"},
			{Hosts: []string{}},
		},
	}
	require.Equal(t, wantSpec, initial.Spec)
	require.Equal(t, IngressLookupMetadataOutput{
		Annotations: map[string]string{}, Labels: map[string]string{},
	}, initial.Metadata)
	obj.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
		{IP: "192.0.2.1"}, {Hostname: "first.example"}, {}, {IP: "192.0.2.2"},
	}
	next, err := lookup.Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, wantSpec, next.Spec)
	require.Equal(t, &IngressStatus{LoadBalancer: IngressLoadBalancer{Ingress: []IngressEndpoint{
		{IP: "192.0.2.1"}, {Hostname: "first.example"}, {}, {IP: "192.0.2.2"},
	}}}, next.Status)
	obj.Status.LoadBalancer.Ingress[0], obj.Status.LoadBalancer.Ingress[3] =
		obj.Status.LoadBalancer.Ingress[3], obj.Status.LoadBalancer.Ingress[0]
	next, err = lookup.Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, &IngressStatus{LoadBalancer: IngressLoadBalancer{Ingress: []IngressEndpoint{
		{IP: "192.0.2.2"}, {Hostname: "first.example"}, {}, {IP: "192.0.2.1"},
	}}}, next.Status)
	const request = "GET /apis/networking.k8s.io/v1/namespaces/default/ingresses/routes"
	require.Equal(t, []string{request, request, request}, requests)
}

func TestIngressInfoReadFailures(t *testing.T) {
	const request = "GET /apis/networking.k8s.io/v1/namespaces/default/ingresses/route"
	for _, tc := range []struct {
		name   string
		status int
		body   any
		check  func(error) bool
	}{
		{"forbidden", http.StatusForbidden, apierrors.NewForbidden(
			networkingv1.Resource("ingresses"), "route", errors.New("denied")).ErrStatus,
			apierrors.IsForbidden},
		{"server error", http.StatusInternalServerError,
			apierrors.NewInternalError(errors.New("failed")).ErrStatus, apierrors.IsInternalError},
		{"malformed response", http.StatusOK, []byte("{"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if raw, ok := tc.body.([]byte); ok {
					_, err := w.Write(raw)
					assert.NoError(t, err)
					return
				}
				writeIngressInfoResponse(t, w, tc.body)
			}))
			t.Cleanup(server.Close)
			lookup := IngressDataSource{Metadata: IngressLookupMetadata{Name: new("route")}}
			out, err := lookup.Read(t.Context(), &config.Configuration{Server: &server.URL})
			require.ErrorContains(t, err, "get Ingress default/route")
			require.Nil(t, out)
			if tc.check != nil {
				require.True(t, tc.check(err), "%v", err)
			}
			require.Equal(t, []string{request}, requests)
		})
	}
	t.Run("configuration, name, and transport", func(t *testing.T) {
		var requests []string
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			requests = append(requests, r.Method+" "+r.URL.RequestURI())
		}))
		t.Cleanup(server.Close)
		cfg := &config.Configuration{Server: &server.URL}
		lookup := IngressDataSource{}
		out, err := lookup.Read(t.Context(), cfg)
		require.ErrorContains(t, err, "resource name may not be empty")
		require.Nil(t, out)
		require.Empty(t, requests)

		lookup.Metadata.Name = new("route")
		out, err = lookup.Read(t.Context(), &config.Configuration{Server: new("http://[")})
		require.ErrorContains(t, err, "get Ingress default/route")
		require.Nil(t, out)
		require.ErrorContains(t, err, "create Kubernetes client")
		require.Empty(t, requests)

		server.Close()
		var urlErr *url.Error
		out, err = lookup.Read(t.Context(), cfg)
		require.ErrorContains(t, err, "get Ingress default/route")
		require.ErrorAs(t, err, &urlErr)
		require.Nil(t, out)
		require.Empty(t, requests)
	})

	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		requests := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			requests <- r.Method + " " + r.URL.RequestURI()
			cancel()
			<-r.Context().Done()
		}))
		t.Cleanup(server.Close)
		lookup := IngressDataSource{Metadata: IngressLookupMetadata{Name: new("route")}}
		out, err := lookup.Read(ctx, &config.Configuration{Server: &server.URL})
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, out)
		close(requests)
		var actual []string
		for req := range requests {
			actual = append(actual, req)
		}
		require.Equal(t, []string{request}, actual)
	})

	t.Run("earlier caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		requests := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			requests <- r.Method + " " + r.URL.RequestURI()
			<-r.Context().Done()
		}))
		t.Cleanup(server.Close)
		lookup := IngressDataSource{Metadata: IngressLookupMetadata{Name: new("route")}}
		out, err := lookup.Read(ctx, &config.Configuration{Server: &server.URL})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, out)
		server.Close()
		close(requests)
		var actual []string
		for req := range requests {
			actual = append(actual, req)
		}
		require.Equal(t, []string{request}, actual)
	})
}

func writeIngressInfoResponse(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	assert.NoError(t, json.NewEncoder(w).Encode(body))
}
