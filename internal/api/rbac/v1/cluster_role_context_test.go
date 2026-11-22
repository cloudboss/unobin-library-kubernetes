package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type clusterRoleTransport func(*http.Request) (*http.Response, error)

func (f clusterRoleTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func clusterRoleContextAPI(t *testing.T, f clusterRoleTransport) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	original := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = original })
	return &config.Configuration{Server: new("http://cluster-role.test")}
}

func clusterRoleResponse(t *testing.T, body any, status int) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return &http.Response{StatusCode: status,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(string(encoded)))}
}

func clusterRoleOperation(ctx context.Context, c *config.Configuration, operation string) error {
	r := validClusterRole()
	p := clusterRolePrior{Inputs: *validClusterRole(), Outputs: savedClusterRole(*c.Server)}
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
	return err
}

func TestClusterRoleOperationDeadlines(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "delete"} {
		for _, parentBound := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parent=%t", operation, parentBound), func(t *testing.T) {
				ctx := context.Background()
				if parentBound {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
				}
				var deadlines []time.Time
				var times []time.Time
				var calls []string
				c := clusterRoleContextAPI(t, func(req *http.Request) (*http.Response, error) {
					deadline, ok := req.Context().Deadline()
					require.True(t, ok)
					deadlines = append(deadlines, deadline)
					times = append(times, time.Now())
					calls = append(calls, req.Method)
					return clusterRoleResponse(t, liveClusterRole(), 200), nil
				})
				before := time.Now()
				require.NoError(t, clusterRoleOperation(ctx, c, operation))
				want := map[string][]string{"create": {"POST", "GET", "GET"}, "read": {"GET", "GET"},
					"update": {"PATCH", "GET", "GET"}, "delete": {"DELETE"}}[operation]
				require.Equal(t, want, calls)
				for _, deadline := range deadlines {
					require.Equal(t, deadlines[0], deadline)
				}
				if parentBound {
					deadline, _ := ctx.Deadline()
					require.Equal(t, deadline, deadlines[0])
				} else {
					require.False(t, deadlines[0].Before(before.Add(20*time.Minute)))
					require.False(t, deadlines[0].After(times[0].Add(20*time.Minute)))
				}
			})
		}
	}
}

func TestClusterRoleCancellationAtEveryStage(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "delete"} {
		count := map[string]int{"create": 3, "read": 2, "update": 3, "delete": 1}[operation]
		for blocked := 1; blocked <= count; blocked++ {
			for _, expiration := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/expiration=%t", operation, blocked, expiration), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					if expiration {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
					}
					defer cancel()
					expected, hasDeadline := ctx.Deadline()
					started := make(chan struct{})
					calls := 0
					c := clusterRoleContextAPI(t, func(req *http.Request) (*http.Response, error) {
						calls++
						if hasDeadline {
							deadline, ok := req.Context().Deadline()
							require.True(t, ok)
							require.Equal(t, expected, deadline)
						}
						if calls == blocked {
							close(started)
							<-req.Context().Done()
							return nil, req.Context().Err()
						}
						return clusterRoleResponse(t, liveClusterRole(), 200), nil
					})
					done := make(chan error, 1)
					go func() { done <- clusterRoleOperation(ctx, c, operation) }()
					select {
					case <-started:
					case <-time.After(time.Second):
						t.Fatal("request did not start")
					}
					if !expiration {
						cancel()
					}
					select {
					case err := <-done:
						if expiration {
							require.ErrorIs(t, err, context.DeadlineExceeded)
						} else {
							require.ErrorIs(t, err, context.Canceled)
						}
					case <-time.After(time.Second):
						t.Fatal("operation did not stop")
					}
					require.Equal(t, blocked, calls)
				})
			}
		}
	}
}

