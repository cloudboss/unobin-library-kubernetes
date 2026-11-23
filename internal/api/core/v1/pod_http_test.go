package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	podHTTPCollection = "/api/v1/namespaces/work/pods"
	podHTTPPath       = podHTTPCollection + "/subject"
	podHTTPEvents     = "/api/v1/namespaces/work/events"
)

func podHTTPLive() *corev1.Pod {
	return &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "subject", Namespace: "work", UID: "uid"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}},
	}
}

func podHTTPResource() PodResource {
	return PodResource{
		Metadata: ServiceMetadata{Name: new("subject"), Namespace: new("work")},
	}
}

func TestPodCreatePhaseCallOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *[]string
		seen   []corev1.PodPhase
	}{
		{"default", nil, []corev1.PodPhase{corev1.PodPending, corev1.PodRunning}},
		{"pending", new([]string{"Pending"}), []corev1.PodPhase{corev1.PodPending}},
		{"multi", new([]string{"Succeeded", "Running"}),
			[]corev1.PodPhase{corev1.PodPending, corev1.PodSucceeded}},
		{"failed", new([]string{"Failed"}), []corev1.PodPhase{corev1.PodFailed}},
		{"unknown", new([]string{"Unknown"}), []corev1.PodPhase{corev1.PodUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			gets := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				live := podHTTPLive()
				if req.Method == http.MethodGet {
					phase := tc.seen[min(gets, len(tc.seen)-1)]
					live.Status.Phase = phase
					gets++
				}
				respond(t, w, live)
			})
			r := podHTTPResource()
			r.TargetState = tc.target
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, "uid", out.UID)
			want := []string{"POST " + podHTTPCollection}
			for range len(tc.seen) + 1 {
				want = append(want, "GET "+podHTTPPath)
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestPodCreateFailures(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		post, get, events                       int
		phase                                   corev1.PodPhase
		cancel, timeout, badIdentity, finalRead bool
		slowEvents                              bool
		message                                 string
	}{
		{name: "duplicate", post: 409, message: "create Pod"},
		{name: "bad identity", badIdentity: true, message: "no name, namespace, or UID"},
		{name: "missing", get: 404, message: "read Pod"},
		{name: "forbidden", get: 403, message: "read Pod"},
		{name: "empty phase", message: `unexpected Pod phase ""`},
		{name: "failed outside targets", phase: corev1.PodFailed, message: "unexpected Pod phase"},
		{name: "event lookup", phase: corev1.PodFailed, events: 403,
			message: "list Pod warning events"},
		{name: "cancelled", phase: corev1.PodPending, cancel: true, message: "context canceled"},
		{name: "deadline", phase: corev1.PodPending, timeout: true,
			message: "context deadline exceeded"},
		{name: "event lookup deadline", phase: corev1.PodFailed, slowEvents: true,
			message: "context deadline exceeded"},
		{name: "final get", phase: corev1.PodRunning, finalRead: true, message: "read Pod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			requests := make(chan string, 6)
			gets := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.Path
				live := podHTTPLive()
				switch req.URL.Path {
				case podHTTPEvents:
					require.Equal(t,
						"involvedObject.kind=Pod,involvedObject.name=subject,involvedObject.namespace=work",
						req.URL.Query().Get("fieldSelector"))
					if tc.slowEvents {
						select {
						case <-req.Context().Done():
						case <-time.After(3 * time.Second):
							namespaceStatus(t, w, http.StatusForbidden)
						}
						return
					}
					if tc.events != 0 {
						namespaceStatus(t, w, tc.events)
						return
					}
					respond(t, w, podWarningEvents())
					return
				case podHTTPCollection:
					if tc.post != 0 {
						namespaceStatus(t, w, tc.post)
						return
					}
					if tc.badIdentity {
						live.UID = ""
					}
				case podHTTPPath:
					gets++
					if tc.get != 0 || tc.finalRead && gets == 2 {
						status := tc.get
						if tc.finalRead {
							status = 404
						}
						namespaceStatus(t, w, status)
						return
					}
					live.Status.Phase = tc.phase
					if tc.cancel {
						cancel()
					}
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
				respond(t, w, live)
			})
			r := podHTTPResource()
			if tc.timeout {
				r.CreateTimeout = new("200ms")
			}
			if tc.slowEvents {
				r.CreateTimeout = new("1s")
			}
			_, err := r.Create(ctx, c)
			require.ErrorContains(t, err, tc.message)
			switch {
			case tc.cancel:
				require.ErrorIs(t, err, context.Canceled)
			case tc.timeout, tc.slowEvents:
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case tc.events == http.StatusForbidden:
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			}
			if tc.events != 0 || tc.slowEvents {
				require.ErrorContains(t, err, `unexpected Pod phase "Failed"`)
			}
			want := []string{"POST " + podHTTPCollection}
			if tc.post == 0 && !tc.badIdentity {
				want = append(want, "GET "+podHTTPPath)
				switch {
				case tc.finalRead:
					want = append(want, "GET "+podHTTPPath)
				case !tc.cancel && !tc.timeout:
					want = append(want, "GET "+podHTTPEvents)
					if tc.events == 0 && !tc.slowEvents {
						require.ErrorContains(t, err, "Newest: first, Second: second, Third: third")
						require.NotContains(t, err.Error(), "Fourth")
						require.NotContains(t, err.Error(), "Normal")
					}
				}
			}
			require.Equal(t, want, podHTTPCalls(requests))
		})
	}
}

