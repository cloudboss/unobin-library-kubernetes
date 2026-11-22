package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestSecretTokenWait(t *testing.T) {
	for _, tt := range []struct {
		name      string
		tokenType bool
		wait      *bool
		update    bool
		gets      int
	}{
		{"default enabled", true, nil, false, 3},
		{"disabled", true, new(false), false, 1},
		{"opaque", false, nil, false, 1},
		{"update", true, nil, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gets := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				obj := liveSecret()
				if tt.tokenType {
					obj.Type = corev1.SecretTypeServiceAccountToken
				}
				if req.Method == "GET" {
					gets++
					if gets >= 2 {
						obj.Data = map[string][]byte{"token": {}}
					}
				}
				respond(t, w, obj)
			})
			r := &SecretResource{WaitForServiceAccountToken: tt.wait}
			var out *SecretResourceOutput
			var err error
			if tt.update {
				out, err = r.Update(t.Context(), c, secretPrior{Outputs: secretSaved(*c.Server)})
			} else {
				out, err = r.Create(t.Context(), c)
			}
			require.NoError(t, err)
			require.Equal(t, tt.gets, gets)
			if tt.gets == 3 {
				require.Equal(t, map[string]string{"token": ""}, out.Object.Data)
			}
		})
	}
}

func TestSecretTokenWaitErrorsAndWarnings(t *testing.T) {
	for _, status := range []int{404, 403} {
		for _, eventFailure := range []bool{false, true} {
			t.Run(http.StatusText(status)+"/events="+boolName(eventFailure), func(t *testing.T) {
				var calls []string
				c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method+" "+req.URL.Path)
					if req.Method == "POST" {
						obj := liveSecret()
						obj.Type = corev1.SecretTypeServiceAccountToken
						respond(t, w, obj)
						return
					}
					if strings.HasSuffix(req.URL.Path, "/events") {
						want := fields.Set{"involvedObject.name": "actual", "involvedObject.kind": "Secret",
							"involvedObject.namespace": "default"}.String()
						require.Equal(t, want, req.URL.Query().Get("fieldSelector"))
						require.Empty(t, req.URL.Query().Get("continue"))
						if eventFailure {
							namespaceStatus(t, w, 422)
							return
						}
						events := &corev1.EventList{}
						for i, message := range []string{"oldest", "third", "second", "second", "first", "normal"} {
							event := corev1.Event{Type: "Warning", Message: message,
								LastTimestamp: metav1.NewTime(time.Unix(int64(i), 0))}
							if message == "normal" {
								event.Type = "Normal"
							}
							events.Items = append(events.Items, event)
						}
						respond(t, w, events)
						return
					}
					namespaceStatus(t, w, status)
				})
				_, err := (&SecretResource{}).Create(t.Context(), c)
				require.Error(t, err)
				if eventFailure {
					require.True(t, apierrors.IsInvalid(err))
				} else {
					require.Contains(t, err.Error(), "first\nsecond\nthird")
					require.NotContains(t, err.Error(), "oldest")
					require.NotContains(t, err.Error(), "normal")
					if status == 404 {
						require.True(t, apierrors.IsNotFound(err))
					} else {
						require.True(t, apierrors.IsForbidden(err))
					}
				}
				require.Equal(t, []string{"POST /api/v1/namespaces/default/secrets",
					"GET /api/v1/namespaces/default/secrets/actual",
					"GET /api/v1/namespaces/default/events"}, calls)
			})
		}
	}
}

type secretTransport func(*http.Request) (*http.Response, error)

func (f secretTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func secretContextAPI(t *testing.T, f secretTransport) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	original := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = original })
	return &config.Configuration{Server: new("http://secret.test")}
}

func secretResponse(t *testing.T, body any, status int) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(encoded)))}
}

