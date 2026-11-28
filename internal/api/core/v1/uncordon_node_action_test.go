package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func uncordonNodeFixture() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "example", UID: "uid", ResourceVersion: "opaque",
		Labels:      map[string]string{"keep": "label"},
		Annotations: map[string]string{"keep": "annotation"}},
		Spec: corev1.NodeSpec{Unschedulable: true, ProviderID: "provider://keep", PodCIDR: "10.0.0.0/24",
			Taints: []corev1.Taint{{Key: "keep", Value: "value", Effect: "NoSchedule"}}},
		Status: corev1.NodeStatus{Phase: corev1.NodePending}}
}

func TestUncordonNodePatch(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			obj := uncordonNodeFixture()
			before := obj.DeepCopy()
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				require.Equal(t, "/api/v1/nodes/example", r.URL.Path)
				require.Empty(t, r.URL.RawQuery)
				require.Contains(t, r.Header.Get("Accept"), "application/json")
				if r.Method == "PATCH" {
					require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid"},
						{"op":"test","path":"/metadata/resourceVersion","value":"opaque"},
						{"op":"add","path":"/spec/unschedulable","value":false}]`, string(body))
					encoded, err := json.Marshal(obj)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					after, err := patch.Apply(encoded)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(after, obj))
					obj.ResourceVersion = "next"
				} else {
					require.Zero(t, r.ContentLength)
				}
				if explicit && r.Method == "PATCH" {
					body, err := json.Marshal(obj)
					require.NoError(t, err)
					w.Header().Set("Content-Type", "application/json")
					_, err = io.WriteString(w, strings.Replace(string(body), `"spec":{`,
						`"spec":{"unschedulable":false,`, 1))
					require.NoError(t, err)
					return
				}
				respond(t, w, obj)
			})
			out, err := (&UncordonNodeAction{Name: "example", ExpectedUID: "uid"}).Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &UncordonNodeActionOutput{Server: *c.Server, APIVersion: "v1",
				Kind: "Node", Name: "example", UID: "uid", ResourceVersion: "next",
				Unschedulable: false}, out)
			require.Equal(t, []string{"GET /api/v1/nodes/example", "PATCH /api/v1/nodes/example"}, calls)
			before.Spec.Unschedulable, before.ResourceVersion = false, "next"
			require.Equal(t, before, obj)
		})
	}
}

func TestUncordonNodeObservationGuards(t *testing.T) {
	for _, phase := range []string{"get", "already false", "patch"} {
		for _, problem := range []string{"name", "scope", "UID", "empty UID", "version", "deletion",
			"unschedulable"} {
			if problem == "unschedulable" && phase != "patch" {
				continue
			}
			t.Run(phase+"/"+problem, func(t *testing.T) {
				obj := uncordonNodeFixture()
				obj.Spec.Unschedulable = phase != "already false"
				var calls []string
				c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					if phase != "patch" || r.Method == "PATCH" {
						obj.Spec.Unschedulable = problem == "unschedulable"
						switch problem {
						case "name":
							obj.Name = "other"
						case "scope":
							obj.Namespace = "wrong"
						case "UID":
							obj.UID = "other"
						case "empty UID":
							obj.UID = ""
						case "version":
							obj.ResourceVersion = ""
						case "deletion":
							obj.DeletionTimestamp = new(metav1.Now())
						}
					}
					respond(t, w, obj)
				})
				out, err := (&UncordonNodeAction{Name: "example", ExpectedUID: "uid"}).Run(t.Context(), c)
				require.ErrorContains(t, err, "Node example")
				require.Nil(t, out)
				want := []string{"GET"}
				if phase == "patch" {
					want = append(want, "PATCH")
				}
				require.Equal(t, want, calls)
			})
		}
	}
}

func TestUncordonNodeAPIErrorsAndRaces(t *testing.T) {
	for _, method := range []string{"GET", "PATCH"} {
		for _, code := range []int{404, 403, 409, 422, 500} {
			for _, race := range []string{"none", "uid", "version"} {
				if race != "none" && (method != "PATCH" || code != 422) {
					continue
				}
				t.Run(fmt.Sprintf("%s/%d/%s", method, code, race), func(t *testing.T) {
					obj := uncordonNodeFixture()
					var calls []string
					c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
						calls = append(calls, r.Method)
						if r.Method == method {
							if race != "none" {
								if race == "uid" {
									obj.UID = "replacement"
								} else {
									obj.ResourceVersion = "new"
								}
								body, err := io.ReadAll(r.Body)
								require.NoError(t, err)
								patch, err := jsonpatch.DecodePatch(body)
								require.NoError(t, err)
								before, err := json.Marshal(obj)
								require.NoError(t, err)
								_, err = patch.Apply(before)
								require.Error(t, err)
							}
							endpointsHTTPError(t, w, code, metav1.StatusReasonUnknown)
							return
						}
						respond(t, w, obj)
					})
					out, err := (&UncordonNodeAction{Name: "example", ExpectedUID: "uid"}).Run(t.Context(), c)
					require.Nil(t, out)
					var status *apierrors.StatusError
					require.ErrorAs(t, err, &status)
					require.Equal(t, int32(code), status.ErrStatus.Code)
					require.ErrorContains(t, err, strings.ToLower(method)+" Node example")
					if code == 404 {
						require.True(t, apierrors.IsNotFound(err))
					}
					want := []string{"GET"}
					if method == "PATCH" {
						want = append(want, "PATCH")
					}
					require.Equal(t, want, calls)
					require.True(t, obj.Spec.Unschedulable)
				})
			}
		}
	}
}

func TestUncordonNodeCancellation(t *testing.T) {
	for _, phase := range []string{"before", "GET", "PATCH"} {
		for _, mode := range []string{"cancel", "action deadline", "caller deadline"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "caller deadline" {
					var end context.CancelFunc
					ctx, end = context.WithTimeout(ctx, 30*time.Millisecond)
					defer end()
				}
				var calls []string
				c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					if r.Method == phase {
						_, err := io.Copy(io.Discard, r.Body)
						require.NoError(t, err)
						if mode == "cancel" {
							cancel()
						}
						<-r.Context().Done()
						return
					}
					respond(t, w, uncordonNodeFixture())
				})
				a := &UncordonNodeAction{Name: "example", ExpectedUID: "uid"}
				if mode == "action deadline" {
					a.Timeout = new("30ms")
				}
				if phase == "before" {
					cancel()
				}
				out, err := a.Run(ctx, c)
				require.Nil(t, out)
				if mode == "cancel" || phase == "before" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				}
				want := []string(nil)
				if phase != "before" {
					want = []string{"GET"}
				}
				if phase == "PATCH" {
					want = append(want, "PATCH")
				}
				require.Equal(t, want, slices.Clone(calls))
			})
		}
	}
}

func TestUncordonNodeAcceptedWriteLostResponse(t *testing.T) {
	obj := uncordonNodeFixture()
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == "PATCH" {
			obj.Spec.Unschedulable = false
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			return
		}
		respond(t, w, obj)
	})
	a := &UncordonNodeAction{Name: "example", ExpectedUID: "uid"}
	out, err := a.Run(t.Context(), c)
	require.Error(t, err)
	require.Nil(t, out)
	out, err = a.Run(t.Context(), c)
	require.NoError(t, err)
	require.False(t, out.Unschedulable)
	require.Equal(t, []string{"GET", "PATCH", "GET"}, calls)
}

func TestUncordonNodePostRequestCancellation(t *testing.T) {
	for _, phase := range []string{"GET", "PATCH"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls []string
			c := secretContextAPI(t, func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.Method)
				obj := uncordonNodeFixture()
				obj.Spec.Unschedulable = r.Method != phase
				if r.Method == phase {
					cancel()
				}
				return secretResponse(t, obj, 200), nil
			})
			out, err := (&UncordonNodeAction{Name: "example", ExpectedUID: "uid"}).Run(ctx, c)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, out)
			want := []string{"GET"}
			if phase == "PATCH" {
				want = append(want, "PATCH")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestUncordonNodeConversionAndMalformedResponse(t *testing.T) {
	for _, phase := range []string{"prepare", "GET", "PATCH"} {
		t.Run(phase, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.Method == phase {
					_, _ = io.WriteString(w, "invalid JSON")
					return
				}
				respond(t, w, uncordonNodeFixture())
			})
			a := &UncordonNodeAction{Name: "example", ExpectedUID: "uid"}
			if phase == "prepare" {
				a.Timeout = new("invalid")
			}
			out, err := a.Run(t.Context(), c)
			require.ErrorContains(t, err, "Node example")
			require.Nil(t, out)
			want := []string(nil)
			if phase != "prepare" {
				want = []string{"GET"}
			}
			if phase == "PATCH" {
				want = append(want, "PATCH")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestUncordonNodeAlreadyFalse(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				obj := uncordonNodeFixture()
				obj.Spec.Unschedulable = false
				body, err := json.Marshal(obj)
				require.NoError(t, err)
				if explicit {
					body = []byte(strings.Replace(string(body), `"spec":{`,
						`"spec":{"unschedulable":false,`, 1))
				}
				w.Header().Set("Content-Type", "application/json")
				_, err = w.Write(body)
				require.NoError(t, err)
			})
			c.Namespace = new("ignored")
			a := &UncordonNodeAction{Name: "example", ExpectedUID: "uid"}
			out, err := a.Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &UncordonNodeActionOutput{Server: *c.Server, APIVersion: "v1",
				Kind: "Node", Name: "example", UID: "uid", ResourceVersion: "opaque",
				Unschedulable: false}, out)
			require.Equal(t, []string{"GET /api/v1/nodes/example"}, calls)
		})
	}
}
