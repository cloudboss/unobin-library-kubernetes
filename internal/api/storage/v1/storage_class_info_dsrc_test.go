package v1_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	storage "github.com/cloudboss/unobin-library-kubernetes/internal/api/storage/v1"
)

const storageClassInfoGET = "GET /apis/storage.k8s.io/v1/storageclasses/lookup.name"

type storageClassInfoRequests struct {
	mu       sync.Mutex
	requests []string
}

func (s *storageClassInfoRequests) add(req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req.Method+" "+req.URL.RequestURI())
}

func (s *storageClassInfoRequests) check(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, want, s.requests)
}

func storageClassInfoAPI(t *testing.T, handler http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, Namespace: new("ignored")}
}

func storageClassInfoResponse(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	if status, ok := result.(metav1.Status); ok {
		w.WriteHeader(int(status.Code))
	}
	_ = json.NewEncoder(w).Encode(result)
}

func TestStorageClassInfoReadHitThenMiss(t *testing.T) {
	remote := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "actual.name", GenerateName: "ignored-", UID: "id", Generation: 5,
			ResourceVersion: "42",
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "kept", "ordinary": "",
			},
			Labels: map[string]string{"kubernetes.io/metadata.name": "actual.name", "group": "one"},
		},
		Provisioner: "remote.example/driver", Parameters: map[string]string{"remote": "one"},
		ReclaimPolicy:        new(corev1.PersistentVolumeReclaimRetain),
		VolumeBindingMode:    new(storagev1.VolumeBindingMode("Unfamiliar")),
		AllowVolumeExpansion: new(true),
		MountOptions:         []string{"z", "", "a", "z"},
		AllowedTopologies: []corev1.TopologySelectorTerm{
			{MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{{Key: "discard"}}},
			{},
			{MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{
				{Key: "second", Values: []string{"z", "", "z", "a"}},
				{Key: "first"},
			}},
			{},
		},
	}
	miss := metav1.Status{Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound}
	var requests storageClassInfoRequests
	reordered := remote.DeepCopy()
	reordered.MountOptions = []string{"a", "z", ""}
	reordered.AllowedTopologies[2].MatchLabelExpressions[0].Values = []string{"a", "z", ""}
	responses := []any{remote, reordered, miss, miss}
	c := storageClassInfoAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests.add(req)
		requests.mu.Lock()
		defer requests.mu.Unlock()
		i := len(requests.requests) - 1
		if i >= len(responses) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		storageClassInfoResponse(w, responses[i])
	})
	input := &storage.StorageClassDataSource{
		Metadata: storage.StorageClassLookupMetadata{
			Name:        new("lookup.name"),
			Annotations: &map[string]*string{"EXAMPLE.COM/Lookup": new(""), "_ignored": nil},
			Labels:      &map[string]*string{"app": new("lookup"), "_ignored": nil},
		},
		Parameters:           &map[string]*string{"empty": new(""), "omitted": nil},
		ReclaimPolicy:        new("Fallback"),
		VolumeBindingMode:    new("Configured"),
		AllowVolumeExpansion: new(false),
		MountOptions:         &[]string{"z", "z", "a"},
		AllowedTopologies: &[]storage.StorageClassTopologyTerm{{
			MatchLabelExpressions: &[]storage.StorageClassTopologyExpression{
				{Key: new("configured"), Values: &[]string{"b", "a", "b"}},
				{},
			},
		}},
	}
	got, err := input.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &storage.StorageClassDataSourceOutput{
		Metadata: storage.StorageClassLookupMetadataOutput{
			Name: "actual.name", UID: "id", ResourceVersion: "42", Generation: 5,
			Annotations: remote.Annotations, Labels: remote.Labels,
		},
		Provisioner: new("remote.example/driver"), Parameters: map[string]string{"remote": "one"},
		ReclaimPolicy: new("Retain"), VolumeBindingMode: new("Unfamiliar"),
		AllowVolumeExpansion: new(true), MountOptions: []string{"", "a", "z"},
		AllowedTopologies: []storage.StorageClassTopologyTermOutput{{
			MatchLabelExpressions: []storage.StorageClassTopologyExpressionOutput{
				{Key: "second", Values: []string{"", "a", "z"}}, {Key: "first", Values: []string{}},
			},
		}},
	}, got)
	body, err := ub.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(body), "generate-name")
	requests.check(t, storageClassInfoGET)

	reorderedOut, err := input.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, got, reorderedOut)
	requests.check(t, storageClassInfoGET, storageClassInfoGET)

	got, err = input.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &storage.StorageClassDataSourceOutput{
		Metadata: storage.StorageClassLookupMetadataOutput{
			Name:        "lookup.name",
			Annotations: map[string]string{"EXAMPLE.COM/Lookup": ""},
			Labels:      map[string]string{"app": "lookup"},
		},
		Parameters: map[string]string{"empty": ""}, ReclaimPolicy: new("Fallback"),
		VolumeBindingMode: new("Configured"), AllowVolumeExpansion: new(false),
		MountOptions: []string{"a", "z"},
		AllowedTopologies: []storage.StorageClassTopologyTermOutput{{
			MatchLabelExpressions: []storage.StorageClassTopologyExpressionOutput{
				{Key: "configured", Values: []string{"a", "b"}}, {Values: []string{}},
			},
		}},
	}, got)
	requests.check(t, storageClassInfoGET, storageClassInfoGET, storageClassInfoGET)

	*input = storage.StorageClassDataSource{
		Metadata:      storage.StorageClassLookupMetadata{Name: new("lookup.name")},
		ReclaimPolicy: new(""), VolumeBindingMode: new(""),
		AllowedTopologies: &[]storage.StorageClassTopologyTerm{},
	}
	got, err = input.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &storage.StorageClassDataSourceOutput{
		Metadata: storage.StorageClassLookupMetadataOutput{
			Name: "lookup.name", Annotations: map[string]string{}, Labels: map[string]string{},
		},
		Parameters: map[string]string{}, MountOptions: []string{},
		AllowedTopologies: []storage.StorageClassTopologyTermOutput{},
	}, got)
	requests.check(t, storageClassInfoGET, storageClassInfoGET, storageClassInfoGET,
		storageClassInfoGET)
}

