package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDaemonSetRolloutDefaultAndDisabled(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "disabled"}[disabled], func(t *testing.T) {
			gets := 0
			obj := readyDaemonSet(t)
			c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
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
			r := daemonSetWithPod()
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
			p := daemonSetPrior{Inputs: *r, Outputs: created, Configuration: c}
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

func TestDaemonSetWaitFailuresPreserveMutation(t *testing.T) {
	for _, cause := range []string{
		"timeout", "cancellation", "impossible generation", "GET error", "missing",
	} {
		t.Run(cause, func(t *testing.T) {
			var methods []string
			obj := readyDaemonSet(t)
			obj.Status.NumberReady = 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				if req.Method == "GET" {
					switch cause {
					case "cancellation":
						cancel()
					case "impossible generation":
						obj.Status.ObservedGeneration = obj.Generation + 1
					case "missing":
						apiError(t, w, 404)
						return
					case "GET error":
						apiError(t, w, 403)
						return
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := daemonSetWithPod()
			r.CreateTimeout = new("30ms")
			out, err := r.Create(ctx, c)
			require.Error(t, err)
			require.Nil(t, out)
			require.Equal(t, []string{"POST", "GET"}, methods)
			if cause == "timeout" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
		})
	}
}

func TestDaemonSetDeleteWaitsAndFailures(t *testing.T) {
	for _, cause := range []string{"disappears", "initial missing", "replacement UID", "timeout",
		"DELETE error", "GET error", "cancellation"} {
		t.Run(cause, func(t *testing.T) {
			obj := readyDaemonSet(t)
			gets := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
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
			r := daemonSetWithPod()
			r.DeleteTimeout = new("2s")
			if cause == "timeout" {
				r.DeleteTimeout = new("20ms")
			}
			out, err := r.output(readyDaemonSet(t), *c.Server)
			require.NoError(t, err)
			err = r.Delete(ctx, c, daemonSetPrior{Inputs: *r, Outputs: out})
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
			r := daemonSetWithPod()
			switch field {
			case "create-timeout":
				r.CreateTimeout = &invalid
			case "update-timeout":
				r.UpdateTimeout = &invalid
			case "delete-timeout":
				r.DeleteTimeout = &invalid
			}
			require.NotEmpty(t, checkNativeDaemonSetInputs(t, r))
		}
		for _, text := range []string{"0s", "-1s"} {
			_, err := duration(&text, field)
			require.NoError(t, err)
		}
	}
}
