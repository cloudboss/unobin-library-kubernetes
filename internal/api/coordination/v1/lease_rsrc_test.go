package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareLease(t *testing.T) {
	r := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")},
		Spec: &LeaseSpec{HolderIdentity: ptr.To(""), LeaseTransitions: ptr.To(int64(0)),
			AcquireTime: ptr.To("2026-01-02T03:04:05.123456789+02:00")}}
	got, err := prepareLease(r)
	require.NoError(t, err)
	require.Equal(t, "default", got.Namespace)
	require.Equal(t, ptr.To(""), got.Spec.HolderIdentity)
	require.Equal(t, ptr.To(int32(0)), got.Spec.LeaseTransitions)
	require.Equal(t, "2026-01-02T01:04:05.123456Z",
		got.Spec.AcquireTime.Format("2006-01-02T15:04:05.000000Z"))
	require.Nil(t, got.Spec.RenewTime)
}

func TestLeaseZeroTimeTakesOwnership(t *testing.T) {
	live := &api.Lease{Spec: api.LeaseSpec{AcquireTime: &metav1.MicroTime{Time: time.Now()}}}
	desired := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")},
		Spec: &LeaseSpec{AcquireTime: ptr.To("0001-01-01T00:00:00Z")}}
	body, err := prepareLease(desired)
	require.NoError(t, err)
	got := manageLease(live, body, LeaseResource{}, desired)
	require.Nil(t, got.Spec.AcquireTime)
}

func leaseTestServer(t *testing.T, handler http.HandlerFunc) *config.Configuration {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	return &config.Configuration{Server: ptr.To(server.URL)}
}
func leaseLive() *api.Lease {
	return &api.Lease{TypeMeta: metav1.TypeMeta{APIVersion: "coordination.k8s.io/v1", Kind: "Lease"},
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default",
			UID: "uid", ResourceVersion: "1"}}
}
func sendLease(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func leaseStatus(w http.ResponseWriter, code int, reason metav1.StatusReason) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Code: int32(code), Reason: reason})
}

func TestLeaseCreateRequests(t *testing.T) {
	for _, generated := range []bool{false, true} {
		for _, spec := range []*LeaseSpec{nil, {},
			{HolderIdentity: ptr.To(""), LeaseTransitions: ptr.To(int64(0)),
				LeaseDurationSeconds: ptr.To(int64(2147483647)),
				RenewTime:            ptr.To("2026-01-01T02:00:00.123456789+02:00")},
		} {
			t.Run(fmt.Sprint(generated, spec), func(t *testing.T) {
				r := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")}, Spec: spec}
				if generated {
					r.Metadata.Name = nil
					r.Metadata.GenerateName = ptr.To("example.")
				}
				expected, err := prepareLease(r)
				require.NoError(t, err)
				var calls []string
				var written *api.Lease
				cfg := leaseTestServer(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method+" "+req.URL.Path)
					if req.Method == http.MethodPost {
						var body api.Lease
						require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
						expectedJSON, err := json.Marshal(expected)
						require.NoError(t, err)
						bodyJSON, err := json.Marshal(body)
						require.NoError(t, err)
						require.JSONEq(t, string(expectedJSON), string(bodyJSON))
						written = body.DeepCopy()
						written.Name = "example"
						written.UID = "uid"
						written.ResourceVersion = "1"
					}
					sendLease(w, written)
				})
				out, err := r.Create(t.Context(), cfg)
				require.NoError(t, err)
				require.Equal(t, []string{"POST /apis/coordination.k8s.io/v1/namespaces/default/leases",
					"GET /apis/coordination.k8s.io/v1/namespaces/default/leases/example"}, calls)
				require.Equal(t, projectLease(written, *cfg.Server, r), out)
				time.Sleep(5 * time.Millisecond)
				require.Equal(t, []string{"POST /apis/coordination.k8s.io/v1/namespaces/default/leases",
					"GET /apis/coordination.k8s.io/v1/namespaces/default/leases/example"}, calls)
			})
		}
	}
}

