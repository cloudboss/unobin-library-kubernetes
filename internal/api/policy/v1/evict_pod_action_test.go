package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestEvictPodRequest(t *testing.T) {
	for _, grace := range []*int64{nil, new(int64(0)), new(int64(30))} {
		t.Run("grace", func(t *testing.T) {
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
			var calls []string
			var eviction policyv1.Eviction
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					require.NoError(t, json.NewEncoder(w).Encode(corev1.Pod{ObjectMeta: metav1.ObjectMeta{
						Name: "example", Namespace: "configured", UID: "opaque"}}))
					return
				}
				require.Equal(t, "application/json", r.Header.Get("Content-Type"))
				require.NoError(t, json.NewDecoder(r.Body).Decode(&eviction))
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			}))
			defer server.Close()
			a := &EvictPodAction{Name: "example", ExpectedUID: "opaque", GracePeriodSeconds: grace,
				WaitForDeletion: new(false)}
			out, err := a.Run(t.Context(), &config.Configuration{Server: &server.URL,
				Namespace: new("configured")})
			require.NoError(t, err)
			require.Equal(t, []string{"GET /api/v1/namespaces/configured/pods/example",
				"POST /api/v1/namespaces/configured/pods/example/eviction"}, calls)
			require.Equal(t, policyv1.Eviction{TypeMeta: metav1.TypeMeta{
				APIVersion: "policy/v1", Kind: "Eviction"}, ObjectMeta: metav1.ObjectMeta{
				Name: "example", Namespace: "configured"}, DeleteOptions: &metav1.DeleteOptions{
				Preconditions:      &metav1.Preconditions{UID: new(types.UID("opaque"))},
				GracePeriodSeconds: grace}}, eviction)
			require.Equal(t, &EvictPodActionOutput{Server: server.URL, APIVersion: "v1", Kind: "Pod",
				Namespace: "configured", Name: "example", UID: "opaque", Accepted: true}, out)
		})
	}
}

type evictionResponse struct {
	code       int
	pod        *corev1.Pod
	hint       int32
	header     string
	block      bool
	disconnect bool
}

func evictionPod(uid string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "team",
		UID: types.UID(uid)}}
}

func terminatingEvictionPod() *corev1.Pod {
	pod := evictionPod("opaque")
	pod.DeletionTimestamp = new(metav1.Now())
	return pod
}

func evictionSequence(t *testing.T, responses []evictionResponse,
	run func(*config.Configuration, *[]string)) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		index := len(calls) - 1
		require.Less(t, index, len(responses))
		response := responses[index]
		path := "/api/v1/namespaces/team/pods/example"
		if r.Method == "POST" {
			path += "/eviction"
			var posted policyv1.Eviction
			require.NoError(t, json.NewDecoder(r.Body).Decode(&posted))
			require.Equal(t, &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{
				UID: new(types.UID("opaque"))}}, posted.DeleteOptions)
			require.Equal(t, "policy/v1", posted.APIVersion)
			require.Equal(t, "Eviction", posted.Kind)
			require.Equal(t, metav1.ObjectMeta{Name: "example", Namespace: "team"}, posted.ObjectMeta)
		} else {
			require.Equal(t, "GET", r.Method)
		}
		require.Equal(t, path, r.URL.Path)
		if response.block {
			<-r.Context().Done()
			return
		}
		if response.disconnect {
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if response.header != "" {
			w.Header().Set("Retry-After", response.header)
		}
		if response.code >= 400 {
			w.WriteHeader(response.code)
			reason := map[int]metav1.StatusReason{403: metav1.StatusReasonForbidden,
				404: metav1.StatusReasonNotFound, 409: metav1.StatusReasonConflict,
				422: metav1.StatusReasonInvalid, 429: metav1.StatusReasonTooManyRequests,
				500: metav1.StatusReasonInternalError}[response.code]
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure",
				Reason: reason, Code: int32(response.code), Message: "test refusal",
				Details: &metav1.StatusDetails{RetryAfterSeconds: response.hint}}))
		} else if response.pod != nil {
			require.NoError(t, json.NewEncoder(w).Encode(response.pod))
		} else {
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
		}
	}))
	defer server.Close()
	run(&config.Configuration{Server: &server.URL, Namespace: new("configured")}, &calls)
}

