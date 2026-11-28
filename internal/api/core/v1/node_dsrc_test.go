package v1

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestNodeLookupRead(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		require.Empty(t, req.URL.RawQuery)
		require.Zero(t, req.ContentLength)
		respond(t, w, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker.example"}})
	})
	c.Namespace = new("ignored")
	r := &NodeDataSource{Metadata: NodeIdentity{Name: "worker.example"}}
	out, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "worker.example", out.Metadata.Name)
	require.NotNil(t, out.Spec)
	require.NotNil(t, out.Status)
	require.Equal(t, []string{"GET /api/v1/nodes/worker.example"}, calls)
}

func TestNodeLookupProjection(t *testing.T) {
	timestamp := metav1.NewTime(time.Date(2025, 2, 3, 4, 5, 6, 0,
		time.FixedZone("offset", 3600)))
	ref := &corev1.NodeConfigSource{ConfigMap: &corev1.ConfigMapNodeConfigSource{
		Namespace: "config-ns", Name: "config", UID: "config-uid", ResourceVersion: "3",
		KubeletConfigKey: "kubelet.conf",
	}}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker.example", GenerateName: "worker-", UID: "uid", ResourceVersion: "12",
			Generation: 4, Labels: map[string]string{"kubernetes.io/controller": "present"},
			Annotations:       map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "raw"},
			DeletionTimestamp: &timestamp,
		},
		Spec: corev1.NodeSpec{
			PodCIDR: "10.0.0.0/24", PodCIDRs: []string{"fd00::/64", "10.0.0.0/24", "fd00::/64"},
			ProviderID: "provider://node", Unschedulable: true, DoNotUseExternalID: "external",
			ConfigSource: ref,
			Taints: []corev1.Taint{
				{Key: "z", Value: "v", Effect: "Future", TimeAdded: &timestamp},
				{Key: "a", TimeAdded: &metav1.Time{}}, {Key: "z", Value: "v", Effect: "Future"},
			},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				"cpu": resource.MustParse("1.5"), "example.com/extended": resource.MustParse("1.5Gi"),
			},
			Allocatable: corev1.ResourceList{"memory": resource.MustParse("1024Mi")}, Phase: "Future",
			Conditions: []corev1.NodeCondition{
				{Type: "Ready", Status: "False", Reason: "failed", Message: "not ready",
					LastHeartbeatTime: timestamp},
				{Type: "Future", Status: "Future", LastTransitionTime: timestamp},
			},
			Addresses: []corev1.NodeAddress{
				{Type: "Future", Address: "b"}, {Type: "InternalIP", Address: "a"},
				{Type: "Future", Address: "b"},
			},
			DaemonEndpoints: corev1.NodeDaemonEndpoints{KubeletEndpoint: corev1.DaemonEndpoint{
				Port: -2147483648,
			}},
			NodeInfo: corev1.NodeSystemInfo{
				MachineID: "machine", SystemUUID: "system", BootID: "boot", KernelVersion: "kernel",
				OSImage: "os", ContainerRuntimeVersion: "runtime", KubeletVersion: "kubelet",
				//nolint:staticcheck // Preserve the deprecated API value.
				KubeProxyVersion: "proxy", OperatingSystem: "linux", Architecture: "arm64",
				Swap: &corev1.NodeSwapStatus{Capacity: new(int64(0))},
			},
			Images: []corev1.ContainerImage{
				{Names: []string{"z", "a", "z"}, SizeBytes: 9223372036854775807},
				{}, {Names: []string{"z", "a", "z"}, SizeBytes: 9223372036854775807},
			},
			VolumesInUse:    []corev1.UniqueVolumeName{"raw://b", "raw://a", "raw://b"},
			VolumesAttached: []corev1.AttachedVolume{{Name: "raw://b", DevicePath: "/raw//path"}},
			Config: &corev1.NodeConfigStatus{
				Assigned: ref, Active: &corev1.NodeConfigSource{}, LastKnownGood: ref, Error: "failure",
			},
			RuntimeHandlers: []corev1.NodeRuntimeHandler{
				{Name: "nil"}, {Name: "empty", Features: &corev1.NodeRuntimeHandlerFeatures{}},
				{Name: "flags", Features: &corev1.NodeRuntimeHandlerFeatures{
					RecursiveReadOnlyMounts: new(false), UserNamespaces: new(true),
				}},
			},
			Features: &corev1.NodeFeatures{SupplementalGroupsPolicy: new(false)},
		},
	}
	before := node.DeepCopy()
	var requests []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		respond(t, w, node)
	})
	r := &NodeDataSource{Metadata: NodeIdentity{Name: "worker.example"}}
	got, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"GET /api/v1/nodes/worker.example"}, requests)
	require.Equal(t, nodeDataSourceOutput(node), got)
	configRef := &NodeConfigSourceOutput{ConfigMap: &NodeConfigMapOutput{
		Namespace: "config-ns", Name: "config", UID: "config-uid", ResourceVersion: "3",
		KubeletConfigKey: "kubelet.conf",
	}}
	want := &NodeDataSourceOutput{
		Metadata: NodeMetadataOutput{
			Name: "worker.example", GenerateName: "worker-", UID: "uid", ResourceVersion: "12",
			Generation: 4, Labels: map[string]string{"kubernetes.io/controller": "present"},
			Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "raw"},
		},
		Spec: &NodeSpecOutput{
			PodCIDR: "10.0.0.0/24", PodCidrs: []string{"fd00::/64", "10.0.0.0/24", "fd00::/64"},
			ProviderID: "provider://node", Unschedulable: true, ExternalID: "external",
			ConfigSource: configRef, Taints: []NodeTaintOutput{
				{Key: "z", Value: "v", Effect: "Future", TimeAdded: new("2025-02-03T03:05:06Z")},
				{Key: "a"}, {Key: "z", Value: "v", Effect: "Future"},
			},
		},
		Status: &NodeStatusOutput{
			Capacity:    map[string]string{"cpu": "1500m", "example.com/extended": "1536Mi"},
			Allocatable: map[string]string{"memory": "1Gi"}, Phase: "Future",
			Conditions: []NodeConditionOutput{
				{Type: "Ready", Status: "False", Reason: "failed", Message: "not ready",
					LastHeartbeatTime: new("2025-02-03T03:05:06Z")},
				{Type: "Future", Status: "Future", LastTransitionTime: new("2025-02-03T03:05:06Z")},
			},
			Addresses: []NodeLookupAddress{
				{Type: "Future", Address: "b"}, {Type: "InternalIP", Address: "a"},
				{Type: "Future", Address: "b"},
			},
			DaemonEndpoints: NodeDaemonEndpointsOutput{KubeletEndpoint: NodeEndpointOutput{
				Port: -2147483648,
			}},
			NodeInfo: NodeSystemInfoOutput{
				MachineID: "machine", SystemUUID: "system", BootID: "boot", KernelVersion: "kernel",
				OSImage: "os", ContainerRuntimeVersion: "runtime", KubeletVersion: "kubelet",
				KubeProxyVersion: "proxy", OperatingSystem: "linux", Architecture: "arm64",
				Swap: &NodeSwapOutput{Capacity: new(int64(0))},
			},
			Images: []NodeImageOutput{
				{Names: []string{"z", "a", "z"}, SizeBytes: 9223372036854775807},
				{Names: []string{}}, {Names: []string{"z", "a", "z"}, SizeBytes: 9223372036854775807},
			},
			VolumesInUse:    []string{"raw://b", "raw://a", "raw://b"},
			VolumesAttached: []NodeVolumeOutput{{Name: "raw://b", DevicePath: "/raw//path"}},
			Config: &NodeConfigStatusOutput{
				Assigned: configRef, Active: &NodeConfigSourceOutput{},
				LastKnownGood: configRef, Error: "failure",
			},
			RuntimeHandlers: []NodeRuntimeHandlerOutput{
				{Name: "nil"}, {Name: "empty", Features: &NodeRuntimeFeaturesOutput{}},
				{Name: "flags", Features: &NodeRuntimeFeaturesOutput{
					RecursiveReadOnlyMounts: new(false), UserNamespaces: new(true),
				}},
			},
			Features: &NodeFeaturesOutput{SupplementalGroupsPolicy: new(false)},
		},
	}
	require.Equal(t, want, got)
	require.Equal(t, before, node)
	got.Metadata.Labels["other"] = "value"
	got.Spec.PodCidrs[0] = "changed"
	*got.Status.NodeInfo.Swap.Capacity = 1
	*got.Status.Features.SupplementalGroupsPolicy = true
	require.Equal(t, before, node)
}

