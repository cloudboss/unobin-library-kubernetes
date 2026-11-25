package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

//nolint:staticcheck // Test responses use the core/v1 Endpoints API.
type kubeEndpoints = corev1.Endpoints

const endpointsLookupGet = "GET /api/v1/namespaces/default/endpoints/lookup.test"

func TestEndpointsLookupRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace *string
		path      string
		wantError string
	}{
		{name: "default", path: endpointsLookupGet},
		{name: "custom", namespace: new("tenant"),
			path: "GET /api/v1/namespaces/tenant/endpoints/lookup.test"},
		{name: "unscoped", namespace: new(""), path: "GET /api/v1/endpoints/lookup.test"},
		{name: "invalid namespace", namespace: new("bad/path"),
			wantError: "invalid namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 10)
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				respond(t, w, &kubeEndpoints{ObjectMeta: metav1.ObjectMeta{
					Name: "server-name", Namespace: "server-ns", UID: "existing",
				}})
			})
			lookup := EndpointsDataSource{
				Metadata: EndpointsLookupMetadata{
					Name: new("lookup.test"), Namespace: tc.namespace, GenerateName: new("prefix-"),
					Labels:      &map[string]*string{"label": new("configured")},
					Annotations: &map[string]*string{"annotation": new("configured")},
				},
				Subset: &[]EndpointsLookupSubset{{Port: &[]EndpointsLookupPort{{Port: 42}}}},
			}
			out, err := lookup.Read(t.Context(), c)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, out)
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, &EndpointsDataSourceOutput{
				Metadata: EndpointsLookupMetadataOutput{
					Name: "server-name", Namespace: "server-ns", UID: "existing",
					Labels: map[string]string{}, Annotations: map[string]string{},
				},
				Subset: []EndpointsLookupSubsetOutput{},
			}, out)
			podInfoAssertRequests(t, requests, tc.path)
		})
	}
}

func TestEndpointsLookupWithoutName(t *testing.T) {
	requests := make(chan string, 10)
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests <- req.Method + " " + req.URL.RequestURI()
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, metadata := range []EndpointsLookupMetadata{{}, {GenerateName: new("only-prefix-")}} {
		lookup := EndpointsDataSource{Metadata: metadata}
		out, err := lookup.Read(t.Context(), c)
		require.ErrorContains(t, err, "get Endpoints default/: resource name may not be empty")
		require.Nil(t, out)
		require.Empty(t, requests)
	}
}

func TestEndpointsLookupLiveThenMissing(t *testing.T) {
	requests := make(chan string, 10)
	var missing atomic.Bool
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests <- req.Method + " " + req.URL.RequestURI()
		if missing.Load() {
			err := apierrors.NewNotFound(corev1.Resource("endpoints"), "lookup.test")
			w.WriteHeader(http.StatusNotFound)
			respond(t, w, &err.ErrStatus)
			return
		}
		respond(t, w, endpointsLookupLiveObject())
	})
	lookup := EndpointsDataSource{
		Metadata: EndpointsLookupMetadata{
			Name: new("lookup.test"), GenerateName: new("initial-"),
			Annotations: &map[string]*string{"annotation": new("initial")},
			Labels:      &map[string]*string{"label": new("initial")},
		},
		Subset: &[]EndpointsLookupSubset{{Port: &[]EndpointsLookupPort{{Port: 42}}}},
	}
	out, err := lookup.Read(t.Context(), c)
	require.NoError(t, err)
	want := endpointsLookupLiveOutput()
	require.Equal(t, want, out)

	missing.Store(true)
	lookup.Metadata.GenerateName = new("current-")
	lookup.Metadata.Annotations = &map[string]*string{"annotation": new("current"), "bad key": nil}
	lookup.Metadata.Labels = &map[string]*string{"label": new(""), "bad key": nil}
	ports := []EndpointsLookupPort{
		{Name: new("odd name"), Port: 1 << 40, Protocol: new("unvalidated value")},
		{Port: -1, Protocol: new("")}, {Port: 0}, {Port: -1, Protocol: new("")},
	}
	addr := []EndpointsLookupAddress{
		{IP: "not parsed", Hostname: new("unvalidated host"), NodeName: new("unvalidated node")},
	}
	lookup.Subset = &[]EndpointsLookupSubset{
		{}, {Address: &addr, NotReadyAddress: &addr, Port: &ports},
	}
	fallback, err := lookup.Read(t.Context(), c)
	require.NoError(t, err)
	wantAddress := []EndpointsLookupAddressOutput{
		{IP: "not parsed", Hostname: "unvalidated host", NodeName: "unvalidated node"},
	}
	require.Equal(t, &EndpointsDataSourceOutput{
		Metadata: EndpointsLookupMetadataOutput{
			Name: "lookup.test", Namespace: "default", GenerateName: "current-",
			Annotations: map[string]string{"annotation": "current"},
			Labels:      map[string]string{"label": ""},
		},
		Subset: []EndpointsLookupSubsetOutput{{
			Address: wantAddress, NotReadyAddress: wantAddress,
			Port: []EndpointsLookupPortOutput{
				{Port: -1}, {Port: 0, Protocol: "TCP"},
				{Name: "odd name", Port: 1 << 40, Protocol: "unvalidated value"},
			},
		}},
	}, fallback)
	require.Equal(t, want, out)
	podInfoAssertRequests(t, requests, endpointsLookupGet, endpointsLookupGet)
}

