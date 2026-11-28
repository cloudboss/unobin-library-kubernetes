package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func scaleValue() *autoscalingv1.Scale {
	return &autoscalingv1.Scale{
		TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v1", Kind: "Scale"},
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", UID: "uid",
			ResourceVersion: "12", Labels: map[string]string{"keep": "label"}},
		Spec:   autoscalingv1.ScaleSpec{Replicas: 3},
		Status: autoscalingv1.ScaleStatus{Replicas: 1, Selector: "app=example"},
	}
}

func TestScaleRequestsAndOutput(t *testing.T) {
	for kind, resource := range map[string]string{
		"Deployment": "deployments", "StatefulSet": "statefulsets", "ReplicaSet": "replicasets",
	} {
		for _, replicas := range []int32{0, 3, 2147483647} {
			t.Run(fmt.Sprintf("%s/%d", kind, replicas), func(t *testing.T) {
				live := scaleValue()
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method+" "+r.URL.RequestURI())
					if r.Method == http.MethodPut {
						var got autoscalingv1.Scale
						require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
						want := live.DeepCopy()
						want.Spec.Replicas = replicas
						require.Equal(t, want, &got)
						live = want
						live.ResourceVersion = "13"
					}
					require.NoError(t, json.NewEncoder(w).Encode(live))
				})
				a := &ScaleAction{Kind: kind, Name: "example", Replicas: replicas,
					Namespace: new("default")}
				out, err := a.Run(t.Context(), c)
				require.NoError(t, err)
				require.Equal(t, &ScaleActionOutput{Server: *c.Server, APIVersion: "apps/v1", Kind: kind,
					Name: "example", Namespace: "default", UID: "uid", ResourceVersion: "13",
					Replicas: replicas}, out)
				path := "/apis/apps/v1/namespaces/default/" + resource + "/example/scale"
				require.Equal(t, []string{"GET " + path, "PUT " + path}, calls)
			})
		}
	}
}

func TestScaleNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name, want           string
		explicit, configured *string
		kubeconfig           bool
	}{
		{name: "explicit", want: "explicit", explicit: new("explicit"), configured: new("configured")},
		{name: "configuration", want: "configured", configured: new("configured")},
		{name: "context", want: "context-space", kubeconfig: true},
		{name: "default", want: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := scaleValue()
			live.Namespace = tc.want
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				require.NoError(t, json.NewEncoder(w).Encode(live))
			})
			c.Namespace = tc.configured
			if tc.kubeconfig {
				name, err := filepath.Abs("testdata/restart-context.yaml")
				require.NoError(t, err)
				c.Kubeconfig = &name
			}
			a := &ScaleAction{Kind: "Deployment", Name: "example", Replicas: 3, Namespace: tc.explicit}
			out, err := a.Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, tc.want, out.Namespace)
			path := "/apis/apps/v1/namespaces/" + tc.want + "/deployments/example/scale"
			require.Equal(t, []string{"GET " + path, "PUT " + path}, calls)
		})
	}
}