func TestStorageClassInfoReadSparseResults(t *testing.T) {
	emptyExpressions := []storage.StorageClassTopologyTermOutput{{
		MatchLabelExpressions: []storage.StorageClassTopologyExpressionOutput{},
	}}
	for _, tc := range []struct {
		name      string
		remote    *storagev1.StorageClass
		fallback  *[]storage.StorageClassTopologyTerm
		binding   *string
		expansion *bool
		topology  []storage.StorageClassTopologyTermOutput
	}{
		{
			name: "absent API fields retain configured fallbacks",
			remote: &storagev1.StorageClass{
				Parameters: map[string]string{},
			},
			fallback:  &[]storage.StorageClassTopologyTerm{{}},
			binding:   new("Fallback"),
			expansion: new(true),
			topology:  emptyExpressions,
		},
		{
			name:     "absent API fields without fallbacks",
			remote:   &storagev1.StorageClass{},
			topology: []storage.StorageClassTopologyTermOutput{},
		},
		{
			name: "explicit false and empty API topology override fallbacks",
			remote: &storagev1.StorageClass{
				AllowVolumeExpansion: new(false), VolumeBindingMode: new(storagev1.VolumeBindingMode("")),
				AllowedTopologies: []corev1.TopologySelectorTerm{},
			},
			fallback: &[]storage.StorageClassTopologyTerm{{
				MatchLabelExpressions: &[]storage.StorageClassTopologyExpression{{Key: new("ignored")}},
			}},
			binding: new(""), expansion: new(false), topology: emptyExpressions,
		},
		{
			name:     "all API terms empty",
			remote:   &storagev1.StorageClass{AllowedTopologies: []corev1.TopologySelectorTerm{{}, {}}},
			topology: emptyExpressions,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.remote.Name = "actual.name"
			var requests storageClassInfoRequests
			c := storageClassInfoAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests.add(req)
				result := any(tc.remote)
				if tc.remote.AllowedTopologies != nil && len(tc.remote.AllowedTopologies) == 0 {
					result = struct {
						*storagev1.StorageClass
						AllowedTopologies []corev1.TopologySelectorTerm `json:"allowedTopologies"`
					}{tc.remote, tc.remote.AllowedTopologies}
				}
				storageClassInfoResponse(w, result)
			})
			input := storage.StorageClassDataSource{
				Metadata: storage.StorageClassLookupMetadata{
					Name:        new("lookup.name"),
					Annotations: &map[string]*string{"configured": new("discard")},
					Labels:      &map[string]*string{"configured": new("discard")},
				},
				Parameters:    &map[string]*string{"configured": new("discard")},
				ReclaimPolicy: new("Configured"), MountOptions: &[]string{"ignored"},
				AllowedTopologies: tc.fallback,
			}
			if tc.fallback != nil {
				input.VolumeBindingMode, input.AllowVolumeExpansion = new("Fallback"), new(true)
			}
			got, err := input.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &storage.StorageClassDataSourceOutput{
				Metadata: storage.StorageClassLookupMetadataOutput{
					Name: "actual.name", Annotations: map[string]string{}, Labels: map[string]string{},
				},
				Provisioner: new(""), Parameters: map[string]string{}, ReclaimPolicy: new(""),
				VolumeBindingMode: tc.binding, AllowVolumeExpansion: tc.expansion,
				MountOptions: []string{}, AllowedTopologies: tc.topology,
			}, got)
			requests.check(t, storageClassInfoGET)
		})
	}
}

