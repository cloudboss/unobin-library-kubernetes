package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func statusWorkload(kind string) any {
	obj := restartWorkload(kind, false)
	obj.(runtime.Object).GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{
		Group: "apps", Version: "v1", Kind: kind,
	})
	restartReady(obj, 4)
	return obj
}

func statusAction(kind string) *RolloutStatusAction {
	return &RolloutStatusAction{Kind: kind, Name: "example", Namespace: new("default")}
}

func TestRolloutStatusNamespaces(t *testing.T) {
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
		for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				obj := statusWorkload(kind)
				meta, _ := restartParts(obj)
				meta.Namespace = tc.want
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method+" "+r.URL.RequestURI())
					require.Equal(t, int64(0), r.ContentLength)
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				c.Namespace = tc.configured
				if tc.kubeconfig {
					name, err := filepath.Abs("testdata/restart-context.yaml")
					require.NoError(t, err)
					c.Kubeconfig = &name
				}
				a := statusAction(kind)
				a.Namespace = tc.explicit
				out, err := a.Run(t.Context(), c)
				require.NoError(t, err)
				require.Equal(t, tc.want, out.Namespace)
				resource := map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets",
					"DaemonSet": "daemonsets"}[kind]
				require.Equal(t, []string{
					"GET /apis/apps/v1/namespaces/" + tc.want + "/" + resource + "/example",
				}, calls)
			})
		}
	}
}

func TestRolloutStatusRejectsIdentityAndStrategy(t *testing.T) {
	for _, tc := range []struct {
		name, kind, message string
		change              func(any)
	}{
		{"missing UID", "Deployment", "UID", func(v any) {
			meta, _ := restartParts(v)
			meta.UID = ""
		}},
		{"replaced", "Deployment", "UID", func(v any) {
			meta, _ := restartParts(v)
			meta.UID = "replacement"
		}},
		{"zero generation", "Deployment", "generation", func(v any) {
			meta, _ := restartParts(v)
			meta.Generation = 0
		}},
		{"superseded", "Deployment", "generation", func(v any) {
			meta, _ := restartParts(v)
			meta.Generation++
		}},
		{"wrong name", "Deployment", "identity", func(v any) {
			meta, _ := restartParts(v)
			meta.Name = "different"
		}},
		{"wrong namespace", "Deployment", "identity", func(v any) {
			meta, _ := restartParts(v)
			meta.Namespace = "different"
		}},
		{"wrong kind", "Deployment", "", func(v any) {
			v.(*appsv1.Deployment).Kind = "StatefulSet"
		}},
		{"wrong API version", "Deployment", "", func(v any) {
			v.(*appsv1.Deployment).APIVersion = "apps/v2"
		}},
		{"deleting", "Deployment", "deleting", func(v any) {
			meta, _ := restartParts(v)
			meta.DeletionTimestamp = new(metav1.Now())
		}},
		{"paused", "Deployment", "paused", func(v any) {
			v.(*appsv1.Deployment).Spec.Paused = true
		}},
		{"deadline", "Deployment", "progress deadline", func(v any) {
			v.(*appsv1.Deployment).Status.Conditions = []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded"},
			}
		}},
		{"daemon OnDelete", "DaemonSet", "OnDelete", func(v any) {
			v.(*appsv1.DaemonSet).Spec.UpdateStrategy.Type = "OnDelete"
		}},
		{"stateful OnDelete", "StatefulSet", "OnDelete", func(v any) {
			v.(*appsv1.StatefulSet).Spec.UpdateStrategy.Type = "OnDelete"
		}},
	} {
		for _, later := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/later=%v", tc.name, later), func(t *testing.T) {
				obj := statusWorkload(tc.kind)
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					if !later || len(calls) > 1 {
						restartReady(obj, 4)
						tc.change(obj)
					} else {
						restartReady(obj, 3)
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				a := statusAction(tc.kind)
				a.ExpectedUID, a.ExpectedGeneration = new("uid"), new(int64(4))
				out, err := a.Run(t.Context(), c)
				require.Error(t, err)
				if tc.message != "" {
					require.ErrorContains(t, err, tc.message)
				}
				require.Nil(t, out)
				want := []string{"GET"}
				if later {
					want = append(want, "GET")
				}
				require.Equal(t, want, calls)
			})
		}
	}
}

func TestRolloutStatusOptionalExpectedIdentity(t *testing.T) {
	for _, tc := range []struct {
		uid        *string
		generation *int64
		message    string
	}{
		{}, {uid: new("uid"), generation: new(int64(4))},
		{uid: new("different"), message: "expected UID"},
		{generation: new(int64(5)), message: "expected generation"},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			obj := statusWorkload("Deployment")
			meta, _ := restartParts(obj)
			meta.ResourceVersion = ""
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := statusAction("Deployment")
			a.ExpectedUID, a.ExpectedGeneration = tc.uid, tc.generation
			out, err := a.Run(t.Context(), c)
			if tc.message == "" {
				require.NoError(t, err)
				require.Equal(t, int64(4), out.Generation)
			} else {
				require.ErrorContains(t, err, tc.message)
				require.Nil(t, out)
			}
			require.Equal(t, []string{"GET"}, calls)
		})
	}
}

