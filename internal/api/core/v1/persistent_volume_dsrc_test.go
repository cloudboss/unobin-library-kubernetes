package v1_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

const pvLookupRequest = "GET /api/v1/persistentvolumes/lookup"

func pvLookup() *svc.PersistentVolumeDataSource {
	return &svc.PersistentVolumeDataSource{
		Metadata: svc.PersistentVolumeLookupMetadata{Name: "lookup"},
	}
}

func TestPVLookupRead(t *testing.T) {
	var requests []string
	labels := map[string]string{"kubernetes.io/metadata.name": "lookup"}
	annotations := map[string]string{"pv.kubernetes.io/bound-by-controller": "yes"}
	cfg := namespaceInfoAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		body, err := io.ReadAll(req.Body)
		if err != nil || len(body) != 0 {
			t.Errorf("GET request body = %q, error = %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"metadata": {
				"name": "lookup", "generateName": "look-", "namespace": "unexpected",
				"uid": "pv-uid", "generation": 9, "resourceVersion": "42",
				"labels": {"kubernetes.io/metadata.name": "lookup"},
				"annotations": {"pv.kubernetes.io/bound-by-controller": "yes"}
			},
			"spec": {
				"capacity": {"storage": "1.5Gi", "vendor.test/fast": "12"},
				"accessModes": ["ReadWriteOnce", "ReadOnlyMany", "ReadWriteOnce"],
				"persistentVolumeReclaimPolicy": "Delete",
				"storageClassName": "fast",
				"volumeMode": "Block",
				"mountOptions": ["ro", "noexec", "ro"],
				"claimRef": {"name": "claim", "kind": "PersistentVolumeClaim", "uid": "other",
					"resourceVersion": "15"},
				"nfs": {"server": "storage.example", "path": "/export", "readOnly": true},
				"nodeAffinity": {"required": {"nodeSelectorTerms": [
					{
						"matchExpressions": [
							{"key": "zone", "operator": "In", "values": ["z", "a", "z"]},
							{"key": "disk", "operator": "Exists"}
						],
						"matchFields": [{"key": "metadata.name", "operator": "In", "values": ["node"]}]
					},
					{"matchExpressions": [{"key": "rack", "operator": "DoesNotExist"}]}
				]}}
			},
			"status": {"phase": "Failed", "message": "backend offline"}
		}`)
	})
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"test": {Server: *cfg.Server}},
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test", Namespace: "other"}},
		CurrentContext: "test",
	}, path))
	lookup := pvLookup()
	lookup.Metadata.Labels = &map[string]string{"lookup": ""}
	lookup.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/Lookup": "configured value!"}
	out, err := lookup.Read(t.Context(), &config.Configuration{Kubeconfig: &path})
	require.NoError(t, err)
	require.Equal(t, &svc.PersistentVolumeDataSourceOutput{
		Metadata: svc.PersistentVolumeLookupMetadataOutput{
			Name: "lookup", UID: "pv-uid", Generation: 9, ResourceVersion: "42",
			Labels: labels, Annotations: annotations,
		},
		Spec: svc.PersistentVolumeSpecOutput{
			Capacity:                      map[string]string{"storage": "1536Mi", "vendor.test/fast": "12"},
			AccessModes:                   []string{"ReadOnlyMany", "ReadWriteOnce"},
			PersistentVolumeReclaimPolicy: "Delete", StorageClassName: "fast", VolumeMode: "Block",
			MountOptions: []string{"noexec", "ro"},
			ClaimRef:     &svc.PersistentVolumeClaimReference{Name: "claim"},
			PersistentVolumeSource: svc.PersistentVolumeSource{NFS: &svc.PersistentVolumeNFS{
				Server: "storage.example", Path: "/export", ReadOnly: new(true),
			}},
			NodeAffinity: &svc.PersistentVolumeNodeAffinity{
				Required: &svc.PersistentVolumeNodeSelector{
					NodeSelectorTerms: []svc.PersistentVolumeNodeSelectorTerm{
						{
							MatchExpressions: &[]svc.PersistentVolumeNodeSelectorRequirement{
								{Key: "zone", Operator: "In", Values: &[]string{"a", "z"}},
								{Key: "disk", Operator: "Exists", Values: &[]string{}},
							},
							MatchFields: &[]svc.PersistentVolumeNodeSelectorRequirement{
								{Key: "metadata.name", Operator: "In", Values: &[]string{"node"}},
							},
						},
						{MatchExpressions: &[]svc.PersistentVolumeNodeSelectorRequirement{
							{Key: "rack", Operator: "DoesNotExist", Values: &[]string{}},
						}},
					},
				},
			},
		},
	}, out)
	require.Equal(t, []string{pvLookupRequest}, requests)
}

func TestPVLookupSparseAndLegacy(t *testing.T) {
	base := svc.PersistentVolumeSpecOutput{
		AccessModes: []string{}, MountOptions: []string{}, Capacity: map[string]string{},
	}
	for _, tc := range []struct {
		name string
		spec corev1.PersistentVolumeSpec
		want svc.PersistentVolumeSpecOutput
	}{
		{name: "sparse", want: base},
		{
			name: "explicit defaults and unsupported source",
			spec: corev1.PersistentVolumeSpec{
				VolumeMode:                    new(corev1.PersistentVolumeFilesystem),
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				ClaimRef:                      &corev1.ObjectReference{Name: "claim", Namespace: "apps"},
				NodeAffinity:                  &corev1.VolumeNodeAffinity{},
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					PortworxVolume: &corev1.PortworxVolumeSource{VolumeID: "unsupported"},
				},
			},
			want: svc.PersistentVolumeSpecOutput{
				AccessModes: []string{}, MountOptions: []string{}, Capacity: map[string]string{},
				VolumeMode: "Filesystem", PersistentVolumeReclaimPolicy: "Retain",
				ClaimRef: &svc.PersistentVolumeClaimReference{
					Name: "claim", Namespace: new("apps"),
				},
				NodeAffinity: &svc.PersistentVolumeNodeAffinity{},
			},
		},
		{
			name: "legacy and explicit empty paths",
			spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					AzureDisk: &corev1.AzureDiskVolumeSource{
						DiskName: "disk", DataDiskURI: "uri", ReadOnly: new(false),
					},
					FC:       &corev1.FCVolumeSource{TargetWWNs: []string{"z", "a", "z"}},
					HostPath: &corev1.HostPathVolumeSource{},
					Local:    &corev1.LocalVolumeSource{},
				},
			},
			want: svc.PersistentVolumeSpecOutput{
				AccessModes: []string{}, MountOptions: []string{}, Capacity: map[string]string{},
				PersistentVolumeSource: svc.PersistentVolumeSource{
					AzureDisk: &svc.PersistentVolumeAzureDisk{
						DiskName: "disk", DataDiskURI: "uri", ReadOnly: new(false), CachingMode: "",
					},
					FC:       &svc.PersistentVolumeFC{Lun: 0, TargetWWNs: []string{"a", "z"}},
					HostPath: &svc.PersistentVolumeHostPath{Path: new("")},
					Local:    &svc.PersistentVolumeLocal{Path: new("")},
				},
			},
		},
		{
			name: "CSI without optional fields",
			spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{Driver: "driver", VolumeHandle: "vol"},
				},
			},
			want: svc.PersistentVolumeSpecOutput{
				AccessModes: []string{}, MountOptions: []string{}, Capacity: map[string]string{},
				PersistentVolumeSource: svc.PersistentVolumeSource{
					CSI: &svc.PersistentVolumeCSI{
						Driver: "driver", VolumeHandle: "vol", ReadOnly: new(false),
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVCLookupAPI(t)
			api.respond(http.StatusOK, &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "lookup"},
				Spec:       tc.spec, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumePending},
			})
			out, err := pvLookup().Read(t.Context(), &config.Configuration{Server: &api.server.URL})
			require.NoError(t, err)
			require.Equal(t, &svc.PersistentVolumeDataSourceOutput{
				Metadata: svc.PersistentVolumeLookupMetadataOutput{
					Name: "lookup", Labels: map[string]string{}, Annotations: map[string]string{},
				},
				Spec: tc.want,
			}, out)
			require.Equal(t, []string{pvLookupRequest}, api.requests())
		})
	}
}

func TestPVLookupErrors(t *testing.T) {
	api := newPVCLookupAPI(t)
	cfg := &config.Configuration{Server: &api.server.URL}
	requests := []string{}
	for _, tc := range []struct {
		status int
		reason metav1.StatusReason
	}{
		{http.StatusNotFound, metav1.StatusReasonNotFound},
		{http.StatusForbidden, metav1.StatusReasonForbidden},
		{http.StatusInternalServerError, metav1.StatusReasonInternalError},
	} {
		api.respond(tc.status, metav1.Status{Reason: tc.reason, Code: int32(tc.status)})
		out, err := pvLookup().Read(t.Context(), cfg)
		require.ErrorContains(t, err, "get PersistentVolume lookup")
		require.Equal(t, tc.reason, apierrors.ReasonForError(err))
		require.Nil(t, out)
		requests = append(requests, pvLookupRequest)
		require.Equal(t, requests, api.requests())
	}
	out, err := pvLookup().Read(t.Context(), &config.Configuration{
		Kubeconfig: new(filepath.Join(t.TempDir(), "missing")),
	})
	require.ErrorContains(t, err, "get PersistentVolume lookup")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, out)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err = pvLookup().Read(ctx, cfg)
	require.ErrorContains(t, err, "get PersistentVolume lookup")
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)
	require.Equal(t, requests, api.requests())
}

func TestPVLookupDeadline(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	stop := errors.New("test transport stopped")
	for _, timeout := range []time.Duration{0, time.Minute} {
		ctx := t.Context()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		var requests []string
		http.DefaultTransport = namespaceInfoTransport(func(req *http.Request) (*http.Response, error) {
			requests = append(requests, req.Method+" "+req.URL.RequestURI())
			deadline, ok := req.Context().Deadline()
			require.True(t, ok)
			if expected, ok := ctx.Deadline(); ok {
				require.Equal(t, expected, deadline)
			} else {
				require.WithinDuration(t, time.Now().Add(20*time.Minute), deadline, time.Second)
			}
			return nil, stop
		})
		out, err := pvLookup().Read(ctx, &config.Configuration{Server: new("http://pv.invalid")})
		require.ErrorIs(t, err, stop)
		require.Nil(t, out)
		require.Equal(t, []string{pvLookupRequest}, requests)
	}
}
