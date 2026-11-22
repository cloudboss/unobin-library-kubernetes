package v1_test

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func namespaceInfoAPI(t *testing.T, handler http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, Namespace: new("ignored")}
}

func TestNamespaceInfoRead(t *testing.T) {
	for _, mode := range []string{"existing", "empty", "missing"} {
		t.Run(mode, func(t *testing.T) {
			var requests []string
			c := namespaceInfoAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				if mode == "missing" {
					w.WriteHeader(http.StatusNotFound)
					require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
						Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
					}))
					return
				}
				obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "remote.name"}}
				if mode == "existing" {
					obj.ObjectMeta = metav1.ObjectMeta{Name: "remote.name", UID: "uid", Generation: 7,
						ResourceVersion: "42", Annotations: map[string]string{
							"kubectl.kubernetes.io/last-applied-configuration": "retained", "remote": "value"},
						Labels: map[string]string{"kubernetes.io/metadata.name": "remote.name"}}
					obj.Spec.Finalizers = []corev1.FinalizerName{"z.example/final", "a.example/final"}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			input := &svc.NamespaceDataSource{Metadata: svc.NamespaceLookupMetadata{
				Name: "lookup.name", Annotations: &map[string]*string{
					"EXAMPLE.COM/Lookup": new("configured"), "İ.COM/Key": new("unicode"), "null": nil},
				Labels: &map[string]*string{"app": new("lookup"), "null": nil},
			}}
			out, err := input.Read(t.Context(), c)
			require.NoError(t, err)
			want := &svc.NamespaceDataSourceOutput{Metadata: svc.NamespaceLookupMetadataOutput{
				Name: "remote.name", Annotations: map[string]string{}, Labels: map[string]string{},
			}, Spec: &svc.NamespaceLookupSpec{Finalizers: []string{}}}
			if mode == "existing" {
				want.Metadata = svc.NamespaceLookupMetadataOutput{
					Name: "remote.name", UID: "uid", Generation: 7, ResourceVersion: "42",
					Annotations: map[string]string{
						"kubectl.kubernetes.io/last-applied-configuration": "retained", "remote": "value"},
					Labels: map[string]string{"kubernetes.io/metadata.name": "remote.name"},
				}
				want.Spec.Finalizers = []string{"z.example/final", "a.example/final"}
			}
			if mode == "missing" {
				want.Metadata.Name = "lookup.name"
				want.Metadata.Annotations = map[string]string{
					"EXAMPLE.COM/Lookup": "configured", "İ.COM/Key": "unicode",
				}
				want.Metadata.Labels = map[string]string{"app": "lookup"}
				want.Spec = nil
			}
			require.Equal(t, want, out)
			require.Equal(t, []string{"GET /api/v1/namespaces/lookup.name"}, requests)
			body, err := ub.Marshal(out)
			require.NoError(t, err)
			if mode == "empty" {
				require.Contains(t, string(body), "annotations: {}")
				require.Contains(t, string(body), "labels: {}")
				require.Contains(t, string(body), "finalizers: []")
			}
			if mode == "missing" {
				input.Metadata.Annotations, input.Metadata.Labels = nil, nil
				empty, err := input.Read(t.Context(), c)
				require.NoError(t, err)
				require.Equal(t, &svc.NamespaceDataSourceOutput{Metadata: svc.NamespaceLookupMetadataOutput{
					Name: "lookup.name", Annotations: map[string]string{}, Labels: map[string]string{},
				}}, empty)
			}
		})
	}
}

func TestNamespaceInfoErrors(t *testing.T) {
	calls := 0
	c := namespaceInfoAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		require.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
			Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden,
		}))
	})
	input := &svc.NamespaceDataSource{Metadata: svc.NamespaceLookupMetadata{Name: "lookup"}}
	out, err := input.Read(t.Context(), c)
	require.True(t, apierrors.IsForbidden(err))
	require.ErrorContains(t, err, "get Namespace lookup")
	require.Nil(t, out)
	require.Equal(t, 1, calls)
	out, err = input.Read(t.Context(), &config.Configuration{Kubeconfig: new("/no/such/config")})
	require.ErrorContains(t, err, "get Namespace lookup")
	require.Nil(t, out)
	require.Equal(t, 1, calls)
}

type namespaceInfoContextKey struct{}

type namespaceInfoTransport func(*http.Request) (*http.Response, error)

func (f namespaceInfoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNamespaceInfoCallerContext(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	input := &svc.NamespaceDataSource{Metadata: svc.NamespaceLookupMetadata{Name: "lookup"}}
	c := &config.Configuration{Server: new("http://namespace-info.invalid")}
	for _, mode := range []string{"unbounded", "deadline", "cancelled", "cancel-during-get"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), namespaceInfoContextKey{}, mode)
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(ctx, time.Second)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			calls := 0
			sentinel := errors.New("transport failure")
			http.DefaultTransport = namespaceInfoTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, mode, req.Context().Value(namespaceInfoContextKey{}))
				want, hasDeadline := ctx.Deadline()
				got, gotDeadline := req.Context().Deadline()
				require.Equal(t, hasDeadline, gotDeadline)
				require.Equal(t, want, got)
				if mode == "cancel-during-get" {
					cancel()
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				return nil, sentinel
			})
			out, err := input.Read(ctx, c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get Namespace lookup")
			if mode == "cancelled" || mode == "cancel-during-get" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, sentinel)
			}
			require.LessOrEqual(t, calls, 1)
			if mode != "cancelled" {
				require.Equal(t, 1, calls)
			}
		})
	}
}
