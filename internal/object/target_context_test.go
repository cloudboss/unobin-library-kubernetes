package object

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestDeleteDiscoveryCancellation(t *testing.T) {
	for _, name := range []string{"delete-timeout", "caller-deadline", "caller-cancellation"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var startedOnce, canceledOnce sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				startedOnce.Do(func() { close(started) })
				select {
				case <-r.Context().Done():
					canceledOnce.Do(func() { close(canceled) })
				case <-release:
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer func() {
				close(release)
				server.Close()
			}()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			timeout, want := 10*time.Second, context.Canceled
			switch name {
			case "delete-timeout":
				timeout, want = 100*time.Millisecond, context.DeadlineExceeded
			case "caller-deadline":
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
				want = context.DeadlineExceeded
			}
			done := make(chan error, 1)
			go func() {
				done <- Delete(ctx, &config.Configuration{Server: &server.URL}, Target{
					Server: server.URL, APIVersion: "v1", Kind: "ConfigMap",
					Name: "example", Namespace: "default", UID: "managed-uid",
				}, metav1.DeleteOptions{}, timeout)
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("discovery request did not start")
			}
			if name == "caller-cancellation" {
				cancel()
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, want)
			case <-time.After(time.Second):
				t.Fatal("delete did not stop during API discovery")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("discovery HTTP request was not canceled")
			}
		})
	}
}