func TestLeaseConflictPreservesFreshOwnership(t *testing.T) {
	before := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example"),
		Labels: ptr.To(map[string]string{"remove": "old", "managed": "old"})},
		Spec: &LeaseSpec{HolderIdentity: ptr.To("old"), LeaseTransitions: ptr.To(int64(4))}}
	desired := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example"),
		Labels: ptr.To(map[string]string{"managed": "new"})},
		Spec: &LeaseSpec{HolderIdentity: ptr.To("")}}
	live := leaseLive()
	live.Labels = map[string]string{"remove": "old", "managed": "drift", "other": "first"}
	live.Annotations = map[string]string{"external": "keep"}
	live.Spec = api.LeaseSpec{HolderIdentity: ptr.To("drift"), LeaseTransitions: ptr.To(int32(7)),
		RenewTime: &metav1.MicroTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Strategy:  ptr.To(api.OldestEmulationVersion), PreferredHolder: ptr.To("candidate")}
	var calls []string
	var bodies []*api.Lease
	cfg := leaseTestServer(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == http.MethodPut {
			var body api.Lease
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			bodies = append(bodies, body.DeepCopy())
			if len(bodies) == 1 {
				live.ResourceVersion = "2"
				live.Labels["other"] = "second"
				leaseStatus(w, 409, metav1.StatusReasonConflict)
				return
			}
			live = body.DeepCopy()
		}
		sendLease(w, live)
	})
	saved := projectLease(live, *cfg.Server, before)
	out, err := desired.Update(t.Context(), cfg, leasePrior{
		Inputs: before, Outputs: saved, Configuration: cfg,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "PUT", "GET", "PUT", "GET"}, calls)
	expected, err := prepareLease(desired)
	require.NoError(t, err)
	initial := leaseLive()
	initial.Labels = map[string]string{"remove": "old", "managed": "drift", "other": "first"}
	initial.Annotations = map[string]string{"external": "keep"}
	initial.Spec = live.Spec
	first := manageLease(initial, expected, before, desired)
	second := first.DeepCopy()
	second.ResourceVersion = "2"
	second.Labels["other"] = "second"
	require.Equal(t, []*api.Lease{first, second}, bodies)
	require.Equal(t, LeaseSpec{HolderIdentity: ptr.To("")}, out.Spec)
	require.Equal(t, map[string]string{"managed": "new"}, out.Metadata.Labels)
}

func TestLeaseUpdateGuardsAndErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*api.Lease)
		code   int
		reason metav1.StatusReason
		puts   int
	}{
		{name: "missing RV", mutate: func(l *api.Lease) { l.ResourceVersion = "" }},
		{name: "UID", mutate: func(l *api.Lease) { l.UID = "replacement" }},
		{name: "name", mutate: func(l *api.Lease) { l.Name = "wrong" }},
		{name: "namespace", mutate: func(l *api.Lease) { l.Namespace = "wrong" }},
		{name: "forbidden", code: 403, reason: metav1.StatusReasonForbidden, puts: 1},
		{name: "invalid", code: 422, reason: metav1.StatusReasonInvalid, puts: 1},
		{name: "notfound", code: 404, reason: metav1.StatusReasonNotFound, puts: 1},
		{name: "conflict exhaustion", code: 409, reason: metav1.StatusReasonConflict, puts: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := leaseLive()
			saved := projectLease(live, "server", LeaseResource{})
			if tc.mutate != nil {
				tc.mutate(live)
			}
			var calls []string
			cfg := leaseTestServer(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == http.MethodPut {
					leaseStatus(w, tc.code, tc.reason)
					return
				}
				sendLease(w, live)
			})
			saved.Server = *cfg.Server
			r := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")}}
			_, err := r.Update(t.Context(), cfg, leasePrior{Inputs: r, Outputs: saved, Configuration: cfg})
			require.Error(t, err)
			expected := []string{"GET"}
			if tc.puts > 0 {
				expected = nil
				for range tc.puts {
					expected = append(expected, "GET", "PUT")
				}
			}
			require.Equal(t, expected, calls)
		})
	}
}