func endpointsLookupLiveObject() *kubeEndpoints {
	ready := kubeEndpointAddress{
		IP: "2001:0DB8::1", Hostname: "primary", NodeName: new("node-z"),
		TargetRef: &corev1.ObjectReference{Name: "omitted"},
	}
	readyAgain := ready
	readyAgain.TargetRef = &corev1.ObjectReference{Name: "also-omitted"}
	port := kubeEndpointPort{Name: "api", Port: 100, Protocol: corev1.ProtocolSCTP}
	portAgain := port
	portAgain.AppProtocol = new("example.com/omitted")
	return &kubeEndpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name: "live", Namespace: "remote", GenerateName: "server-", UID: "abc",
			ResourceVersion: "7", Generation: 2,
			Annotations: map[string]string{"control-plane.alpha.kubernetes.io/leader": "remote"},
			Labels:      map[string]string{"label": "remote"},
		},
		Subsets: []kubeEndpointSubset{
			{Addresses: []kubeEndpointAddress{readyAgain, ready, {IP: "192.0.2.1"}}},
			{},
			{
				Addresses:         []kubeEndpointAddress{ready},
				NotReadyAddresses: []kubeEndpointAddress{readyAgain, ready, {IP: "192.0.2.2"}},
				Ports: []kubeEndpointPort{
					port, {Name: "api", Port: 20, Protocol: corev1.ProtocolUDP},
					{Port: 10}, portAgain,
				},
			},
			{Addresses: []kubeEndpointAddress{{IP: "192.0.2.1"}, ready}},
		},
	}
}

func endpointsLookupLiveOutput() *EndpointsDataSourceOutput {
	ready := EndpointsLookupAddressOutput{IP: "2001:0DB8::1", Hostname: "primary", NodeName: "node-z"}
	return &EndpointsDataSourceOutput{
		Metadata: EndpointsLookupMetadataOutput{
			Name: "live", Namespace: "remote", GenerateName: "server-", UID: "abc",
			ResourceVersion: "7", Generation: 2,
			Annotations: map[string]string{"control-plane.alpha.kubernetes.io/leader": "remote"},
			Labels:      map[string]string{"label": "remote"},
		},
		Subset: []EndpointsLookupSubsetOutput{
			{
				Address:         []EndpointsLookupAddressOutput{{IP: "192.0.2.1"}, ready},
				NotReadyAddress: []EndpointsLookupAddressOutput{},
				Port:            []EndpointsLookupPortOutput{},
			},
			{
				Address:         []EndpointsLookupAddressOutput{ready},
				NotReadyAddress: []EndpointsLookupAddressOutput{{IP: "192.0.2.2"}, ready},
				Port: []EndpointsLookupPortOutput{
					{Port: 10}, {Name: "api", Port: 20, Protocol: "UDP"},
					{Name: "api", Port: 100, Protocol: "SCTP"},
				},
			},
		},
	}
}