func evictionAction(waiting bool) *EvictPodAction {
	return &EvictPodAction{Name: "example", Namespace: new("team"), ExpectedUID: "opaque",
		WaitForDeletion: new(waiting), Timeout: new("5s")}
}

func TestEvictPodIdentityAndObservation(t *testing.T) {
	tests := []struct {
		name      string
		responses []evictionResponse
		waiting   bool
		accepted  bool
		deleted   bool
		failure   string
		calls     []string
	}{
		{name: "initial absence", responses: []evictionResponse{{code: 404}},
			waiting: true, deleted: true, calls: []string{"GET"}},
		{name: "initial replacement", responses: []evictionResponse{{pod: evictionPod("replacement")}},
			failure: "initial Pod UID", calls: []string{"GET"}},
		{name: "missing UID", responses: []evictionResponse{{pod: evictionPod("")}},
			failure: "invalid identity", calls: []string{"GET"}},
		{name: "terminating acknowledgement",
			responses: []evictionResponse{{pod: terminatingEvictionPod()}},
			calls:     []string{"GET"}},
		{name: "terminating waits", responses: []evictionResponse{{pod: terminatingEvictionPod()},
			{pod: terminatingEvictionPod()}, {code: 404}}, waiting: true, deleted: true,
			calls: []string{"GET", "GET", "GET"}},
		{name: "accepted pending then gone", responses: []evictionResponse{{pod: evictionPod("opaque")},
			{}, {pod: evictionPod("opaque")}, {pod: terminatingEvictionPod()}, {code: 404}},
			waiting: true, accepted: true, deleted: true,
			calls: []string{"GET", "POST", "GET", "GET", "GET"}},
		{name: "accepted replaced", responses: []evictionResponse{{pod: evictionPod("opaque")},
			{}, {pod: evictionPod("replacement")}}, waiting: true, accepted: true, deleted: true,
			calls: []string{"GET", "POST", "GET"}},
		{name: "POST404 same", responses: []evictionResponse{{pod: evictionPod("opaque")},
			{code: 404}, {pod: evictionPod("opaque")}}, failure: "test refusal",
			calls: []string{"GET", "POST", "GET"}},
		{name: "POST404 absent", responses: []evictionResponse{{pod: evictionPod("opaque")},
			{code: 404}, {code: 404}}, deleted: true, calls: []string{"GET", "POST", "GET"}},
		{name: "POST404 replacement", responses: []evictionResponse{{pod: evictionPod("opaque")},
			{code: 404}, {pod: evictionPod("replacement")}}, deleted: true,
			calls: []string{"GET", "POST", "GET"}},
	}
	for _, field := range []string{"name", "namespace"} {
		pod := evictionPod("opaque")
		if field == "name" {
			pod.Name = "wrong"
		} else {
			pod.Namespace = "wrong"
		}
		tests = append(tests, struct {
			name      string
			responses []evictionResponse
			waiting   bool
			accepted  bool
			deleted   bool
			failure   string
			calls     []string
		}{name: "bad " + field, responses: []evictionResponse{{pod: pod}}, failure: "invalid identity",
			calls: []string{"GET"}})
		tests = append(tests, struct {
			name      string
			responses []evictionResponse
			waiting   bool
			accepted  bool
			deleted   bool
			failure   string
			calls     []string
		}{name: "bad observed " + field, responses: []evictionResponse{{pod: evictionPod("opaque")},
			{}, {pod: pod}}, waiting: true, failure: "invalid identity",
			calls: []string{"GET", "POST", "GET"}})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evictionSequence(t, tc.responses, func(c *config.Configuration, calls *[]string) {
				out, err := evictionAction(tc.waiting).Run(t.Context(), c)
				require.Equal(t, tc.calls, *calls)
				if tc.failure != "" {
					require.ErrorContains(t, err, tc.failure)
					require.ErrorContains(t, err, "evict-pod")
					require.ErrorContains(t, err, "team/example UID opaque")
					require.Nil(t, out)
					return
				}
				require.NoError(t, err)
				require.Equal(t, &EvictPodActionOutput{Server: *c.Server, APIVersion: "v1", Kind: "Pod",
					Namespace: "team", Name: "example", UID: "opaque", Accepted: tc.accepted,
					Deleted: tc.deleted, Waited: tc.waiting}, out)
			})
		})
	}
}

