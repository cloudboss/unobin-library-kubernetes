package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPersistentVolumeClaimBindingWait(t *testing.T) {
	for _, phase := range []string{"Bound", "Pending", "Lost", "", "404", "403"} {
		t.Run(phase, func(t *testing.T) {
			reads, events := 0, 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/events") {
					events++
					respond(t, w, &corev1.EventList{})
					return
				}
				obj := livePVC()
				if req.Method == "GET" {
					reads++
					if phase == "404" || phase == "403" {
						code := 404
						if phase == "403" {
							code = 403
						}
						namespaceStatus(t, w, code)
						return
					}
					obj.Status.Phase = corev1.PersistentVolumeClaimPhase(phase)
					if phase == "Pending" && reads > 1 {
						obj.Status.Phase = corev1.ClaimBound
					}
				}
				respond(t, w, obj)
			})
			r := pvcInput()
			r.WaitUntilBound = nil
			out, err := r.Create(t.Context(), c)
			if phase == "Bound" || phase == "Pending" {
				require.NoError(t, err)
				require.NotNil(t, out)
				if phase == "Pending" {
					require.Equal(t, 3, reads)
				} else {
					require.Equal(t, 2, reads)
				}
				require.Zero(t, events)
			} else {
				require.Error(t, err)
				require.Equal(t, 1, reads)
				require.Equal(t, 2, events)
			}
		})
	}
}

func TestPersistentVolumeClaimWarningDiagnostics(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			queries := []string{}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if !strings.HasSuffix(req.URL.Path, "/events") {
					obj := livePVC()
					obj.Status.Phase = corev1.ClaimLost
					obj.Spec.VolumeName = "original-volume"
					respond(t, w, obj)
					return
				}
				selector := strings.Split(req.URL.Query().Get("fieldSelector"), ",")
				slices.Sort(selector)
				queries = append(queries, req.URL.Path+"?"+strings.Join(selector, ","))
				pvc := strings.Contains(req.URL.Path, "namespaces")
				list := &corev1.EventList{ListMeta: metav1.ListMeta{Continue: "ignored"}}
				if !pvc || !fallback {
					for i, message := range []string{"old", "third", "second", "newest", "newest", "normal"} {
						event := corev1.Event{Type: corev1.EventTypeWarning, Message: message, Reason: "Failed",
							InvolvedObject: corev1.ObjectReference{Kind: "Claim", Name: "claim-actual"},
							LastTimestamp:  metav1.NewTime(time.Unix(int64(i), 0))}
						if message == "normal" {
							event.Type = corev1.EventTypeNormal
						}
						list.Items = append(list.Items, event)
					}
				}
				respond(t, w, list)
			})
			r := pvcInput()
			r.WaitUntilBound = nil
			_, err := r.Create(t.Context(), c)
			require.Error(t, err)
			require.ErrorContains(t, err, "Claim claim-actual: Failed: newest")
			require.NotContains(t, err.Error(), ": old")
			require.NotContains(t, err.Error(), ": normal")
			require.Less(t, strings.Index(err.Error(), ": newest"), strings.Index(err.Error(), ": second"))
			require.Less(t, strings.Index(err.Error(), ": second"), strings.Index(err.Error(), ": third"))
			want := []string{
				"/api/v1/namespaces/default/events?involvedObject.kind=PersistentVolumeClaim," +
					"involvedObject.name=claim-actual,involvedObject.namespace=default",
			}
			if fallback {
				want = append(want, "/api/v1/events?involvedObject.kind=PersistentVolume,"+
					"involvedObject.name=original-volume")
			}
			require.Equal(t, want, queries)
		})
	}
}

