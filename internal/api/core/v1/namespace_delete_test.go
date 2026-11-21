package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestNamespaceDelete(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		deleteStatus, pollStatus int
		phase                    corev1.NamespacePhase
		replacement              bool
		wantError                bool
	}{
		{name: "already absent", deleteStatus: 404},
		{name: "delete failure", deleteStatus: 403, wantError: true},
		{name: "UID conflict", deleteStatus: 409, wantError: true},
		{name: "terminating then absent", phase: corev1.NamespaceTerminating, pollStatus: 404},
		{name: "replacement", replacement: true, phase: corev1.NamespaceActive},
		{name: "active", phase: corev1.NamespaceActive, wantError: true},
		{name: "empty", wantError: true},
		{name: "unknown", phase: "Unknown", wantError: true},
		{name: "forbidden", pollStatus: 403, wantError: true},
		{name: "server error", pollStatus: 500, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			gets := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				require.Equal(t, "/api/v1/namespaces/generated-actual", req.URL.Path)
				require.Equal(t, "Bearer refreshed", req.Header.Get("Authorization"))
				require.Empty(t, req.URL.RawQuery)
				if req.Method == http.MethodDelete {
					var options metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
					require.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid"))}, options.Preconditions)
					require.Nil(t, options.PropagationPolicy)
					require.Nil(t, options.GracePeriodSeconds)
					if tt.deleteStatus != 0 {
						namespaceStatus(t, w, tt.deleteStatus)
						return
					}
					respond(t, w, metav1.Status{Status: "Success"})
					return
				}
				require.Equal(t, "GET", req.Method)
				gets++
				if tt.pollStatus != 0 && (tt.pollStatus != 404 || gets > 1) {
					namespaceStatus(t, w, tt.pollStatus)
					return
				}
				obj := liveNamespace()
				obj.Status.Phase = tt.phase
				if tt.replacement {
					obj.UID = "replacement"
				}
				respond(t, w, obj)
			})
			p := namespacePrior{Outputs: savedNamespace(*c.Server)}
			c.Server = new("https://unreachable.invalid")
			c.Token = new("refreshed")
			err := (&NamespaceResource{}).Delete(t.Context(), c, p)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			want := []string{"DELETE /api/v1/namespaces/generated-actual"}
			if tt.deleteStatus == 0 {
				want = append(want, "GET /api/v1/namespaces/generated-actual")
				if tt.name == "terminating then absent" {
					want = append(want, "GET /api/v1/namespaces/generated-actual")
				}
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestNamespaceDeleteCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == http.MethodDelete {
			respond(t, w, metav1.Status{Status: "Success"})
			return
		}
		respond(t, w, liveNamespace())
		cancel()
	})
	err := (&NamespaceResource{}).Delete(ctx, c,
		namespacePrior{Outputs: savedNamespace(*c.Server)})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"DELETE", "GET"}, calls)
}

func TestNamespaceDeleteDeadline(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		calls := 0
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			if req.Method == http.MethodDelete {
				respond(t, w, metav1.Status{Status: "Success"})
				return
			}
			require.Equal(t, "GET", req.Method)
			respond(t, w, liveNamespace())
		})
		p := namespacePrior{Inputs: NamespaceResource{DeleteTimeout: new("100ms")},
			Outputs: savedNamespace(*c.Server)}
		ctx := t.Context()
		if callerDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			p.Inputs.DeleteTimeout = new("1h")
		}
		start := time.Now()
		err := (&NamespaceResource{DeleteTimeout: new("1h")}).Delete(ctx, c, p)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Less(t, time.Since(start), time.Second)
		require.Equal(t, 2, calls)
	}
}