func TestSecretOperationDeadlines(t *testing.T) {
	for _, tt := range []struct {
		name, timeout    string
		budget           time.Duration
		waiting, failure bool
	}{
		{"create", "", time.Minute, false, false},
		{"create", "2s", 2 * time.Second, true, false},
		{"create", "30m", 30 * time.Minute, true, false},
		{"create", "30m", 30 * time.Minute, true, true},
		{"read", "", 20 * time.Minute, false, false},
		{"update", "", 20 * time.Minute, false, false},
		{"delete", "", 20 * time.Minute, false, false},
	} {
		t.Run(tt.name+"/"+tt.timeout+"/failure="+boolName(tt.failure), func(t *testing.T) {
			start := time.Now()
			var deadlines []time.Time
			var calls []string
			gets := 0
			c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				deadlines = append(deadlines, deadline)
				calls = append(calls, req.Method+" "+req.URL.Path)
				if strings.HasSuffix(req.URL.Path, "/events") {
					return secretResponse(t, &corev1.EventList{}, 200), nil
				}
				obj := liveSecret()
				if tt.waiting {
					obj.Type = corev1.SecretTypeServiceAccountToken
				}
				if req.Method == "GET" {
					gets++
					if tt.failure {
						return secretResponse(t, metav1.Status{
							Code: 403, Reason: metav1.StatusReasonForbidden, Message: "forbidden",
						}, 403), nil
					}
					if gets >= 2 {
						obj.Data = map[string][]byte{"token": {}}
					}
				}
				return secretResponse(t, obj, 200), nil
			})
			r := &SecretResource{}
			if tt.timeout != "" {
				r.CreateTimeout = &tt.timeout
			}
			p := secretPrior{Outputs: secretSaved(*c.Server)}
			var err error
			switch tt.name {
			case "create":
				_, err = r.Create(t.Context(), c)
			case "read":
				_, err = r.Read(t.Context(), c, p)
			case "update":
				_, err = r.Update(t.Context(), c, p)
			case "delete":
				err = r.Delete(t.Context(), c, p)
			}
			if tt.failure {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotEmpty(t, deadlines)
			require.WithinDuration(t, start.Add(tt.budget), deadlines[0], time.Second)
			for _, deadline := range deadlines {
				require.Equal(t, deadlines[0], deadline)
			}
			want := []string{"GET /api/v1/namespaces/default/secrets/actual"}
			switch tt.name {
			case "create":
				want = []string{"POST /api/v1/namespaces/default/secrets",
					"GET /api/v1/namespaces/default/secrets/actual"}
				if tt.waiting {
					if tt.failure {
						want = append(want, "GET /api/v1/namespaces/default/events")
					} else {
						want = append(want, "GET /api/v1/namespaces/default/secrets/actual",
							"GET /api/v1/namespaces/default/secrets/actual")
					}
				}
			case "update":
				want = []string{"PATCH /api/v1/namespaces/default/secrets/actual",
					"GET /api/v1/namespaces/default/secrets/actual"}
			case "delete":
				want = []string{"DELETE /api/v1/namespaces/default/secrets/actual"}
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestSecretCancellationAndEarlierDeadline(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			expected, _ := ctx.Deadline()
			calls := 0
			c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				require.Equal(t, expected, deadline)
				<-req.Context().Done()
				return nil, req.Context().Err()
			})
			r := &SecretResource{CreateTimeout: new("1h")}
			p := secretPrior{Outputs: secretSaved(*c.Server)}
			var err error
			switch operation {
			case "create":
				_, err = r.Create(ctx, c)
			case "read":
				_, err = r.Read(ctx, c, p)
			case "update":
				_, err = r.Update(ctx, c, p)
			case "delete":
				err = r.Delete(ctx, c, p)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Equal(t, 1, calls)
		})
	}
}

func TestSecretExpiredWaitBudget(t *testing.T) {
	var first time.Time
	var paths []string
	c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		require.True(t, ok)
		if first.IsZero() {
			first = deadline
		} else {
			require.Equal(t, first, deadline)
		}
		paths = append(paths, req.Method+" "+req.URL.Path)
		if strings.HasSuffix(req.URL.Path, "/events") {
			require.ErrorIs(t, req.Context().Err(), context.DeadlineExceeded)
			return nil, req.Context().Err()
		}
		obj := liveSecret()
		obj.Type = corev1.SecretTypeServiceAccountToken
		return secretResponse(t, obj, 200), nil
	})
	_, err := (&SecretResource{CreateTimeout: new("30ms")}).Create(t.Context(), c)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.LessOrEqual(t, len(paths), 3)
	for _, path := range paths {
		require.NotContains(t, path, "DELETE")
	}
}

func TestSecretTransportErrors(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			failure := errors.New("transport failure")
			calls := 0
			c := secretContextAPI(t, func(_ *http.Request) (*http.Response, error) {
				calls++
				return nil, failure
			})
			r := &SecretResource{}
			p := secretPrior{Outputs: secretSaved(*c.Server)}
			var err error
			switch operation {
			case "create":
				_, err = r.Create(t.Context(), c)
			case "read":
				_, err = r.Read(t.Context(), c, p)
			case "update":
				_, err = r.Update(t.Context(), c, p)
			case "delete":
				err = r.Delete(t.Context(), c, p)
			}
			require.ErrorIs(t, err, failure)
			require.Equal(t, 1, calls)
		})
	}
}

func TestSecretConfiguredDeadlineBlocksRequests(t *testing.T) {
	for _, stage := range []string{"POST", "GET"} {
		t.Run(stage, func(t *testing.T) {
			var first time.Time
			c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				if first.IsZero() {
					first = deadline
				} else {
					require.Equal(t, first, deadline)
				}
				if req.Method == stage {
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				return secretResponse(t, liveSecret(), 200), nil
			})
			start := time.Now()
			_, err := (&SecretResource{CreateTimeout: new("40ms")}).Create(t.Context(), c)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(start), time.Second)
			require.WithinDuration(t, start.Add(40*time.Millisecond), first, 10*time.Millisecond)
		})
	}
}

func TestSecretTokenWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls []string
	c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if strings.HasSuffix(req.URL.Path, "/events") {
			require.ErrorIs(t, req.Context().Err(), context.Canceled)
			return nil, req.Context().Err()
		}
		obj := liveSecret()
		obj.Type = corev1.SecretTypeServiceAccountToken
		if req.Method == "GET" {
			cancel()
		}
		return secretResponse(t, obj, 200), nil
	})
	_, err := (&SecretResource{}).Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	require.LessOrEqual(t, len(calls), 3)
	for _, call := range calls {
		require.NotContains(t, call, "DELETE")
	}
}