func TestPersistentVolumeClaimDeadlines(t *testing.T) {
	require.Equal(t, 20*time.Minute, persistentVolumeClaimOperationTimeout)
	for _, timeout := range []string{"0s", "-1s", "25ms"} {
		t.Run(timeout, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				select {
				case <-req.Context().Done():
				case <-time.After(100 * time.Millisecond):
				}
			})
			r := pvcInput()
			r.CreateTimeout = &timeout
			start := time.Now()
			_, err := r.Create(t.Context(), c)
			require.Error(t, err)
			require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
			require.Less(t, time.Since(start), time.Second)
		})
	}
	for _, operation := range []string{"read", "update", "delete", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if operation == "refresh" && req.Method == "POST" {
					respond(t, w, livePVC())
					return
				}
				select {
				case <-req.Context().Done():
				case <-time.After(100 * time.Millisecond):
				}
			})
			r := pvcInput()
			p := persistentVolumeClaimPrior{Inputs: *pvcInput(), Outputs: savedPVC(*c.Server)}
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			var err error
			switch operation {
			case "read":
				_, err = r.Read(ctx, c, p)
			case "update":
				r.Object.Metadata.Labels = &map[string]string{"key": "value"}
				_, err = r.Update(ctx, c, p)
			case "delete":
				err = r.Delete(ctx, c, p)
			case "refresh":
				_, err = r.Create(ctx, c)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestPersistentVolumeClaimMutationAndDiagnosticErrors(t *testing.T) {
	for _, operation := range []string{"create", "patch", "diagnostic"} {
		t.Run(operation, func(t *testing.T) {
			mutations, queries := 0, 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "events") {
					queries++
					namespaceStatus(t, w, 403)
					return
				}
				if req.Method == "POST" || req.Method == "PATCH" {
					mutations++
					if operation != "diagnostic" {
						namespaceStatus(t, w, 409)
						return
					}
				}
				obj := livePVC()
				obj.Status.Phase = corev1.ClaimLost
				respond(t, w, obj)
			})
			r := pvcInput()
			var err error
			if operation == "patch" {
				r.Object.Metadata.Labels = &map[string]string{"key": "value"}
				_, err = r.Update(t.Context(), c, persistentVolumeClaimPrior{Inputs: *pvcInput(),
					Outputs: savedPVC(*c.Server)})
			} else {
				r.WaitUntilBound = nil
				_, err = r.Create(t.Context(), c)
			}
			require.Error(t, err)
			require.Equal(t, 1, mutations)
			if operation == "diagnostic" {
				require.ErrorContains(t, err, "Events")
				require.Equal(t, 1, queries)
			}
		})
	}
}

func TestPersistentVolumeClaimFailedCreateDoesNotAdopt(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	exists := false
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == "POST" {
			if exists {
				namespaceStatus(t, w, 409)
				return
			}
			exists = true
		}
		if strings.HasSuffix(req.URL.Path, "events") {
			respond(t, w, &corev1.EventList{})
			return
		}
		obj := livePVC()
		obj.Status.Phase = corev1.ClaimPending
		respond(t, w, obj)
		if req.Method == "GET" {
			w.(http.Flusher).Flush()
			cancel()
		}
	})
	r := pvcInput()
	r.WaitUntilBound = nil
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		TLSHandshakeStart: func() { time.Sleep(50 * time.Millisecond) },
	})
	out, err := r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)
	require.True(t, exists)
	out, err = r.Create(t.Context(), c)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Nil(t, out)
	require.Equal(t, []string{
		"POST /api/v1/namespaces/default/persistentvolumeclaims",
		"GET /api/v1/namespaces/default/persistentvolumeclaims/claim-actual",
		"POST /api/v1/namespaces/default/persistentvolumeclaims",
	}, calls)
}

func TestPersistentVolumeClaimEmptyVolumeWarningFallback(t *testing.T) {
	queries := []string{}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "events") {
			selector := strings.Split(req.URL.Query().Get("fieldSelector"), ",")
			slices.Sort(selector)
			queries = append(queries, req.URL.Path+"?"+strings.Join(selector, ","))
			respond(t, w, &corev1.EventList{})
			return
		}
		obj := livePVC()
		obj.Status.Phase = corev1.ClaimLost
		respond(t, w, obj)
	})
	r := pvcInput()
	r.WaitUntilBound = nil
	_, err := r.Create(t.Context(), c)
	require.Error(t, err)
	require.Equal(t, []string{
		"/api/v1/namespaces/default/events?involvedObject.kind=PersistentVolumeClaim," +
			"involvedObject.name=claim-actual,involvedObject.namespace=default",
		"/api/v1/events?involvedObject.kind=PersistentVolume,involvedObject.name=",
	}, queries)
	r.CreateTimeout = new("bad duration")
	_, err = r.Create(t.Context(), c)
	require.ErrorContains(t, err, "parse create-timeout")
}