func TestRolloutStatusPinsWithoutExpectedInputs(t *testing.T) {
	for _, change := range []string{"UID", "generation"} {
		t.Run(change, func(t *testing.T) {
			obj := statusWorkload("Deployment")
			meta, _ := restartParts(obj)
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				restartReady(obj, 3)
				if len(calls) == 2 {
					if change == "UID" {
						meta.UID = "replacement"
					} else {
						meta.Generation++
					}
					restartReady(obj, meta.Generation)
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := statusAction("Deployment").Run(t.Context(), c)
			require.ErrorContains(t, err, change)
			require.Nil(t, out)
			require.Equal(t, []string{"GET", "GET"}, calls)
		})
	}
}

func TestRolloutStatusReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		change     func(any)
	}{
		{"deployment stale", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.ObservedGeneration = 3
		}},
		{"deployment old", "Deployment", func(v any) { v.(*appsv1.Deployment).Status.Replicas = 2 }},
		{"deployment not updated", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.UpdatedReplicas = 0
		}},
		{"deployment unavailable", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.AvailableReplicas = 0
		}},
		{"deployment not ready", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.ReadyReplicas = 0
		}},
		{"daemon stale", "DaemonSet", func(v any) {
			v.(*appsv1.DaemonSet).Status.ObservedGeneration = 3
		}},
		{"daemon not updated", "DaemonSet", func(v any) {
			v.(*appsv1.DaemonSet).Status.UpdatedNumberScheduled = 0
		}},
		{"daemon unavailable", "DaemonSet", func(v any) {
			v.(*appsv1.DaemonSet).Status.NumberAvailable = 0
		}},
		{"daemon not ready", "DaemonSet", func(v any) { v.(*appsv1.DaemonSet).Status.NumberReady = 0 }},
		{"stateful stale", "StatefulSet", func(v any) {
			v.(*appsv1.StatefulSet).Status.ObservedGeneration = 3
		}},
		{"stateful not ready", "StatefulSet", func(v any) {
			v.(*appsv1.StatefulSet).Status.ReadyReplicas = 0
		}},
		{"stateful not updated", "StatefulSet", func(v any) {
			v.(*appsv1.StatefulSet).Status.UpdatedReplicas = 0
		}},
		{"stateful revision", "StatefulSet", func(v any) {
			v.(*appsv1.StatefulSet).Status.CurrentRevision = "old"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := statusWorkload(tc.kind)
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				restartReady(obj, 4)
				if len(calls) == 1 {
					tc.change(obj)
				} else {
					restartReady(obj, 6)
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := statusAction(tc.kind).Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, int64(4), out.Generation)
			require.Equal(t, int64(6), out.ObservedGeneration)
			require.Equal(t, []string{"GET", "GET"}, calls)
		})
	}
}

func TestRolloutStatusStatefulSelection(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		replicas, start, partition *int32
		selected                   int32
		all                        bool
	}{
		{name: "defaults", selected: 1, all: true},
		{name: "zero", replicas: new(int32(0))},
		{name: "full", replicas: new(int32(3)), partition: new(int32(0)), selected: 3, all: true},
		{name: "partial", replicas: new(int32(3)), partition: new(int32(2)), selected: 1},
		{name: "last boundary", replicas: new(int32(3)), partition: new(int32(3))},
		{name: "beyond last", replicas: new(int32(3)), partition: new(int32(9))},
		{name: "start full", replicas: new(int32(3)), start: new(int32(5)),
			partition: new(int32(2)), selected: 3, all: true},
		{name: "start boundary", replicas: new(int32(3)), start: new(int32(5)),
			partition: new(int32(5)), selected: 3, all: true},
		{name: "start partial", replicas: new(int32(3)), start: new(int32(5)),
			partition: new(int32(7)), selected: 1},
		{name: "start none", replicas: new(int32(3)), start: new(int32(5)),
			partition: new(int32(8))},
		{name: "wide ordinals", replicas: new(int32(2147483647)), start: new(int32(2147483647)),
			partition: new(int32(2147483647)), selected: 2147483647, all: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := statusWorkload("StatefulSet").(*appsv1.StatefulSet)
			obj.Spec.Replicas = tc.replicas
			if tc.start != nil {
				obj.Spec.Ordinals = &appsv1.StatefulSetOrdinals{Start: *tc.start}
			}
			if tc.partition != nil {
				obj.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: tc.partition,
				}
			}
			replicas := int32(1)
			if tc.replicas != nil {
				replicas = *tc.replicas
			}
			obj.Status = appsv1.StatefulSetStatus{ObservedGeneration: 4, ReadyReplicas: replicas,
				UpdatedReplicas: tc.selected, CurrentRevision: "old", UpdateRevision: "new"}
			if tc.all {
				obj.Status.CurrentRevision = "new"
			}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := statusAction("StatefulSet").Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, int64(4), out.ObservedGeneration)
			require.Equal(t, []string{"GET"}, calls)
		})
	}
}