func podWarningEvents() corev1.EventList {
	event := func(reason, message, kind string, age int64) corev1.Event {
		return corev1.Event{
			Reason: reason, Message: message, Type: kind,
			LastTimestamp: metav1.NewTime(time.Unix(age, 0)),
		}
	}
	return corev1.EventList{Items: []corev1.Event{
		event("Fourth", "fourth", corev1.EventTypeWarning, 1),
		event("Third", "third", corev1.EventTypeWarning, 2),
		event("Older", "first", corev1.EventTypeWarning, 4),
		event("Normal", "ignored", corev1.EventTypeNormal, 6),
		event("Second", "second", corev1.EventTypeWarning, 3),
		event("Newest", "first", corev1.EventTypeWarning, 5),
	}}
}

func podHTTPCalls(requests chan string) []string {
	var calls []string
	for {
		select {
		case req := <-requests:
			calls = append(calls, req)
		default:
			return calls
		}
	}
}

func TestPodReadAndUpdateIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		uid    types.UID
	}{
		{"absent", 404, "uid"}, {"forbidden", 403, "uid"}, {"reused", 0, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 4)
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.Path
				require.Equal(t, "Bearer refreshed", req.Header.Get("Authorization"))
				if tc.status != 0 {
					namespaceStatus(t, w, tc.status)
					return
				}
				live := podHTTPLive()
				live.UID = tc.uid
				respond(t, w, live)
			})
			r := podHTTPResource()
			before, err := r.output(podHTTPLive(), *c.Server, ServiceMetadataOutput{})
			require.NoError(t, err)
			p := podPrior{Inputs: r, Outputs: before}
			c.Server = new("https://unreachable.invalid")
			c.Token = new("refreshed")
			_, err = r.Read(t.Context(), c, p)
			switch tc.status {
			case 404:
				require.ErrorIs(t, err, runtime.ErrNotFound)
			case 403:
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			default:
				require.ErrorContains(t, err, "UID changed from uid to other")
			}
			_, err = r.Update(t.Context(), c, p)
			require.Error(t, err)
			require.False(t, errors.Is(err, runtime.ErrNotFound), "%v", err)
			require.Equal(t, []string{"GET " + podHTTPPath, "GET " + podHTTPPath},
				podHTTPCalls(requests))
		})
	}
}

func TestPodUpdatePatchContents(t *testing.T) {
	live := podHTTPLive()
	live.Annotations = map[string]string{
		"example.com/old~key": "prior", "internal.kubernetes.io/system": "keep",
		"example.com/keep": "external",
	}
	live.Labels = map[string]string{"controller.kubernetes.io/keep": "yes"}
	live.Spec.ActiveDeadlineSeconds = new(int64(1800))
	r := podHTTPResource()
	r.Metadata.Annotations = new(map[string]string{"example.com/old~key": "prior"})
	r.IgnoreAnnotations = new([]string{`^example\.com/(keep|old~key|new)$`})
	calls := make(chan string, 4)
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls <- req.Method + " " + req.URL.Path
		if req.Method == http.MethodPatch {
			require.Equal(t, string(types.JSONPatchType), req.Header.Get("Content-Type"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.JSONEq(t, `[
				{"op":"test","path":"/metadata/uid","value":"uid"},
				{"op":"add","path":"/metadata/annotations/example.com~1new","value":"value"},
				{"op":"remove","path":"/metadata/annotations/example.com~1old~0key"},
				{"op":"add","path":"/metadata/labels/team","value":"app"},
				{"op":"replace","path":"/spec/activeDeadlineSeconds","value":900}
			]`, string(body))
			delete(live.Annotations, "example.com/old~key")
			live.Annotations["example.com/new"] = "value"
			live.Labels["team"] = "app"
			live.Spec.ActiveDeadlineSeconds = new(int64(900))
		}
		respond(t, w, live)
	})
	before, err := r.output(live, *c.Server, podTrackedMetadata(r.Metadata, ServiceMetadataOutput{}))
	require.NoError(t, err)
	r.Spec.ActiveDeadlineSeconds = new(int64(1800))
	prior := podPrior{Inputs: r, Outputs: before, Observed: before}
	r.Metadata.Annotations = new(map[string]string{"example.com/new": "value"})
	r.Metadata.Labels = new(map[string]string{"team": "app"})
	r.Spec.ActiveDeadlineSeconds = new(int64(900))
	after, err := r.Update(t.Context(), c, prior)
	require.NoError(t, err)
	require.Equal(t, before.UID, after.UID)
	require.Equal(t, map[string]string{
		"example.com/new": "value",
	}, after.Metadata.Annotations)
	require.Equal(t, map[string]string{"team": "app"}, after.Metadata.Labels)
	require.Equal(t, []string{"GET " + podHTTPPath, "PATCH " + podHTTPPath, "GET " + podHTTPPath},
		podHTTPCalls(calls))
}