func TestEvictPodTerminalErrors(t *testing.T) {
	for _, code := range []int{403, 409, 422, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			evictionSequence(t, []evictionResponse{{pod: evictionPod("opaque")}, {code: code}},
				func(c *config.Configuration, calls *[]string) {
					out, err := evictionAction(true).Run(t.Context(), c)
					require.ErrorContains(t, err, "evict-pod evict team/example UID opaque")
					var status apierrors.APIStatus
					require.ErrorAs(t, err, &status)
					require.Equal(t, int32(code), status.Status().Code)
					require.Nil(t, out)
					require.Equal(t, []string{"GET", "POST"}, *calls)
				})
		})
	}
	evictionSequence(t, []evictionResponse{{pod: evictionPod("opaque")}, {disconnect: true}},
		func(c *config.Configuration, calls *[]string) {
			out, err := evictionAction(true).Run(t.Context(), c)
			require.ErrorContains(t, err, "EOF")
			require.Nil(t, out)
			require.Equal(t, []string{"GET", "POST"}, *calls)
		})
}

func TestEvictPodRefusalRecheck(t *testing.T) {
	for _, next := range []evictionResponse{{code: 404}, {pod: evictionPod("replacement")},
		{pod: terminatingEvictionPod()}, {pod: evictionPod("opaque")}} {
		t.Run(fmt.Sprint(next), func(t *testing.T) {
			responses := []evictionResponse{{pod: evictionPod("opaque")}, {code: 429, hint: 1}, next}
			deleted := next.pod == nil || next.pod.UID != "opaque"
			accepted := next.pod != nil && next.pod.DeletionTimestamp == nil && !deleted
			expected := []string{"GET", "POST", "GET"}
			if accepted {
				responses = append(responses, evictionResponse{})
				expected = append(expected, "POST")
			}
			evictionSequence(t, responses, func(c *config.Configuration, calls *[]string) {
				start := time.Now()
				out, err := evictionAction(false).Run(t.Context(), c)
				require.NoError(t, err)
				require.GreaterOrEqual(t, time.Since(start), time.Second)
				require.Equal(t, expected, *calls)
				require.Equal(t, accepted, out.Accepted)
				require.Equal(t, deleted, out.Deleted)
			})
		})
	}
}

func TestEvictPodRetryHints(t *testing.T) {
	for _, tc := range []struct {
		header string
		hint   int32
		delay  time.Duration
	}{
		{"2", 0, 2 * time.Second}, {"1", 2, 2 * time.Second}, {"bad", 0, 5 * time.Second},
		{"0", 0, 5 * time.Second}, {"-1", -1, 5 * time.Second}, {"", 0, 5 * time.Second},
	} {
		t.Run(tc.header+fmt.Sprint(tc.hint), func(t *testing.T) {
			err := &apierrors.StatusError{ErrStatus: metav1.Status{
				Details: &metav1.StatusDetails{RetryAfterSeconds: tc.hint}}}
			header, _ := strconv.Atoi(tc.header)
			require.Equal(t, tc.delay, evictionRetryDelay(err, int64(header)))
		})
	}
	for _, hint := range []int32{0, 2} {
		t.Run("final numeric header "+fmt.Sprint(hint), func(t *testing.T) {
			responses := []evictionResponse{{pod: evictionPod("opaque")}}
			expected := []string{"GET"}
			for range 10 {
				responses = append(responses, evictionResponse{code: 429, header: "0"})
				expected = append(expected, "POST")
			}
			responses = append(responses, evictionResponse{code: 429, header: "1", hint: hint},
				evictionResponse{code: 404})
			expected = append(expected, "POST", "GET")
			evictionSequence(t, responses, func(c *config.Configuration, calls *[]string) {
				start := time.Now()
				out, err := evictionAction(false).Run(t.Context(), c)
				require.NoError(t, err)
				require.True(t, out.Deleted)
				require.False(t, out.Accepted)
				require.GreaterOrEqual(t, time.Since(start), time.Duration(max(hint, 1))*time.Second)
				require.Equal(t, expected, *calls)
			})
		})
	}
}