func TestStorageClassInfoReadErrors(t *testing.T) {
	var requests storageClassInfoRequests
	c := storageClassInfoAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests.add(req)
		storageClassInfoResponse(w, metav1.Status{
			Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden,
		})
	})
	input := &storage.StorageClassDataSource{}
	got, err := input.Read(t.Context(), c)
	require.ErrorContains(t, err, "resource name may not be empty")
	require.Nil(t, got)
	requests.check(t)

	input.Metadata.Name = new("lookup.name")
	missing := filepath.Join(t.TempDir(), "missing-config")
	got, err = input.Read(t.Context(), &config.Configuration{Kubeconfig: &missing})
	require.ErrorContains(t, err, "get StorageClass lookup.name")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, got)
	requests.check(t)

	got, err = input.Read(t.Context(), c)
	require.ErrorContains(t, err, "get StorageClass lookup.name")
	require.True(t, apierrors.IsForbidden(err))
	require.Nil(t, got)
	requests.check(t, storageClassInfoGET)
}

type storageClassInfoTransport func(*http.Request) (*http.Response, error)

func (f storageClassInfoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestStorageClassInfoReadContext(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	c := &config.Configuration{Server: new("http://storage-class-info.invalid")}
	input := &storage.StorageClassDataSource{Metadata: storage.StorageClassLookupMetadata{
		Name: new("lookup.name"),
	}}
	for _, mode := range []string{"implicit", "earlier", "cancel", "expired"} {
		t.Run(mode, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			switch mode {
			case "earlier":
				ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
			case "expired":
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			default:
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			var requests storageClassInfoRequests
			sentinel := errors.New("transport failure")
			start := time.Now()
			transport := func(req *http.Request) (*http.Response, error) {
				requests.add(req)
				got, ok := req.Context().Deadline()
				require.True(t, ok)
				if want, bounded := ctx.Deadline(); bounded {
					require.Equal(t, want, got)
				} else {
					require.WithinRange(t, got, start.Add(20*time.Minute), time.Now().Add(20*time.Minute))
				}
				if mode == "cancel" {
					cancel()
					return nil, req.Context().Err()
				}
				if mode == "expired" {
					return nil, req.Context().Err()
				}
				return nil, sentinel
			}
			http.DefaultTransport = storageClassInfoTransport(transport)
			got, err := input.Read(ctx, c)
			require.Nil(t, got)
			require.ErrorContains(t, err, "get StorageClass lookup.name")
			switch mode {
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
			case "expired":
				require.ErrorIs(t, err, context.DeadlineExceeded)
				requests.check(t)
				return
			default:
				require.ErrorIs(t, err, sentinel)
			}
			requests.check(t, storageClassInfoGET)
		})
	}
}