func TestPodPatchDeadlineAndNoMutationUpdates(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after *int64
		status        int
	}{
		{"set", nil, new(int64(90)), 0},
		{"remove", new(int64(90)), nil, 422},
		{"unmanaged", nil, nil, 0},
		{"after mutation disappeared", nil, new(int64(90)), 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := podHTTPLive()
			live.Spec.ActiveDeadlineSeconds = tc.before
			var calls []string
			mutated := false
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				switch req.Method {
				case http.MethodPatch:
					var operations []configMapPatchOperation
					require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
					require.Equal(t, []configMapPatchOperation{
						{Op: "test", Path: "/metadata/uid", Value: "uid"},
						{Op: "replace", Path: "/spec/activeDeadlineSeconds",
							Value: float64(serviceValue(tc.after))},
					}, operations)
					if tc.status == 422 {
						namespaceStatus(t, w, tc.status)
						return
					}
					mutated = true
					live.Spec.ActiveDeadlineSeconds = tc.after
				case http.MethodGet:
					if mutated && tc.status == 404 {
						namespaceStatus(t, w, tc.status)
						return
					}
				}
				respond(t, w, live)
			})
			r := podHTTPResource()
			r.Spec.ActiveDeadlineSeconds = tc.before
			out, err := r.output(live, *c.Server, ServiceMetadataOutput{})
			require.NoError(t, err)
			p := podPrior{Inputs: r, Outputs: out}
			r.Spec.ActiveDeadlineSeconds = tc.after
			r.Spec.OS = &PodOS{Name: "linux"}
			current, err := r.Update(t.Context(), c, p)
			if tc.status == 0 {
				require.NoError(t, err)
				require.Nil(t, current.Spec.OS)
				require.Equal(t, tc.after, current.Spec.ActiveDeadlineSeconds)
				require.Equal(t, "uid", current.UID)
			} else {
				require.Error(t, err)
				require.False(t, errors.Is(err, runtime.ErrNotFound), "%v", err)
			}
			want := []string{"GET"}
			if tc.name != "unmanaged" {
				want = append(want, "PATCH")
				if tc.status != 422 {
					want = append(want, "GET")
				}
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestPodDeleteSavedUID(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		deleteStatus, pollStatus int
		reused, timeout, cancel  bool
	}{
		{name: "absent", deleteStatus: 404},
		{name: "wrong-uid", deleteStatus: 409},
		{name: "poll-forbidden", pollStatus: 403},
		{name: "replaced", reused: true},
		{name: "same-uid-then-gone", pollStatus: 404},
		{name: "timeout", timeout: true},
		{name: "cancel", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			requests := make(chan string, 8)
			gets := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if podTestDiscovery(t, w, req) {
					return
				}
				requests <- req.Method + " " + req.URL.Path
				require.Equal(t, podHTTPPath, req.URL.Path)
				switch req.Method {
				case http.MethodDelete:
					var opts metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&opts))
					require.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid"))},
						opts.Preconditions)
					require.Nil(t, opts.PropagationPolicy)
					require.Nil(t, opts.GracePeriodSeconds)
					if tc.deleteStatus != 0 {
						namespaceStatus(t, w, tc.deleteStatus)
						return
					}
					respond(t, w, metav1.Status{Status: "Success"})
				case http.MethodGet:
					gets++
					if tc.pollStatus == 403 || tc.pollStatus == 404 && gets == 2 {
						namespaceStatus(t, w, tc.pollStatus)
						return
					}
					live := podHTTPLive()
					if tc.reused {
						live.UID = "new"
					}
					if tc.cancel {
						cancel()
					}
					respond(t, w, live)
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
			})
			r := podHTTPResource()
			if tc.timeout {
				r.DeleteTimeout = new("200ms")
			}
			out, err := r.output(podHTTPLive(), *c.Server, ServiceMetadataOutput{})
			require.NoError(t, err)
			p := podPrior{Inputs: r, Outputs: out}
			c.Server = new("https://unreachable.invalid")
			err = r.Delete(ctx, c, p)
			switch {
			case tc.timeout:
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case tc.cancel:
				require.ErrorIs(t, err, context.Canceled)
			case tc.deleteStatus == 409:
				require.True(t, apierrors.IsConflict(err), "%v", err)
			case tc.pollStatus == 403:
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			default:
				require.NoError(t, err)
			}
			want := []string{"DELETE " + podHTTPPath}
			if tc.deleteStatus == 0 {
				want = append(want, "GET "+podHTTPPath)
				if tc.pollStatus == 404 {
					want = append(want, "GET "+podHTTPPath)
				}
			}
			require.Equal(t, want, podHTTPCalls(requests))
		})
	}
}