func TestNodeLookupZeroValues(t *testing.T) {
	node := &corev1.Node{Status: corev1.NodeStatus{
		Capacity: corev1.ResourceList{"cpu": resource.MustParse("1")},
	}}
	got := nodeDataSourceOutput(node)
	require.Equal(t, &NodeDataSourceOutput{
		Metadata: NodeMetadataOutput{Labels: map[string]string{}, Annotations: map[string]string{}},
		Spec:     &NodeSpecOutput{PodCidrs: []string{}, Taints: []NodeTaintOutput{}},
		Status: &NodeStatusOutput{
			Capacity: map[string]string{"cpu": "1"}, Allocatable: map[string]string{},
			Conditions: []NodeConditionOutput{}, Addresses: []NodeLookupAddress{},
			Images: []NodeImageOutput{}, VolumesInUse: []string{},
			VolumesAttached: []NodeVolumeOutput{}, RuntimeHandlers: []NodeRuntimeHandlerOutput{},
		},
	}, got)
	//nolint:staticcheck // Preserve an empty deprecated configuration source.
	node.Spec.ConfigSource = &corev1.NodeConfigSource{}
	node.Status.Config = &corev1.NodeConfigStatus{}
	node.Status.Features = &corev1.NodeFeatures{}
	node.Status.NodeInfo.Swap = &corev1.NodeSwapStatus{}
	got = nodeDataSourceOutput(node)
	require.Equal(t, &NodeConfigSourceOutput{}, got.Spec.ConfigSource)
	require.Equal(t, &NodeConfigStatusOutput{}, got.Status.Config)
	require.Equal(t, &NodeFeaturesOutput{}, got.Status.Features)
	require.Equal(t, &NodeSwapOutput{}, got.Status.NodeInfo.Swap)
}

