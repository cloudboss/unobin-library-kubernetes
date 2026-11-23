package v1_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

type nodeRequest struct {
	Method string
	Path   string
	Query  url.Values
}

func nodesAPI(
	t *testing.T, handler http.HandlerFunc,
) (*config.Configuration, func() []nodeRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []nodeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, nodeRequest{r.Method, r.URL.Path, r.URL.Query()})
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
			"fixture": {Cluster: "fixture", Namespace: "other"},
		},
	}, path))
	return &config.Configuration{Kubeconfig: &path}, func() []nodeRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]nodeRequest(nil), requests...)
	}
}

func TestNodesRead(t *testing.T) {
	body, err := os.ReadFile("testdata/nodes/list.json")
	require.NoError(t, err)
	heartbeat := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Local().String()
	transition := time.Date(2024, 12, 1, 2, 3, 4, 0, time.UTC).Local().String()
	empty := svc.NodeLookupResult{
		Metadata: svc.NodeLookupMetadata{
			Name: "a-node", Annotations: map[string]string{}, Labels: map[string]string{},
		},
		Spec: svc.NodeLookupSpec{PodCidrs: []string{}, Taints: []svc.NodeLookupTaint{}},
		Status: svc.NodeLookupStatus{
			Allocatable: map[string]string{}, Capacity: map[string]string{},
			Addresses: []svc.NodeLookupAddress{}, Conditions: []svc.NodeLookupCondition{},
		},
	}
	want := []svc.NodeLookupResult{
		{
			Metadata: svc.NodeLookupMetadata{
				Name: "z-node", Generation: 7, ResourceVersion: "node-version", UID: "node-uid",
				Annotations: map[string]string{
					"kubectl.kubernetes.io/last-applied-configuration": "unchanged",
				},
				Labels: map[string]string{
					"kubernetes.io/hostname": "z-node", "example.com/region": "north",
				},
			},
			Spec: svc.NodeLookupSpec{
				PodCIDR: "10.0.0.0/24", PodCidrs: []string{"fd00::/64", "10.0.0.0/24", "fd00::/64"},
				ProviderID: "infra://z-node", Unschedulable: true,
				Taints: []svc.NodeLookupTaint{
					{Key: "zone", Value: "west", Effect: "NoExecute"},
					{Key: "drain", Effect: "NoSchedule"},
				},
			},
			Status: svc.NodeLookupStatus{
				Allocatable: map[string]string{"cpu": "1500m", "memory": "1Gi"},
				Capacity:    map[string]string{"cpu": "2", "memory": "2Gi", "pods": "32"},
				Addresses: []svc.NodeLookupAddress{
					{Type: "InternalIP", Address: "10.10.10.2"},
					{Type: "Hostname", Address: "z-node"},
				},
				Conditions: []svc.NodeLookupCondition{
					{
						Type: "Ready", Status: "True", Reason: "Running", Message: "Node is ready",
						LastHeartbeatTime:  heartbeat,
						LastTransitionTime: transition,
					},
					{
						Type: "MemoryPressure", Status: "False",
						LastHeartbeatTime:  "0001-01-01 00:00:00 +0000 UTC",
						LastTransitionTime: "0001-01-01 00:00:00 +0000 UTC",
					},
				},
				NodeInfo: svc.NodeLookupSystemInfo{
					MachineID: "machine", SystemUUID: "system", BootID: "boot", KernelVersion: "6.6",
					OSImage: "Linux", ContainerRuntimeVersion: "containerd://2",
					KubeletVersion: "v1.33.4", KubeProxyVersion: "v1.33.3",
					OperatingSystem: "linux", Architecture: "amd64",
				},
			},
		},
		empty,
		empty,
	}
	for _, tc := range []struct {
		name string
		meta *svc.NodesLookupMetadata
		q    url.Values
	}{
		{name: "no metadata", q: url.Values{}},
		{
			name: "empty labels",
			meta: &svc.NodesLookupMetadata{Labels: map[string]string{}},
			q:    url.Values{},
		},
		{
			name: "all labels",
			meta: &svc.NodesLookupMetadata{Labels: map[string]string{
				"z": "last", "example.com/zone": "", "app": "first",
			}},
			q: url.Values{"labelSelector": {"app=first,example.com/zone=,z=last"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests := nodesAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write(body)
				require.NoError(t, err)
			})
			out, err := (&svc.NodesDataSource{Metadata: tc.meta}).Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &svc.NodesDataSourceOutput{Nodes: &want}, out)
			require.Equal(t, []nodeRequest{{http.MethodGet, "/api/v1/nodes", tc.q}}, requests())
		})
	}
}