func TestClusterRoleFailuresStopRequests(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "delete"} {
		count := map[string]int{"create": 3, "read": 2, "update": 3, "delete": 1}[operation]
		for failure := 1; failure <= count; failure++ {
			for _, status := range []int{403, 404, 409, 500} {
				t.Run(fmt.Sprintf("%s/%d/%d", operation, failure, status), func(t *testing.T) {
					calls := 0
					c := clusterRoleContextAPI(t, func(req *http.Request) (*http.Response, error) {
						calls++
						if calls == failure {
							reason := map[int]metav1.StatusReason{403: metav1.StatusReasonForbidden,
								404: metav1.StatusReasonNotFound, 409: metav1.StatusReasonAlreadyExists,
								500: metav1.StatusReasonInternalError}[status]
							return clusterRoleResponse(t, metav1.Status{Status: "Failure", Code: int32(status),
								Reason: reason}, status), nil
						}
						return clusterRoleResponse(t, liveClusterRole(), 200), nil
					})
					err := clusterRoleOperation(t.Context(), c, operation)
					if operation == "delete" && status == 404 {
						require.NoError(t, err)
					} else if status == 404 && (operation == "read" || failure > 1) {
						require.ErrorIs(t, err, runtime.ErrNotFound)
					} else {
						require.Error(t, err)
						var statusErr *apierrors.StatusError
						require.ErrorAs(t, err, &statusErr)
						require.Equal(t, int32(status), statusErr.ErrStatus.Code)
					}
					require.Equal(t, failure, calls)
				})
			}
		}
	}
}

func TestClusterRoleIdentityAndDelete(t *testing.T) {
	calls := []string{}
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		require.NotContains(t, req.URL.Path, "namespaces")
		if req.Method == "DELETE" {
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, "uid", string(*options.Preconditions.UID))
			require.Nil(t, options.GracePeriodSeconds)
			require.Nil(t, options.PropagationPolicy)
			respond(t, w, metav1.Status{Status: "Success"})
			return
		}
		obj := liveClusterRole()
		obj.Finalizers = []string{"example.com/keep"}
		respond(t, w, obj)
	})
	saved := savedClusterRole(*c.Server)
	c.Server = new("https://must-not-use.invalid")
	c.Token = new("current")
	c.Namespace = new("other")
	r := &ClusterRoleResource{}
	p := clusterRolePrior{Outputs: saved, Inputs: *r}
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, saved.Server, out.Server)
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, []string{"GET", "GET", "DELETE"}, calls)
	direct := apierrors.NewNotFound(schema.GroupResource{Group: "rbac.authorization.k8s.io",
		Resource: "clusterroles"}, "x")
	require.True(t, directClusterRoleNotFound(direct))
	require.False(t, directClusterRoleNotFound(fmt.Errorf("wrapped: %w", direct)))
	require.False(t, directClusterRoleNotFound(errors.New("NotFound")))
	for _, field := range []string{"server", "api-version", "kind", "name", "uid", "nil"} {
		missing := *saved
		switch field {
		case "server":
			missing.Server = ""
		case "api-version":
			missing.APIVersion = ""
		case "kind":
			missing.Kind = "Role"
		case "name":
			missing.Name = ""
		case "uid":
			missing.UID = ""
		}
		p.Outputs = &missing
		if field == "nil" {
			p.Outputs = nil
		}
		_, err := r.Read(t.Context(), c, p)
		require.Error(t, err)
		_, err = r.Update(t.Context(), c, p)
		require.Error(t, err)
		require.Error(t, r.Delete(t.Context(), c, p))
	}
	require.Equal(t, []string{"GET", "GET", "DELETE"}, calls)
}

func TestClusterRoleUIDChecksAndConfigurationErrors(t *testing.T) {
	for _, operation := range []string{"read", "update", "delete"} {
		calls := 0
		c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			if req.Method == "DELETE" {
				w.WriteHeader(409)
				respond(t, w, metav1.Status{
					Code: 409, Reason: metav1.StatusReasonConflict})
				return
			}
			obj := liveClusterRole()
			obj.UID = "replacement"
			respond(t, w, obj)
		})
		require.Error(t, clusterRoleOperation(t.Context(), c, operation))
		require.Equal(t, 1, calls)
	}
	for _, operation := range []string{"create", "read", "update", "delete"} {
		c := &config.Configuration{Server: new("http://cluster-role.test"), ProxyURL: new("invalid")}
		require.ErrorContains(t, clusterRoleOperation(t.Context(), c, operation), "proxy-url")
	}
}

func TestClusterRoleDirectIdentityChanges(t *testing.T) {
	calls := 0
	c := roleAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	for _, name := range []bool{false, true} {
		r := validClusterRole()
		p := clusterRolePrior{Inputs: *validClusterRole(), Outputs: savedClusterRole(*c.Server)}
		if name {
			r.Metadata.GenerateName = nil
			r.Metadata.Name = new("system:renamed")
		} else {
			r.Metadata.GenerateName = new("changed-")
		}
		_, err := r.Update(t.Context(), c, p)
		require.ErrorContains(t, err, "replacement is required")
	}
	require.Zero(t, calls)
}