func TestNodeLookupFailures(t *testing.T) {
	for _, code := range []int{404, 403, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			reason := map[int]metav1.StatusReason{
				404: metav1.StatusReasonNotFound, 403: metav1.StatusReasonForbidden,
				500: metav1.StatusReasonInternalError,
			}[code]
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				w.WriteHeader(code)
				respond(t, w, &metav1.Status{Status: "Failure", Reason: reason, Code: int32(code)})
			})
			r := &NodeDataSource{Metadata: NodeIdentity{Name: "worker.example"}}
			got, err := r.Read(t.Context(), c)
			if code == 404 {
				require.NoError(t, err)
				require.Equal(t, &NodeDataSourceOutput{Metadata: NodeMetadataOutput{
					Name: "worker.example", Labels: map[string]string{}, Annotations: map[string]string{},
				}}, got)
			} else {
				require.ErrorContains(t, err, "Node worker.example")
				var status *apierrors.StatusError
				require.ErrorAs(t, err, &status)
				require.Equal(t, reason, status.ErrStatus.Reason)
				require.Nil(t, got)
			}
			require.Equal(t, []string{"GET /api/v1/nodes/worker.example"}, calls)
		})
	}
	r := &NodeDataSource{Metadata: NodeIdentity{Name: "worker.example"}}
	t.Run("configuration", func(t *testing.T) {
		_, err := r.Read(t.Context(), &config.Configuration{
			Kubeconfig: new(filepath.Join(t.TempDir(), "missing")),
		})
		require.ErrorContains(t, err, "Node worker.example")
	})
	t.Run("malformed", func(t *testing.T) {
		c := configMapAPI(t, func(w http.ResponseWriter, _ *http.Request) {
			_, err := io.WriteString(w, `{"spec":`)
			require.NoError(t, err)
		})
		_, err := r.Read(t.Context(), c)
		require.ErrorContains(t, err, "Node worker.example")
	})
	t.Run("transport", func(t *testing.T) {
		s := httptest.NewServer(http.NotFoundHandler())
		s.Close()
		_, err := r.Read(t.Context(), &config.Configuration{Server: new(s.URL)})
		require.ErrorContains(t, err, "Node worker.example")
	})
}

func TestNodeLookupCancellation(t *testing.T) {
	r := &NodeDataSource{Metadata: NodeIdentity{Name: "worker.example"}}
	t.Run("canceled", func(t *testing.T) {
		c := configMapAPI(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected GET") })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := r.Read(ctx, c)
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("blocked GET", func(t *testing.T) {
		started := make(chan struct{})
		c := configMapAPI(t, func(_ http.ResponseWriter, req *http.Request) {
			close(started)
			<-req.Context().Done()
		})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, err := r.Read(ctx, c)
		require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
		select {
		case <-started:
		default:
			t.Fatal("GET did not start")
		}
	})
}
