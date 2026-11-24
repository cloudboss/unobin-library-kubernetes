package v1_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func namespacesAPI(
	t *testing.T, handler http.HandlerFunc,
) (*config.Configuration, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "fixture",
		Clusters:       map[string]*clientcmdapi.Cluster{"fixture": {Server: server.URL}},
		Contexts: map[string]*clientcmdapi.Context{
			"fixture": {Cluster: "fixture", Namespace: "context-namespace"},
		},
	}, path))
	return &config.Configuration{Kubeconfig: &path, Namespace: new("configured-namespace")},
		func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(requests)
		}
}

func TestNamespacesRead(t *testing.T) {
	c, requests := namespacesAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{
			"kind":"NamespaceList","apiVersion":"v1",
			"metadata":{"continue":"next-page","resourceVersion":"42","remainingItemCount":1},
			"items":[
				{"metadata":{"name":"Zulu.Example","labels":{"tier":"test"}},"status":{"phase":"Active"}},
				{"metadata":{"name":"alpha","uid":"uid-2"},"status":{"phase":"Terminating"}},
				{"metadata":{"name":"Zulu.Example","annotations":{"ignored":"data"}}}
			]
		}`))
		assert.NoError(t, err)
	})
	out, err := (&svc.NamespacesDataSource{}).Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &svc.NamespacesDataSourceOutput{Namespaces: []string{
		"Zulu.Example", "alpha", "Zulu.Example",
	}}, out)
	require.Equal(t, []string{"GET /api/v1/namespaces"}, requests())
}

func TestNamespacesEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty items", `{"kind":"NamespaceList","apiVersion":"v1","items":[]}`},
		{"omitted items", `{"kind":"NamespaceList","apiVersion":"v1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests := namespacesAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte(tc.body))
				assert.NoError(t, err)
			})
			out, err := (&svc.NamespacesDataSource{}).Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &svc.NamespacesDataSourceOutput{Namespaces: []string{}}, out)
			require.Equal(t, []string{"GET /api/v1/namespaces"}, requests())
		})
	}
}

func TestNamespacesClientErrors(t *testing.T) {
	input := &svc.NamespacesDataSource{}
	c, requests := namespacesAPI(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected LIST")
	})
	missing := *c
	missing.Kubeconfig = new(filepath.Join(t.TempDir(), "missing"))
	out, err := input.Read(t.Context(), &missing)
	require.ErrorContains(t, err, "list Namespaces")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, out)

	c.Server = new("https://127.0.0.1")
	c.CertificateAuthorityData = new("aW52YWxpZCBjZXJ0aWZpY2F0ZQ==")
	out, err = input.Read(t.Context(), c)
	require.ErrorContains(t, err, "list Namespaces: create Kubernetes client")
	require.ErrorContains(t, err, "unable to load root certificates")
	require.Nil(t, out)
	require.Empty(t, requests())
}

func TestNamespacesListErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reason metav1.StatusReason
	}{
		{"not found", http.StatusNotFound, metav1.StatusReasonNotFound},
		{"forbidden", http.StatusForbidden, metav1.StatusReasonForbidden},
		{"server error", http.StatusInternalServerError, metav1.StatusReasonInternalError},
		{"malformed response", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests := namespacesAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if tc.reason == "" {
					_, err := w.Write([]byte(`{"items":42}`))
					assert.NoError(t, err)
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
					Status:   "Failure", Reason: tc.reason, Code: int32(tc.status),
				}))
			})
			out, err := (&svc.NamespacesDataSource{}).Read(t.Context(), c)
			require.ErrorContains(t, err, "list Namespaces")
			require.Nil(t, out)
			if tc.reason == "" {
				require.ErrorContains(t, err, "cannot unmarshal number")
			} else {
				var status *apierrors.StatusError
				require.ErrorAs(t, err, &status)
				require.Equal(t, tc.reason, status.ErrStatus.Reason)
				require.EqualValues(t, tc.status, status.ErrStatus.Code)
			}
			require.Equal(t, []string{"GET /api/v1/namespaces"}, requests())
		})
	}
}

func TestNamespacesTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected LIST")
	}))
	server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	out, err := (&svc.NamespacesDataSource{}).Read(t.Context(), &config.Configuration{
		Server: &server.URL,
	})
	require.ErrorContains(t, err, "list Namespaces")
	var connErr *net.OpError
	require.ErrorAs(t, err, &connErr)
	require.Equal(t, "dial", connErr.Op)
	require.Nil(t, out)
}

func TestNamespacesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	c, requests := namespacesAPI(t, func(_ http.ResponseWriter, req *http.Request) {
		cancel()
		select {
		case <-req.Context().Done():
			close(stopped)
		case <-time.After(5 * time.Second):
			t.Error("Namespace LIST did not receive caller cancellation")
		}
	})
	out, err := (&svc.NamespacesDataSource{}).Read(ctx, c)
	require.ErrorContains(t, err, "list Namespaces")
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Namespace LIST remained in flight after cancellation")
	}
	require.Equal(t, []string{"GET /api/v1/namespaces"}, requests())
}