func TestEvictPodContextBudget(t *testing.T) {
	for _, phase := range []string{"initial GET", "POST", "refusal", "poll"} {
		t.Run(phase, func(t *testing.T) {
			responses := []evictionResponse{{block: true}}
			expected := []string{"GET"}
			switch phase {
			case "POST":
				responses = []evictionResponse{{pod: evictionPod("opaque")}, {block: true}}
				expected = []string{"GET", "POST"}
			case "refusal":
				responses = []evictionResponse{{pod: evictionPod("opaque")}, {code: 429}}
				expected = []string{"GET", "POST"}
			case "poll":
				responses = []evictionResponse{{pod: evictionPod("opaque")}, {},
					{pod: terminatingEvictionPod()}}
				expected = []string{"GET", "POST", "GET"}
			}
			for _, cancellation := range []bool{false, true} {
				evictionSequence(t, responses, func(c *config.Configuration, calls *[]string) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					a := evictionAction(true)
					a.Timeout = new("60ms")
					want := context.DeadlineExceeded
					if cancellation {
						a.Timeout = new("5s")
						timer := time.AfterFunc(60*time.Millisecond, cancel)
						defer timer.Stop()
						want = context.Canceled
					}
					start := time.Now()
					out, err := a.Run(ctx, c)
					require.ErrorIs(t, err, want)
					require.Nil(t, out)
					require.Less(t, time.Since(start), time.Second)
					require.Equal(t, expected, *calls)
					if phase == "refusal" {
						require.ErrorContains(t, err, "test refusal")
					}
				})
			}
		})
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if deadline {
			ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
		}
		cancel()
		out, err := evictionAction(true).Run(ctx, nil)
		require.Nil(t, out)
		if deadline {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		} else {
			require.ErrorIs(t, err, context.Canceled)
		}
	}
}

func TestEvictPodRefusalDelayDeadline(t *testing.T) {
	for _, header := range []string{"", "bad", "0", "-1", "Wed, 21 Oct 2015 07:28:00 GMT", "2"} {
		t.Run(header, func(t *testing.T) {
			responses := []evictionResponse{{pod: evictionPod("opaque")}}
			expected := []string{"GET"}
			attempts := 1
			if header == "0" || header == "-1" {
				attempts = 11
			}
			for range attempts {
				responses = append(responses, evictionResponse{code: 429, header: header})
				expected = append(expected, "POST")
			}
			evictionSequence(t, responses, func(c *config.Configuration, calls *[]string) {
				a := evictionAction(false)
				a.Timeout = new("80ms")
				if attempts > 1 {
					a.Timeout = new("1s")
				}
				out, err := a.Run(t.Context(), c)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				if header == "2" {
					require.ErrorContains(t, err, "HTTP 429")
				}
				require.Nil(t, out)
				require.Equal(t, expected, *calls)
			})
		})
	}
}

func TestEvictPodInvalidObservedUIDAndReadErrors(t *testing.T) {
	for _, response := range []evictionResponse{{pod: evictionPod("")}, {code: 403}, {code: 500}} {
		evictionSequence(t, []evictionResponse{{pod: evictionPod("opaque")}, {}, response},
			func(c *config.Configuration, calls *[]string) {
				out, err := evictionAction(true).Run(t.Context(), c)
				require.ErrorContains(t, err, "evict-pod wait for deletion team/example UID opaque")
				require.Nil(t, out)
				require.Equal(t, []string{"GET", "POST", "GET"}, *calls)
			})
	}
}