func TestLeaseReadSavedServerAndHistoricalInputs(t *testing.T) {
	live := leaseLive()
	live.Spec.HolderIdentity = ptr.To("observed")
	var auth string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		sendLease(w, live)
	}))
	t.Cleanup(server.Close)
	cfg := &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: ptr.To(true)}
	prior := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("invalid!")},
		Spec: &LeaseSpec{HolderIdentity: ptr.To("old"), AcquireTime: ptr.To("invalid")}}
	saved := projectLease(live, *cfg.Server, prior)
	cfg.Server = ptr.To("http://127.0.0.1:1")
	cfg.Token = ptr.To("current")
	desired := LeaseResource{}
	out, err := desired.Read(t.Context(), cfg, leasePrior{Inputs: prior, Outputs: saved})
	require.NoError(t, err)
	require.Equal(t, ptr.To("observed"), out.Spec.HolderIdentity)
	require.Equal(t, "Bearer current", auth)
}

func TestLeaseDeleteWaitIdentityAndCancellation(t *testing.T) {
	for _, mode := range []string{"notfound", "conflict", "delayed", "replacement", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var calls []string
			gets := 0
			cfg := leaseTestServer(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == http.MethodDelete {
					var body metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					uid := types.UID("uid")
					require.Equal(t, metav1.DeleteOptions{TypeMeta: metav1.TypeMeta{Kind: "DeleteOptions",
						APIVersion: "coordination.k8s.io/v1"}, Preconditions: &metav1.Preconditions{UID: &uid}}, body)
					if mode == "notfound" {
						leaseStatus(w, 404, metav1.StatusReasonNotFound)
						return
					}
					if mode == "conflict" {
						leaseStatus(w, 409, metav1.StatusReasonConflict)
						return
					}
					sendLease(w, metav1.Status{Status: "Success"})
					return
				}
				gets++
				live := leaseLive()
				live.Finalizers = []string{"example.test/hold"}
				if mode == "replacement" {
					live.UID = "new"
				}
				if mode == "delayed" && gets == 2 {
					leaseStatus(w, 404, metav1.StatusReasonNotFound)
					return
				}
				sendLease(w, live)
			})
			saved := projectLease(leaseLive(), *cfg.Server, LeaseResource{})
			r := LeaseResource{}
			prior := leasePrior{Inputs: LeaseResource{DeleteTimeout: ptr.To("invalid")}, Outputs: saved}
			ctx := t.Context()
			var cancel context.CancelFunc
			if mode == "cancel" {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			err := r.Delete(ctx, cfg, prior)
			expected := []string{"DELETE"}
			switch mode {
			case "conflict":
				require.True(t, apierrors.IsConflict(err))
			case "delayed":
				require.NoError(t, err)
				expected = append(expected, "GET", "GET")
			case "replacement":
				require.NoError(t, err)
				expected = append(expected, "GET")
			case "cancel":
				require.ErrorIs(t, err, context.DeadlineExceeded)
				expected = append(expected, "GET")
			default:
				require.NoError(t, err)
			}
			require.Equal(t, expected, calls)
		})
	}
}

func TestLeaseReadMissingAndReplaced(t *testing.T) {
	for _, mode := range []string{"missing", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			cfg := leaseTestServer(t, func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, http.MethodGet, req.Method)
				if mode == "missing" {
					leaseStatus(w, 404, metav1.StatusReasonNotFound)
					return
				}
				live := leaseLive()
				live.UID = "new"
				sendLease(w, live)
			})
			saved := projectLease(leaseLive(), *cfg.Server, LeaseResource{})
			r := LeaseResource{}
			_, err := r.Read(t.Context(), cfg, leasePrior{Outputs: saved})
			if mode == "missing" {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.ErrorContains(t, err, "UID changed")
			}
		})
	}
}

func TestLeasePreconditionsBeforeRequests(t *testing.T) {
	requests := []string{}
	cfg := leaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method)
	})
	r := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")}}
	for _, operation := range []func() error{
		func() error { _, err := r.Read(t.Context(), cfg, leasePrior{}); return err },
		func() error { _, err := r.Update(t.Context(), cfg, leasePrior{}); return err },
		func() error { return r.Delete(t.Context(), cfg, leasePrior{}) },
	} {
		require.ErrorContains(t, operation(), "missing Lease identity")
	}
	saved := projectLease(leaseLive(), *cfg.Server, r)
	changed := r
	changed.Metadata.Name = ptr.To("replacement")
	_, err := changed.Update(t.Context(), cfg, leasePrior{
		Inputs: r, Outputs: saved, Configuration: cfg,
	})
	require.ErrorContains(t, err, "replacement is required")
	changed = r
	changed.Spec = &LeaseSpec{AcquireTime: ptr.To("invalid")}
	_, err = changed.Update(t.Context(), cfg, leasePrior{
		Inputs: r, Outputs: saved, Configuration: cfg,
	})
	require.ErrorContains(t, err, "parse Lease timestamp")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.Create(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, requests)
}