func TestNodesEmptyAndNotFound(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   *[]svc.NodeLookupResult
	}{
		{"empty items", http.StatusOK, `{"items":[]}`, new([]svc.NodeLookupResult{})},
		{"omitted items", http.StatusOK, `{}`, new([]svc.NodeLookupResult{})},
		{"not found", http.StatusNotFound, `{"kind":"Status","reason":"NotFound","code":404}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests := nodesAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, err := w.Write([]byte(tc.body))
				require.NoError(t, err)
			})
			out, err := (&svc.NodesDataSource{}).Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &svc.NodesDataSourceOutput{Nodes: tc.want}, out)
			require.Equal(t, []nodeRequest{{http.MethodGet, "/api/v1/nodes", url.Values{}}},
				requests())
		})
	}
}

func TestNodesReadErrors(t *testing.T) {
	input := &svc.NodesDataSource{}
	out, err := input.Read(t.Context(), &config.Configuration{
		Kubeconfig: new(filepath.Join(t.TempDir(), "missing")),
	})
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, out)

	for _, tc := range []struct {
		name   string
		status int
		reason metav1.StatusReason
	}{
		{"forbidden", http.StatusForbidden, metav1.StatusReasonForbidden},
		{"server error without Retry-After", http.StatusInternalServerError,
			metav1.StatusReasonInternalError},
		{"malformed response", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests := nodesAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if tc.reason == "" {
					_, err := w.Write([]byte(`{"items":42}`))
					require.NoError(t, err)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
					Status:   "Failure", Reason: tc.reason, Code: int32(tc.status),
				}))
			})
			out, err := input.Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "list Nodes")
			if tc.reason == "" {
				require.ErrorContains(t, err, "cannot unmarshal number")
			} else {
				var status *apierrors.StatusError
				require.ErrorAs(t, err, &status)
				require.Equal(t, tc.reason, status.ErrStatus.Reason)
				require.EqualValues(t, tc.status, status.ErrStatus.Code)
			}
			require.Equal(t, []nodeRequest{{http.MethodGet, "/api/v1/nodes", url.Values{}}},
				requests())
		})
	}
}

func TestNodesReadTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request")
	}))
	server.Close()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	out, err := (&svc.NodesDataSource{}).Read(t.Context(), &config.Configuration{Server: &server.URL})
	require.Nil(t, out)
	require.ErrorContains(t, err, "list Nodes")
	var connectionError *net.OpError
	require.ErrorAs(t, err, &connectionError)
	require.Equal(t, "dial", connectionError.Op)
}

func TestNodesReadContext(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	c, requests := nodesAPI(t, func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-time.After(5 * time.Second):
			t.Error("Node LIST did not receive caller cancellation")
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	input := &svc.NodesDataSource{}
	out, err := input.Read(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Node LIST remained in flight after cancellation")
	}
	require.Equal(t, []nodeRequest{{http.MethodGet, "/api/v1/nodes", url.Values{}}}, requests())

	expired, end := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer end()
	out, err = input.Read(expired, c)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, out)
	require.Equal(t, []nodeRequest{{http.MethodGet, "/api/v1/nodes", url.Values{}}}, requests())
}