func TestEndpointsLookupEmptySubsets(t *testing.T) {
	for _, tc := range []struct {
		name       string
		live       []kubeEndpointSubset
		configured *[]EndpointsLookupSubset
	}{
		{name: "absent"},
		{name: "empty", live: []kubeEndpointSubset{}, configured: &[]EndpointsLookupSubset{}},
		{name: "all-empty", live: []kubeEndpointSubset{{}}, configured: &[]EndpointsLookupSubset{
			{Address: &[]EndpointsLookupAddress{}, Port: &[]EndpointsLookupPort{}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 10)
			var missing atomic.Bool
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				if missing.Load() {
					err := apierrors.NewNotFound(corev1.Resource("endpoints"), "lookup.test")
					w.WriteHeader(http.StatusNotFound)
					respond(t, w, &err.ErrStatus)
					return
				}
				body := map[string]any{
					"metadata": metav1.ObjectMeta{Name: "live", UID: "uid"},
				}
				if tc.live != nil {
					body["subsets"] = tc.live
				}
				respond(t, w, body)
			})
			lookup := EndpointsDataSource{
				Metadata: EndpointsLookupMetadata{Name: new("lookup.test")}, Subset: tc.configured,
			}
			out, err := lookup.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &EndpointsDataSourceOutput{
				Metadata: EndpointsLookupMetadataOutput{
					Name: "live", UID: "uid", Annotations: map[string]string{},
					Labels: map[string]string{},
				},
				Subset: []EndpointsLookupSubsetOutput{},
			}, out)
			missing.Store(true)
			out, err = lookup.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &EndpointsDataSourceOutput{
				Metadata: EndpointsLookupMetadataOutput{
					Name: "lookup.test", Namespace: "default", Annotations: map[string]string{},
					Labels: map[string]string{},
				},
				Subset: []EndpointsLookupSubsetOutput{},
			}, out)
			podInfoAssertRequests(t, requests, endpointsLookupGet, endpointsLookupGet)
		})
	}
}

func TestEndpointsLookupErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reason metav1.StatusReason
	}{
		{"forbidden", http.StatusForbidden, metav1.StatusReasonForbidden},
		{"api error", http.StatusInternalServerError, metav1.StatusReasonInternalError},
		{"malformed response", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 10)
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				w.WriteHeader(tc.status)
				if tc.reason == "" {
					_, _ = w.Write([]byte("{"))
					return
				}
				respond(t, w, &metav1.Status{Status: "Failure", Reason: tc.reason,
					Code: int32(tc.status)})
			})
			lookup := EndpointsDataSource{Metadata: EndpointsLookupMetadata{Name: new("lookup.test")}}
			out, err := lookup.Read(t.Context(), c)
			require.ErrorContains(t, err, "get Endpoints default/lookup.test:")
			require.Nil(t, out)
			if tc.reason != "" {
				require.Equal(t, tc.reason, apierrors.ReasonForError(err))
			}
			podInfoAssertRequests(t, requests, endpointsLookupGet)
		})
	}
	t.Run("configuration", func(t *testing.T) {
		requests := make(chan string, 10)
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			requests <- req.Method + " " + req.URL.RequestURI()
			w.WriteHeader(http.StatusInternalServerError)
		})
		c.CertificateAuthorityData = new("not base64")
		lookup := EndpointsDataSource{Metadata: EndpointsLookupMetadata{Name: new("lookup.test")}}
		out, err := lookup.Read(t.Context(), c)
		require.ErrorContains(t, err, "decode certificate-authority-data: invalid base64")
		require.Nil(t, out)
		require.Empty(t, requests)
	})
	t.Run("transport", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		lookup := EndpointsDataSource{Metadata: EndpointsLookupMetadata{Name: new("lookup.test")}}
		out, err := lookup.Read(t.Context(), &config.Configuration{Server: &server.URL})
		require.ErrorContains(t, err, "get Endpoints default/lookup.test:")
		require.ErrorContains(t, err, "connection refused")
		require.Nil(t, out)
	})
	t.Run("canceled caller", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		requests := make(chan string, 10)
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			requests <- req.Method + " " + req.URL.RequestURI()
			cancel()
			select {
			case <-req.Context().Done():
			case <-time.After(10 * time.Second):
				t.Error("request context did not cancel")
			}
		})
		lookup := EndpointsDataSource{Metadata: EndpointsLookupMetadata{Name: new("lookup.test")}}
		out, err := lookup.Read(ctx, c)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, out)
		podInfoAssertRequests(t, requests, endpointsLookupGet)
	})
}