func TestRolloutStatusInitialMissingIdentity(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		for _, field := range []string{"UID", "generation"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				obj := statusWorkload(kind)
				meta, _ := restartParts(obj)
				if field == "UID" {
					meta.UID = ""
				} else {
					meta.Generation = 0
				}
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				out, err := statusAction(kind).Run(t.Context(), c)
				require.ErrorContains(t, err, field)
				require.Nil(t, out)
				require.Equal(t, []string{"GET"}, calls)
			})
		}
	}
}

func TestRolloutStatusTransportFailure(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprint(later), func(t *testing.T) {
			obj := statusWorkload("Deployment")
			restartReady(obj, 3)
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if !later || len(calls) > 1 {
					connection, buffer, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
					_, err = buffer.WriteString("invalid HTTP response\r\n\r\n")
					require.NoError(t, err)
					require.NoError(t, buffer.Flush())
					require.NoError(t, connection.Close())
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := statusAction("Deployment").Run(t.Context(), c)
			require.ErrorContains(t, err, "malformed HTTP")
			var transportError *url.Error
			require.ErrorAs(t, err, &transportError)
			require.Nil(t, out)
			want := []string{"GET"}
			if later {
				want = append(want, "GET")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestRolloutStatusInvalidTimeoutConversion(t *testing.T) {
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) { calls = append(calls, r.Method) })
	a := statusAction("Deployment")
	a.Timeout = new("invalid")
	out, err := a.Run(t.Context(), c)
	require.ErrorContains(t, err, "parse timeout")
	require.Nil(t, out)
	require.Empty(t, calls)
}

func TestRolloutStatusZero(t *testing.T) {
	for _, kind := range []string{"Deployment", "DaemonSet"} {
		t.Run(kind, func(t *testing.T) {
			obj := statusWorkload(kind)
			switch o := obj.(type) {
			case *appsv1.Deployment:
				o.Spec.Replicas = new(int32(0))
				o.Spec.Strategy.Type = "Recreate"
				o.Status = appsv1.DeploymentStatus{ObservedGeneration: 4}
			case *appsv1.DaemonSet:
				o.Status = appsv1.DaemonSetStatus{ObservedGeneration: 4}
			}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := statusAction(kind).Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, int64(4), out.Generation)
			require.Equal(t, []string{"GET"}, calls)
		})
	}
}

func TestRolloutStatusAPIError(t *testing.T) {
	for _, code := range []int{403, 404} {
		for _, later := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/later=%v", code, later), func(t *testing.T) {
				obj := statusWorkload("Deployment")
				restartReady(obj, 3)
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					if !later || len(calls) > 1 {
						apiError(t, w, code)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				out, err := statusAction("Deployment").Run(t.Context(), c)
				require.Error(t, err)
				require.Nil(t, out)
				if code == 403 {
					require.True(t, apierrors.IsForbidden(err))
				} else {
					require.True(t, apierrors.IsNotFound(err))
				}
				want := []string{"GET"}
				if later {
					want = append(want, "GET")
				}
				require.Equal(t, want, calls)
			})
		}
	}
}

func TestRolloutStatusCancellation(t *testing.T) {
	for _, stage := range []string{"before", "get", "poll", "ready"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			obj := statusWorkload("Deployment")
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if stage == "get" || stage == "ready" || len(calls) > 1 {
					cancel()
				} else {
					restartReady(obj, 3)
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			if stage == "before" {
				cancel()
			}
			out, err := statusAction("Deployment").Run(ctx, c)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, out)
			want := []string{"GET"}
			switch stage {
			case "before":
				want = nil
			case "poll":
				want = append(want, "GET")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestRolloutStatusDeadlines(t *testing.T) {
	for _, caller := range []bool{false, true} {
		for _, blocking := range []bool{false, true} {
			t.Run(fmt.Sprintf("caller=%v/blocking=%v", caller, blocking), func(t *testing.T) {
				obj := statusWorkload("Deployment")
				restartReady(obj, 3)
				var mu sync.Mutex
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					calls = append(calls, r.Method)
					mu.Unlock()
					if blocking {
						<-r.Context().Done()
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				a := statusAction("Deployment")
				ctx := t.Context()
				if caller {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
					defer cancel()
				} else {
					a.Timeout = new("50ms")
				}
				out, err := a.Run(ctx, c)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Nil(t, out)
				mu.Lock()
				defer mu.Unlock()
				require.Equal(t, []string{"GET"}, calls)
			})
		}
	}
}

func TestRolloutStatusComplete(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		t.Run(kind, func(t *testing.T) {
			obj := statusWorkload(kind)
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := (&RolloutStatusAction{
				Kind: kind, Name: "example", Namespace: new("default"),
			}).Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &RolloutStatusActionOutput{
				Server: *c.Server, APIVersion: "apps/v1", Kind: kind, Name: "example",
				Namespace: "default", UID: "uid", Generation: 4, ObservedGeneration: 4,
			}, out)
			resource := map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets",
				"DaemonSet": "daemonsets"}[kind]
			require.Equal(t, []string{
				"GET /apis/apps/v1/namespaces/default/" + resource + "/example",
			}, calls)
		})
	}
}
