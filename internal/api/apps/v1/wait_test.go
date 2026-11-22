package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func readyDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	obj, err := deployment().expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	obj.Generation = 2
	obj.Spec.Strategy.Type = "RollingUpdate"
	obj.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 2, UpdatedReplicas: 1, Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
	}
	return obj
}

func TestRolloutPredicates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*appsv1.Deployment)
		ready  bool
		err    string
	}{
		{"ready nil desired", func(*appsv1.Deployment) {}, true, ""},
		{"generation pending", func(o *appsv1.Deployment) { o.Generation++ }, false, ""},
		{"impossible generation", func(o *appsv1.Deployment) { o.Status.ObservedGeneration++ },
			false, "exceeds"},
		{"updated pending", func(o *appsv1.Deployment) { o.Spec.Replicas = new(int32(2)) }, false, ""},
		{"old replicas", func(o *appsv1.Deployment) { o.Status.Replicas++ }, false, ""},
		{"ready pending", func(o *appsv1.Deployment) { o.Status.ReadyReplicas = 0 }, false, ""},
		{"available pending", func(o *appsv1.Deployment) { o.Status.AvailableReplicas = 0 }, false, ""},
		{"zero replicas", func(o *appsv1.Deployment) {
			o.Spec.Replicas = new(int32(0))
			o.Status = appsv1.DeploymentStatus{ObservedGeneration: 2}
		}, true, ""},
		{"deadline", func(o *appsv1.Deployment) {
			o.Status.Conditions = []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded",
			}}
		}, false, "progress deadline"},
		{"first progressing wins", func(o *appsv1.Deployment) {
			o.Status.Conditions = []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Reason: "ProgressDeadlineExceeded"},
				{Type: appsv1.DeploymentProgressing, Reason: "NewReplicaSetAvailable"},
				{Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded"},
			}
		}, true, ""},
		{"deadline deferred by generation", func(o *appsv1.Deployment) {
			o.Generation++
			o.Status.Conditions = []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded",
			}}
		}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := readyDeployment(t)
			tc.change(obj)
			ready, err := rolloutReady(obj)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.ready, ready)
		})
	}
}

func TestRolloutDefaultAndDisabled(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "disabled"}[disabled], func(t *testing.T) {
			gets := 0
			obj := readyDeployment(t)
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "GET" {
					gets++
					if gets < 2 {
						pending := obj.DeepCopy()
						pending.Status.ObservedGeneration = 0
						require.NoError(t, json.NewEncoder(w).Encode(pending))
						return
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := deployment()
			r.CreateTimeout, r.UpdateTimeout = new("2s"), new("2s")
			if disabled {
				r.WaitForRollout = new(false)
			}
			created, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			if disabled {
				require.Equal(t, 1, gets)
			} else {
				require.Equal(t, 3, gets)
			}
			gets = 0
			p := prior{Inputs: *r, Outputs: created, Configuration: c}
			r.Metadata.Labels = new(map[string]string{"changed": "yes"})
			_, err = r.Update(t.Context(), c, p)
			require.NoError(t, err)
			if disabled {
				require.Equal(t, 2, gets)
			} else {
				require.GreaterOrEqual(t, gets, 3)
			}
		})
	}
}

func TestWaitFailuresPreserveMutation(t *testing.T) {
	for _, cause := range []string{"timeout", "cancellation", "deadline", "GET error"} {
		t.Run(cause, func(t *testing.T) {
			var methodsMu sync.Mutex
			var methods []string
			obj := readyDeployment(t)
			obj.Status.UpdatedReplicas = 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methodsMu.Lock()
				methods = append(methods, req.Method)
				methodsMu.Unlock()
				if req.Method == "GET" {
					switch cause {
					case "timeout":
						<-req.Context().Done()
						return
					case "cancellation":
						cancel()
					case "deadline":
						obj.Status.Conditions = []appsv1.DeploymentCondition{{
							Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded",
						}}
					case "GET error":
						apiError(t, w, 403)
						return
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := deployment()
			r.CreateTimeout = new("5s")
			if cause == "timeout" {
				r.CreateTimeout = new("1s")
			}
			out, err := r.Create(ctx, c)
			require.Error(t, err)
			require.Nil(t, out)
			methodsMu.Lock()
			defer methodsMu.Unlock()
			require.Equal(t, []string{"POST", "GET"}, methods)
			if cause == "timeout" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			if cause == "deadline" {
				require.ErrorContains(t, err, "progress deadline")
			}
		})
	}
}

func TestDeleteWaitsAndFailures(t *testing.T) {
	for _, cause := range []string{"disappears", "initial missing", "replacement UID", "timeout",
		"DELETE error", "GET error", "cancellation"} {
		t.Run(cause, func(t *testing.T) {
			obj := readyDeployment(t)
			gets := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "DELETE" {
					if cause == "initial missing" {
						apiError(t, w, 404)
						return
					}
					if cause == "DELETE error" {
						apiError(t, w, 403)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
					return
				}
				gets++
				if cause == "GET error" {
					apiError(t, w, 403)
					return
				}
				if cause == "disappears" && gets == 3 {
					apiError(t, w, 404)
					return
				}
				if cause == "replacement UID" {
					obj.UID = "replacement"
				}
				if cause == "cancellation" {
					cancel()
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := deployment()
			r.DeleteTimeout = new("2s")
			if cause == "timeout" {
				r.DeleteTimeout = new("20ms")
			}
			out, err := r.output(readyDeployment(t), *c.Server)
			require.NoError(t, err)
			err = r.Delete(ctx, c, prior{Inputs: *r, Outputs: out})
			if cause == "disappears" || cause == "initial missing" || cause == "replacement UID" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	for _, field := range []string{"create-timeout", "update-timeout", "delete-timeout"} {
		value, err := duration(nil, field)
		require.NoError(t, err)
		require.Equal(t, 10*time.Minute, value)
		for _, invalid := range []string{"", "0s", "-1s", "not-a-duration"} {
			r := deployment()
			switch field {
			case "create-timeout":
				r.CreateTimeout = &invalid
			case "update-timeout":
				r.UpdateTimeout = &invalid
			case "delete-timeout":
				r.DeleteTimeout = &invalid
			}
			require.NotEmpty(t, checkNativeDeploymentInputs(t, r))
		}
		for _, text := range []string{"0s", "-1s"} {
			_, err := duration(&text, field)
			require.NoError(t, err)
		}
	}
}
