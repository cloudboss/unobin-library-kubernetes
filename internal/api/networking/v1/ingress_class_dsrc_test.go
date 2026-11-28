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
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestIngressClassLookupRead(t *testing.T) {
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		require.NoError(t, json.NewEncoder(w).Encode(&networkingv1.IngressClass{
			ObjectMeta: metav1.ObjectMeta{Name: "returned",
				DeletionTimestamp: new(metav1.Now())},
		}))
	})
	out, err := (&IngressClassDataSource{Metadata: IngressClassLookupMetadata{Name: "example"}}).
		Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &IngressClassDataSourceOutput{
		Metadata: IngressClassLookupMetadataOutput{
			Name: "returned", Labels: map[string]string{}, Annotations: map[string]string{},
		}, Spec: &IngressClassLookupSpec{},
	}, out)
	require.Equal(t, []string{
		"GET /apis/networking.k8s.io/v1/ingressclasses/example ",
	}, calls)
}

func TestIngressClassLookupErrors(t *testing.T) {
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
				reason := metav1.StatusReasonForbidden
				if code == 500 {
					reason = metav1.StatusReasonInternalError
				}
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					Code: int32(code), Reason: reason,
				}))
			})
			out, err := (&IngressClassDataSource{Metadata: IngressClassLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get IngressClass example")
			if code != 200 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
				require.Equal(t, int32(code), status.Status().Code)
				if code == 403 {
					require.True(t, apierrors.IsForbidden(err))
				} else {
					require.True(t, apierrors.IsInternalError(err))
				}
			}
			require.Equal(t, []string{"GET"}, calls)
		})
	}
	r := &IngressClassDataSource{Metadata: IngressClassLookupMetadata{Name: "example"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := r.Read(ctx, nil)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	out, err = r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("invalid!")})
	require.Nil(t, out)
	require.ErrorContains(t, err, "get IngressClass example")
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

func TestIngressClassLookupCallerDeadline(t *testing.T) {
	c := testAPI(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	out, err := (&IngressClassDataSource{Metadata: IngressClassLookupMetadata{Name: "example"}}).
		Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestIngressClassLookupTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	c := &config.Configuration{Server: new(server.URL)}
	out, err := (&IngressClassDataSource{Metadata: IngressClassLookupMetadata{Name: "example"}}).
		Read(t.Context(), c)
	require.Nil(t, out)
	require.ErrorContains(t, err, "get IngressClass example")
	var transport *url.Error
	require.ErrorAs(t, err, &transport)
}

func TestIngressClassLookupMissingAfterSuccess(t *testing.T) {
	present := true
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if present {
			require.NoError(t, json.NewEncoder(w).Encode(&networkingv1.IngressClass{
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
	r := &IngressClassDataSource{Metadata: IngressClassLookupMetadata{Name: "example"}}
	out, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "old", out.Metadata.UID)
	present = false
	out, err = r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &IngressClassDataSourceOutput{Metadata: IngressClassLookupMetadataOutput{
		Name:   "example",
		Labels: map[string]string{}, Annotations: map[string]string{},
	}}, out)
	get := "GET /apis/networking.k8s.io/v1/ingressclasses/example"
	require.Equal(t, []string{get, get}, calls)
}

func TestIngressClassLookupProjection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		controller string
		parameters *networkingv1.IngressClassParametersReference
		want       *IngressClassLookupParameters
	}{
		{name: "absent"},
		{name: "present-empty", parameters: &networkingv1.IngressClassParametersReference{},
			want: &IngressClassLookupParameters{}},
		{name: "explicit-empty", parameters: &networkingv1.IngressClassParametersReference{
			APIGroup: new(""), Scope: new(""), Namespace: new(""),
		}, want: &IngressClassLookupParameters{
			APIGroup: new(""), Scope: new(""), Namespace: new(""),
		}},
		{name: "cluster", controller: "example.com/controller",
			parameters: &networkingv1.IngressClassParametersReference{
				Kind: "ConfigMap", Name: "config", Scope: new("Cluster"), Namespace: new("unexpected"),
			}, want: &IngressClassLookupParameters{
				Kind: "ConfigMap", Name: "config", Scope: new("Cluster"), Namespace: new("unexpected"),
			}},
		{name: "namespace", parameters: &networkingv1.IngressClassParametersReference{
			APIGroup: new("example.com"), Kind: "Parameters", Name: "missing", Scope: new("Namespace"),
		}, want: &IngressClassLookupParameters{
			APIGroup: new("example.com"), Kind: "Parameters", Name: "missing", Scope: new("Namespace"),
		}},
		{name: "unknown", controller: "Unfamiliar/Controller",
			parameters: &networkingv1.IngressClassParametersReference{
				APIGroup: new("Unknown"), Kind: "", Name: "", Scope: new("Unknown"), Namespace: new("ns"),
			}, want: &IngressClassLookupParameters{
				APIGroup: new("Unknown"), Kind: "", Name: "", Scope: new("Unknown"), Namespace: new("ns"),
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &networkingv1.IngressClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "returned", GenerateName: "prefix-", UID: "live", ResourceVersion: "12",
					Generation: 7, DeletionTimestamp: new(metav1.Now()),
					Labels: map[string]string{"controller.kubernetes.io/label": "retained"},
					Annotations: map[string]string{
						"kubectl.kubernetes.io/last-applied-configuration": "internal",
						"ingressclass.kubernetes.io/is-default-class":      "true",
					},
				},
				Spec: networkingv1.IngressClassSpec{
					Controller: tc.controller, Parameters: tc.parameters,
				},
			}
			before := obj.DeepCopy()
			want := &IngressClassDataSourceOutput{
				Metadata: IngressClassLookupMetadataOutput{
					Name: "returned", GenerateName: "prefix-", UID: "live", ResourceVersion: "12",
					Generation: 7,
					Labels:     map[string]string{"controller.kubernetes.io/label": "retained"},
					Annotations: map[string]string{
						"kubectl.kubernetes.io/last-applied-configuration": "internal",
						"ingressclass.kubernetes.io/is-default-class":      "true",
					},
				},
				Spec: &IngressClassLookupSpec{Controller: tc.controller, Parameters: tc.want},
			}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				calls = append(calls, req.Method+" "+req.URL.RequestURI()+" "+string(body))
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := (&IngressClassDataSource{Metadata: IngressClassLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, want, out)
			require.Equal(t, []string{"GET /apis/networking.k8s.io/v1/ingressclasses/example "}, calls)
			out = ingressClassLookupOutput(obj)
			require.Equal(t, want, out)
			out.Metadata.Labels["controller.kubernetes.io/label"] = "changed"
			out.Metadata.Annotations["ingressclass.kubernetes.io/is-default-class"] = "false"
			if p := out.Spec.Parameters; p != nil {
				for _, field := range []*string{p.APIGroup, p.Scope, p.Namespace} {
					if field != nil {
						*field = "changed"
					}
				}
			}
			require.Equal(t, before, obj)
		})
	}
	require.Equal(t, &IngressClassDataSourceOutput{
		Metadata: IngressClassLookupMetadataOutput{
			Labels: map[string]string{}, Annotations: map[string]string{},
		}, Spec: &IngressClassLookupSpec{},
	}, ingressClassLookupOutput(&networkingv1.IngressClass{}))
}