func TestLeaseAlreadyExists(t *testing.T) {
	var calls []string
	cfg := leaseTestServer(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		leaseStatus(w, 409, metav1.StatusReasonAlreadyExists)
	})
	r := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")}}
	_, err := r.Create(t.Context(), cfg)
	require.True(t, apierrors.IsAlreadyExists(err))
	require.Equal(t, []string{"POST"}, calls)
}

func TestLeaseAllFieldOwnershipAndStableProjection(t *testing.T) {
	before := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example"),
		Annotations: ptr.To(map[string]string{"remove": "old", "managed": "old"})}, Spec: &LeaseSpec{
		HolderIdentity: ptr.To(""), LeaseDurationSeconds: ptr.To(int64(5)),
		LeaseTransitions: ptr.To(int64(0)), AcquireTime: ptr.To("historical-invalid"),
		RenewTime: ptr.To("historical-invalid"),
	}}
	desired := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example"),
		Annotations: ptr.To(map[string]string{"managed": "new"})}}
	live := leaseLive()
	live.Annotations = map[string]string{
		"remove": "drift", "managed": "drift", "external": "preserve"}
	live.Labels = map[string]string{"external": "preserve"}
	stamp := metav1.NewMicroTime(time.Now())
	live.Spec = api.LeaseSpec{HolderIdentity: ptr.To("drift"), LeaseDurationSeconds: ptr.To(int32(9)),
		LeaseTransitions: ptr.To(int32(3)), AcquireTime: &stamp, RenewTime: &stamp,
		Strategy: ptr.To(api.OldestEmulationVersion), PreferredHolder: ptr.To("candidate")}
	body, err := prepareLease(desired)
	require.NoError(t, err)
	got := manageLease(live, body, before, desired)
	want := live.DeepCopy()
	want.Annotations = map[string]string{"managed": "new", "external": "preserve"}
	want.Spec = api.LeaseSpec{Strategy: ptr.To(api.OldestEmulationVersion),
		PreferredHolder: ptr.To("candidate")}
	require.Equal(t, want, got)
	out := projectLease(got, "server", desired)
	require.Equal(t, &LeaseResourceOutput{Server: "server", APIVersion: "coordination.k8s.io/v1",
		Kind: "Lease", Metadata: LeaseMetadataOutput{Name: "example", Namespace: "default", UID: "uid",
			Labels: map[string]string{}, Annotations: map[string]string{"managed": "new"}}}, out)
	got.Spec.HolderIdentity = ptr.To("external-after-clear")
	got.Spec.AcquireTime = &stamp
	body, err = prepareLease(desired)
	require.NoError(t, err)
	require.Equal(t, got, manageLease(got, body, desired, desired))
}

func TestLeaseCreateResponseIdentityGuards(t *testing.T) {
	for _, field := range []string{"name", "namespace", "uid"} {
		t.Run(field, func(t *testing.T) {
			calls := []string{}
			cfg := leaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				live := leaseLive()
				switch field {
				case "name":
					live.Name = ""
				case "namespace":
					live.Namespace = "wrong"
				case "uid":
					live.UID = ""
				}
				sendLease(w, live)
			})
			r := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")}}
			_, err := r.Create(t.Context(), cfg)
			require.Error(t, err)
			require.Equal(t, []string{"POST"}, calls)
		})
	}
}

func TestLeaseConflictCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := []string{}
	cfg := leaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == http.MethodPut {
			cancel()
			leaseStatus(w, 409, metav1.StatusReasonConflict)
			return
		}
		sendLease(w, leaseLive())
	})
	r := LeaseResource{Metadata: LeaseMetadata{Name: ptr.To("example")}}
	saved := projectLease(leaseLive(), *cfg.Server, r)
	_, err := r.Update(ctx, cfg, leasePrior{Inputs: r, Outputs: saved, Configuration: cfg})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"GET", "PUT"}, calls)
}
