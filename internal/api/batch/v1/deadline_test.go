package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type requestTransport func(*http.Request) (*http.Response, error)

func (f requestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRequestDeadlineBudgets(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, op := range []string{"create", "update", "read", "delete"} {
		for _, configured := range []string{"", "31m"} {
			t.Run(op+configured, func(t *testing.T) {
				obj := readyJob(t)
				r := job()
				if configured != "" {
					r.CreateTimeout = &configured
					r.UpdateTimeout = &configured
					r.DeleteTimeout = &configured
				}
				out, err := r.output(obj, "http://deadline.invalid")
				require.NoError(t, err)
				p := prior{Inputs: *r, Outputs: out}
				var deadlines []time.Time
				var methods []string
				http.DefaultTransport = requestTransport(func(req *http.Request) (*http.Response, error) {
					deadline, ok := req.Context().Deadline()
					require.True(t, ok)
					deadlines = append(deadlines, deadline)
					methods = append(methods, req.Method+" "+req.URL.Path)
					var body any = obj
					switch req.URL.Path {
					case "/api":
						body = metav1.APIVersions{Versions: []string{"v1"}}
					case "/apis":
						body = metav1.APIGroupList{
							Groups: []metav1.APIGroup{
								{
									Name: "batch", Versions: []metav1.GroupVersionForDiscovery{
										{GroupVersion: "batch/v1", Version: "v1"}},
									PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "batch/v1",
										Version: "v1"}}}}
					case "/api/v1":
						body = metav1.APIResourceList{GroupVersion: "v1"}
					case "/apis/batch/v1":
						body = metav1.APIResourceList{GroupVersion: "batch/v1", APIResources: []metav1.APIResource{{
							Name: "jobs", Kind: "Job", Namespaced: true}}}
					default:
						if op == "delete" {
							data, err := json.Marshal(metav1.Status{
								Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
							require.NoError(t, err)
							return &http.Response{StatusCode: 404, Header: http.Header{"Content-Type": []string{
								"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
						}
					}
					data, err := json.Marshal(body)
					require.NoError(t, err)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{
						"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
				})
				c := &config.Configuration{Server: &out.Server}
				start := time.Now()
				switch op {
				case "create":
					_, err = r.Create(t.Context(), c)
				case "update":
					_, err = r.Update(t.Context(), c, p)
				case "read":
					_, err = r.Read(t.Context(), c, p)
				case "delete":
					err = r.Delete(t.Context(), c, p)
				}
				require.NoError(t, err)
				require.NotEmpty(t, methods)
				want := time.Minute
				if configured != "" {
					want = 31 * time.Minute
				}
				if op == "read" {
					want = 20 * time.Minute
				}
				require.WithinDuration(t, start.Add(want), deadlines[0], time.Second)
				for _, deadline := range deadlines {
					require.Equal(t, deadlines[0], deadline)
				}
				base := "/apis/batch/v1/namespaces/default/jobs"
				switch op {
				case "create":
					require.Equal(t, []string{"POST " + base, "GET " + base + "/example"}, methods)
				case "update":
					require.Equal(t, []string{
						"GET " + base + "/example", "PATCH " + base + "/example",
						"GET " + base + "/example",
					}, methods)
				}
			})
		}
	}
}

func TestRequestAndDiscoveryStalls(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, op := range []string{"create", "update", "read", "delete"} {
		t.Run(op, func(t *testing.T) {
			calls := 0
			http.DefaultTransport = requestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				<-req.Context().Done()
				return nil, req.Context().Err()
			})
			r := job()
			out, err := r.output(readyJob(t), "http://stall.invalid")
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			start := time.Now()
			c := &config.Configuration{Server: &out.Server}
			switch op {
			case "create":
				_, err = r.Create(ctx, c)
			case "update":
				_, err = r.Update(ctx, c, prior{Inputs: *r, Outputs: out})
			case "read":
				_, err = r.Read(ctx, c, prior{Inputs: *r, Outputs: out})
			case "delete":
				err = r.Delete(ctx, c, prior{Inputs: *r, Outputs: out})
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(start), time.Second)
			require.Greater(t, calls, 0)
		})
	}
}