func TestScaleResponseChecks(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		change        func(*autoscalingv1.Scale)
	}{
		{"name", "identity", func(s *autoscalingv1.Scale) { s.Name = "other" }},
		{"namespace", "identity", func(s *autoscalingv1.Scale) { s.Namespace = "other" }},
		{"empty UID", "UID", func(s *autoscalingv1.Scale) { s.UID = "" }},
		{"empty version", "resource version", func(s *autoscalingv1.Scale) { s.ResourceVersion = "" }},
	} {
		for _, stage := range []string{"GET", "PUT"} {
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					live := scaleValue()
					if r.Method == stage {
						tc.change(live)
					}
					require.NoError(t, json.NewEncoder(w).Encode(live))
				})
				a := &ScaleAction{Kind: "Deployment", Name: "example", Namespace: new("default"), Replicas: 3}
				out, err := a.Run(t.Context(), c)
				require.ErrorContains(t, err, tc.message)
				require.Nil(t, out)
				want := []string{"GET"}
				if stage == "PUT" {
					want = append(want, "PUT")
				}
				require.Equal(t, want, calls)
			})
		}
	}
	for _, tc := range []struct {
		name       string
		expected   *string
		changedUID bool
		replicas   int32
	}{
		{name: "omitted", replicas: 3}, {name: "matching", expected: new("uid"), replicas: 3},
		{name: "mismatch", expected: new("other"), replicas: 3},
		{name: "changed UID", changedUID: true, replicas: 3}, {name: "accepted replicas", replicas: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				live := scaleValue()
				if r.Method == "PUT" {
					if tc.changedUID {
						live.UID = "replacement"
					}
					live.Spec.Replicas = tc.replicas
				}
				require.NoError(t, json.NewEncoder(w).Encode(live))
			})
			a := &ScaleAction{Kind: "Deployment", Name: "example", Namespace: new("default"),
				Replicas: 3, ExpectedUID: tc.expected}
			out, err := a.Run(t.Context(), c)
			want := []string{"GET", "PUT"}
			switch tc.name {
			case "mismatch":
				require.ErrorContains(t, err, "expected UID")
				require.Nil(t, out)
				want = want[:1]
			case "changed UID":
				require.ErrorContains(t, err, "UID")
				require.Nil(t, out)
			case "accepted replicas":
				require.ErrorContains(t, err, "accepted replicas")
				require.Nil(t, out)
			default:
				require.NoError(t, err)
				require.Equal(t, "uid", out.UID)
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestScaleAPIErrors(t *testing.T) {
	for _, code := range []int{403, 404, 409, 500} {
		for _, stage := range []string{"GET", "PUT"} {
			t.Run(fmt.Sprintf("%d/%s", code, stage), func(t *testing.T) {
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					if r.Method == stage {
						reason := map[int]metav1.StatusReason{403: metav1.StatusReasonForbidden,
							404: metav1.StatusReasonNotFound, 409: metav1.StatusReasonConflict,
							500: metav1.StatusReasonInternalError}[code]
						w.WriteHeader(code)
						require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
							TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure",
							Message: "API diagnostic", Reason: reason, Code: int32(code)}))
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(scaleValue()))
				})
				a := &ScaleAction{Kind: "Deployment", Name: "example", Namespace: new("default"), Replicas: 3}
				out, err := a.Run(t.Context(), c)
				phase := "get"
				if stage == "PUT" {
					phase = "update"
				}
				require.ErrorContains(t, err, "scale "+phase+" Deployment default/example replicas 3")
				require.Nil(t, out)
				var status *apierrors.StatusError
				require.ErrorAs(t, err, &status)
				require.Equal(t, int32(code), status.ErrStatus.Code)
				if code == 409 {
					require.True(t, apierrors.IsConflict(err))
				}
				want := []string{"GET"}
				if stage == "PUT" {
					want = append(want, "PUT")
				}
				require.Equal(t, want, calls)
			})
		}
	}
}

func TestScaleCancellationAndTimeout(t *testing.T) {
	for _, stage := range []string{"before", "GET", "PUT"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%v", stage, deadline), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var mu sync.Mutex
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					calls = append(calls, r.Method)
					mu.Unlock()
					if r.Method == stage {
						_, err := io.Copy(io.Discard, r.Body)
						require.NoError(t, err)
						if !deadline {
							cancel()
						}
						<-r.Context().Done()
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(scaleValue()))
				})
				a := &ScaleAction{Kind: "Deployment", Name: "example", Namespace: new("default"), Replicas: 3}
				if stage == "before" {
					cancel()
				}
				if deadline {
					a.Timeout = new("50ms")
				}
				out, err := a.Run(ctx, c)
				wantErr := error(context.Canceled)
				if deadline && stage != "before" {
					wantErr = context.DeadlineExceeded
				}
				require.ErrorIs(t, err, wantErr)
				require.Nil(t, out)
				mu.Lock()
				defer mu.Unlock()
				var want []string
				if stage != "before" {
					want = []string{"GET"}
				}
				if stage == "PUT" {
					want = append(want, "PUT")
				}
				require.Equal(t, want, calls)
			})
		}
	}
}

func TestScalePreparationFailures(t *testing.T) {
	for _, configure := range []bool{false, true} {
		t.Run(fmt.Sprint(configure), func(t *testing.T) {
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) { calls = append(calls, r.Method) })
			a := &ScaleAction{Kind: "Deployment", Name: "example", Namespace: new("default")}
			message := "parse timeout"
			if configure {
				c.CertificateAuthorityData = new("invalid")
				message = "configure"
			} else {
				a.Timeout = new("invalid")
			}
			out, err := a.Run(t.Context(), c)
			require.ErrorContains(t, err, message)
			require.Nil(t, out)
			require.Empty(t, calls)
		})
	}
	duration, err := prepareScale(&ScaleAction{})
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, duration)
}

func TestScaleTransportFailure(t *testing.T) {
	for _, stage := range []string{"GET", "PUT"} {
		t.Run(stage, func(t *testing.T) {
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.Method == stage {
					connection, buffer, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
					_, err = buffer.WriteString("invalid HTTP response\r\n\r\n")
					require.NoError(t, err)
					require.NoError(t, buffer.Flush())
					require.NoError(t, connection.Close())
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(scaleValue()))
			})
			a := &ScaleAction{Kind: "Deployment", Name: "example", Namespace: new("default"), Replicas: 3}
			out, err := a.Run(t.Context(), c)
			require.ErrorContains(t, err, "malformed HTTP")
			var transport *url.Error
			require.ErrorAs(t, err, &transport)
			require.Nil(t, out)
			want := []string{"GET"}
			if stage == "PUT" {
				want = append(want, "PUT")
			}
			require.Equal(t, want, calls)
		})
	}
}