func TestPodMetadataPatchUpdatesAndClearsProjectedKeys(t *testing.T) {
	live := map[string]string{
		"app": "drift", "other": "external", "ignored.example/key": "external",
	}
	projected := map[string]string{"app": "drift"}
	require.Equal(t, []configMapPatchOperation{
		{Op: "replace", Path: "/metadata/labels/app", Value: "restored"},
	}, podMetadataPatch("/metadata/labels", live, projected, map[string]string{"app": "restored"}))
	require.Equal(t, []configMapPatchOperation{
		{Op: "remove", Path: "/metadata/labels/app"},
	}, podMetadataPatch("/metadata/labels", live, projected, nil))
	require.Equal(t, []configMapPatchOperation{
		{Op: "add", Path: "/metadata/labels", Value: map[string]string{"app": "first"}},
	}, podMetadataPatch("/metadata/labels", nil, nil, map[string]string{"app": "first"}))
}

func TestPodUpdateRejectsImmutableChangesBeforePatch(t *testing.T) {
	r := podHTTPResource()
	r.Spec.Container = &[]PodContainer{{Name: "app", Image: new("original")}}
	before, err := r.prepare()
	require.NoError(t, err)
	before.UID = "uid"
	live := before.DeepCopy()
	live.Spec.Containers[0].Image = "externally-changed"
	requests := make(chan string, 2)
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests <- req.Method + " " + req.URL.Path
		respond(t, w, live)
	})
	saved, err := r.output(before, *c.Server, ServiceMetadataOutput{})
	require.NoError(t, err)
	prior := podPrior{Inputs: r, Outputs: saved, Observed: saved}

	replacement := r
	replacement.Spec.Container = &[]PodContainer{{Name: "app", Image: new("desired")}}
	_, err = replacement.Update(t.Context(), c, prior)
	require.ErrorContains(t, err, "replacement is required")
	require.Empty(t, podHTTPCalls(requests))

	r.Metadata.Labels = &map[string]string{"app": "desired"}
	_, err = r.Update(t.Context(), c, prior)
	require.ErrorContains(t, err, "replacement is required")
	require.Equal(t, []string{"GET " + podHTTPPath}, podHTTPCalls(requests))
}

func TestPodUpdateAdoptsComputedNodeAfterRead(t *testing.T) {
	r := podHTTPResource()
	before, err := r.prepare()
	require.NoError(t, err)
	before.UID = "uid"
	live := before.DeepCopy()
	live.Spec.NodeName = "worker"
	requests := make(chan string, 2)
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests <- req.Method + " " + req.URL.Path
		respond(t, w, live)
	})
	saved, err := r.output(before, *c.Server, ServiceMetadataOutput{})
	require.NoError(t, err)
	prior := podPrior{Inputs: r, Outputs: saved}

	r.Spec.NodeName = new("worker")
	out, err := r.Update(t.Context(), c, prior)
	require.NoError(t, err)
	require.Equal(t, "uid", out.UID)
	require.Equal(t, new("worker"), out.Spec.NodeName)
	require.Equal(t, []string{"GET " + podHTTPPath}, podHTTPCalls(requests))

	r.Spec.NodeName = new("other-worker")
	_, err = r.Update(t.Context(), c, prior)
	require.ErrorContains(t, err, "replacement is required")
	require.Equal(t, []string{"GET " + podHTTPPath}, podHTTPCalls(requests))
}
