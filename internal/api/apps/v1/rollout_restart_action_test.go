package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const restartTime = "2026-11-26T12:00:00Z"

func restartAction(kind string) *RolloutRestartAction {
	return &RolloutRestartAction{Kind: kind, Name: "example", Namespace: new("default"),
		ExpectedUID: "uid", RestartAt: restartTime, WaitForRollout: new(false)}
}

func restartWorkload(kind string, annotations bool) any {
	meta := metav1.ObjectMeta{Name: "example", Namespace: "default", UID: "uid",
		ResourceVersion: "opaque-version", Generation: 4, Labels: map[string]string{"outer": "keep"}}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{"app": "keep"}}, Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app", Image: "pause"}}}}
	if annotations {
		template.Annotations = map[string]string{"unrelated": "keep"}
	}
	switch kind {
	case "Deployment":
		return &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{
			Template: template, Replicas: new(int32(1)),
			Strategy: appsv1.DeploymentStrategy{Type: "RollingUpdate"}}}
	case "DaemonSet":
		return &appsv1.DaemonSet{ObjectMeta: meta, Spec: appsv1.DaemonSetSpec{
			Template: template, UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: "RollingUpdate"}}}
	default:
		return &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{
			Template: template, Replicas: new(int32(1)),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: "RollingUpdate"}}}
	}
}

func TestRolloutRestartPatch(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		for _, state := range []string{"absent", "empty", "present"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				annotations := state == "present"
				obj := restartWorkload(kind, annotations)
				if state == "empty" {
					_, values := restartParts(obj)
					*values = map[string]string{}
				}
				original, err := json.Marshal(obj)
				require.NoError(t, err)
				var document map[string]any
				require.NoError(t, json.Unmarshal(original, &document))
				template := document["spec"].(map[string]any)["template"].(map[string]any)
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					resource := map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets",
						"DaemonSet": "daemonsets"}[kind]
					require.Equal(t, "/apis/apps/v1/namespaces/default/"+resource+"/example", r.URL.Path)
					require.Empty(t, r.URL.RawQuery)
					calls = append(calls, r.Method+" "+r.URL.Path)
					if r.Method == http.MethodPatch {
						require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
						body, err := io.ReadAll(r.Body)
						require.NoError(t, err)
						var operations []patchOperation
						require.NoError(t, json.Unmarshal(body, &operations))
						value := any(restartTime)
						path := "/spec/template/metadata/annotations/kubectl.kubernetes.io~1restartedAt"
						if !annotations {
							path = "/spec/template/metadata/annotations"
							value = map[string]any{rolloutRestartAnnotation: restartTime}
						}
						require.Equal(t, []patchOperation{
							{Op: "test", Path: "/metadata/uid", Value: "uid"},
							{Op: "test", Path: "/metadata/generation", Value: float64(4)},
							{Op: "test", Path: "/spec/template/metadata", Value: template["metadata"]},
							{Op: "add", Path: path, Value: value}}, operations)
						patch, err := jsonpatch.DecodePatch(body)
						require.NoError(t, err)
						after, err := patch.Apply(original)
						require.NoError(t, err)
						require.NoError(t, json.Unmarshal(after, obj))
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				out, err := restartAction(kind).Run(t.Context(), c)
				require.NoError(t, err)
				require.Equal(t, &RolloutRestartActionOutput{Server: *c.Server, APIVersion: "apps/v1",
					Kind: kind, Name: "example", Namespace: "default", UID: "uid", RestartAt: restartTime,
					Generation: 4, Strategy: "RollingUpdate",
					Partition:        map[bool]*int64{true: new(int64(0))}[kind == "StatefulSet"],
					SelectedReplicas: map[bool]*int64{true: new(int64(1))}[kind == "StatefulSet"]}, out)
				resource := map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets",
					"DaemonSet": "daemonsets"}[kind]
				path := "/apis/apps/v1/namespaces/default/" + resource + "/example"
				require.Equal(t, []string{"GET " + path, "PATCH " + path}, calls)
				_, values := restartParts(obj)
				delete(*values, rolloutRestartAnnotation)
				if !annotations {
					*values = nil
				}
				restored, err := json.Marshal(obj)
				require.NoError(t, err)
				require.JSONEq(t, string(original), string(restored))
			})
		}
	}
}

func restartParts(obj any) (*metav1.ObjectMeta, *map[string]string) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return &o.ObjectMeta, &o.Spec.Template.Annotations
	case *appsv1.DaemonSet:
		return &o.ObjectMeta, &o.Spec.Template.Annotations
	default:
		value := obj.(*appsv1.StatefulSet)
		return &value.ObjectMeta, &value.Spec.Template.Annotations
	}
}

func restartReady(obj any, observed int64) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		o.Status = appsv1.DeploymentStatus{ObservedGeneration: observed, Replicas: 1, UpdatedReplicas: 1,
			ReadyReplicas: 1, AvailableReplicas: 1}
	case *appsv1.DaemonSet:
		o.Status = appsv1.DaemonSetStatus{ObservedGeneration: observed, DesiredNumberScheduled: 1,
			UpdatedNumberScheduled: 1, NumberReady: 1, NumberAvailable: 1}
	case *appsv1.StatefulSet:
		o.Status = appsv1.StatefulSetStatus{ObservedGeneration: observed, Replicas: 1, ReadyReplicas: 1,
			UpdatedReplicas: 1, CurrentRevision: "new", UpdateRevision: "new"}
	}
}

func TestRolloutRestartPatchAllowsStatusRace(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		t.Run(kind, func(t *testing.T) {
			obj := restartWorkload(kind, true)
			meta, annotations := restartParts(obj)
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.Method == http.MethodPatch {
					meta.ResourceVersion = "controller-status-version"
					restartReady(obj, meta.Generation)
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					before, err := json.Marshal(obj)
					require.NoError(t, err)
					after, err := patch.Apply(before)
					if err != nil {
						apiError(t, w, 422)
						return
					}
					require.NoError(t, json.Unmarshal(after, obj))
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := restartAction(kind).Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, restartTime, out.RestartAt)
			require.Equal(t, []string{"GET", "PATCH"}, calls)
			require.Equal(t, map[string]string{
				"unrelated": "keep", rolloutRestartAnnotation: restartTime,
			}, *annotations)
			require.Equal(t, "controller-status-version", meta.ResourceVersion)
			got, err := json.Marshal(obj)
			require.NoError(t, err)
			expected := restartWorkload(kind, true)
			expectedMeta, expectedAnnotations := restartParts(expected)
			expectedMeta.ResourceVersion = "controller-status-version"
			(*expectedAnnotations)[rolloutRestartAnnotation] = restartTime
			restartReady(expected, expectedMeta.Generation)
			want, err := json.Marshal(expected)
			require.NoError(t, err)
			require.JSONEq(t, string(want), string(got))
		})
	}
}

func TestRolloutRestartTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name, current, requested string
		patch                    bool
		problem                  string
	}{
		{name: "absent", requested: restartTime, patch: true},
		{name: "malformed", current: "old text", requested: restartTime, patch: true},
		{name: "older live", current: "2026-11-25T12:00:00Z", requested: restartTime, patch: true},
		{name: "same", current: restartTime, requested: restartTime},
		{name: "equivalent zone", current: "2026-11-26T07:00:00-05:00", requested: restartTime},
		{name: "canonical fractions", requested: "2026-11-26T07:00:00.123-05:00", patch: true},
		{name: "superseded", current: "2026-11-27T12:00:00Z", requested: restartTime,
			problem: "superseded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := restartWorkload("Deployment", true)
			_, annotations := restartParts(obj)
			(*annotations)[rolloutRestartAnnotation] = tc.current
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.Method == "PATCH" {
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					before, err := json.Marshal(obj)
					require.NoError(t, err)
					after, err := patch.Apply(before)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(after, obj))
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := restartAction("Deployment")
			a.RestartAt = tc.requested
			out, err := a.Run(t.Context(), c)
			if tc.problem != "" {
				require.ErrorContains(t, err, tc.problem)
				require.Nil(t, out)
			} else {
				require.NoError(t, err)
				requested, err := time.Parse(time.RFC3339, tc.requested)
				require.NoError(t, err)
				require.Equal(t, requested.UTC().Format(time.RFC3339Nano), out.RestartAt)
				if !tc.patch {
					require.Equal(t, tc.current, (*annotations)[rolloutRestartAnnotation])
				}
			}
			want := []string{"GET"}
			if tc.patch {
				want = append(want, "PATCH")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestRolloutRestartPreconditions(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		for _, problem := range []string{"missing", "UID", "deleting", "empty UID", "version",
			"generation",
			"paused", "paused waiting", "OnDelete waiting", "OnDelete staging"} {
			if strings.HasPrefix(problem, "paused") && kind != "Deployment" ||
				strings.HasPrefix(problem, "OnDelete") && kind == "Deployment" {
				continue
			}
			t.Run(kind+"/"+problem, func(t *testing.T) {
				obj := restartWorkload(kind, true)
				meta, annotations := restartParts(obj)
				a := restartAction(kind)
				switch problem {
				case "UID":
					meta.UID = "other"
				case "empty UID":
					meta.UID = ""
				case "deleting":
					meta.DeletionTimestamp = new(metav1.Now())
				case "version":
					meta.ResourceVersion = ""
				case "generation":
					meta.Generation = 0
				case "paused", "paused waiting":
					obj.(*appsv1.Deployment).Spec.Paused = true
					a.WaitForRollout = new(problem == "paused waiting")
				case "OnDelete waiting", "OnDelete staging":
					if o, ok := obj.(*appsv1.StatefulSet); ok {
						o.Spec.UpdateStrategy.Type = "OnDelete"
					} else {
						obj.(*appsv1.DaemonSet).Spec.UpdateStrategy.Type = "OnDelete"
					}
					a.WaitForRollout = new(problem == "OnDelete waiting")
				}
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method)
					if problem == "missing" {
						apiError(t, w, 404)
						return
					}
					if r.Method == "PATCH" {
						(*annotations)[rolloutRestartAnnotation] = restartTime
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				})
				out, err := a.Run(t.Context(), c)
				if problem == "OnDelete staging" {
					require.NoError(t, err)
					require.False(t, out.Waited)
					require.Equal(t, []string{"GET", "PATCH"}, calls)
				} else {
					require.Error(t, err)
					require.Nil(t, out)
					require.Equal(t, []string{"GET"}, calls)
				}
			})
		}
	}
}

func TestRolloutRestartPatchFailures(t *testing.T) {
	for _, problem := range []string{"admission", "forbidden", "conflict",
		"dropped marker", "rewritten marker", "response UID", "response generation", "response version"} {
		t.Run(problem, func(t *testing.T) {
			obj := restartWorkload("Deployment", true)
			meta, annotations := restartParts(obj)
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.Method == "PATCH" {
					switch problem {
					case "admission":
						apiError(t, w, 422)
						return
					case "forbidden":
						apiError(t, w, 403)
						return
					case "conflict":
						apiError(t, w, 409)
						return
					case "dropped marker":
					case "rewritten marker":
						(*annotations)[rolloutRestartAnnotation] = "2026-11-27T00:00:00Z"
					default:
						(*annotations)[rolloutRestartAnnotation] = restartTime
						switch problem {
						case "response UID":
							meta.UID = "replacement"
						case "response generation":
							meta.Generation = 0
						case "response version":
							meta.ResourceVersion = ""
						}
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := restartAction("Deployment").Run(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			require.ErrorContains(t, err, "patch Deployment default/example restart-at "+restartTime)
			require.Equal(t, []string{"GET", "PATCH"}, calls)
		})
	}
}

func TestRolloutRestartPatchRejectsTargetRace(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		for _, state := range []string{"absent", "empty", "present"} {
			for _, problem := range []string{"UID", "generation", "marker", "labels"} {
				t.Run(kind+"/"+state+"/"+problem, func(t *testing.T) {
					obj := restartWorkload(kind, state == "present")
					meta, annotations := restartParts(obj)
					if state == "empty" {
						*annotations = map[string]string{}
					}
					var calls []string
					var concurrent []byte
					c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
						calls = append(calls, r.Method)
						if r.Method == http.MethodPatch {
							switch problem {
							case "UID":
								meta.UID = "replacement"
							case "generation":
								meta.Generation++
							case "marker":
								if *annotations == nil {
									*annotations = map[string]string{}
								}
								(*annotations)[rolloutRestartAnnotation] = "2026-11-27T00:00:00Z"
							case "labels":
								switch o := obj.(type) {
								case *appsv1.Deployment:
									o.Spec.Template.Labels["concurrent"] = "keep"
								case *appsv1.StatefulSet:
									o.Spec.Template.Labels["concurrent"] = "keep"
								case *appsv1.DaemonSet:
									o.Spec.Template.Labels["concurrent"] = "keep"
								}
							}
							body, err := io.ReadAll(r.Body)
							require.NoError(t, err)
							patch, err := jsonpatch.DecodePatch(body)
							require.NoError(t, err)
							concurrent, err = json.Marshal(obj)
							require.NoError(t, err)
							_, err = patch.Apply(concurrent)
							require.Error(t, err)
							apiError(t, w, 422)
							return
						}
						require.NoError(t, json.NewEncoder(w).Encode(obj))
					})
					out, err := restartAction(kind).Run(t.Context(), c)
					require.ErrorContains(t, err, "patch "+kind+" default/example")
					require.Nil(t, out)
					require.Equal(t, []string{"GET", "PATCH"}, calls)
					after, err := json.Marshal(obj)
					require.NoError(t, err)
					require.JSONEq(t, string(concurrent), string(after))
				})
			}
		}
	}
}

func TestRolloutRestartWaitOrder(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		t.Run(kind, func(t *testing.T) {
			obj := restartWorkload(kind, true)
			meta, annotations := restartParts(obj)
			var calls []string
			polls := 0
			accepted := false
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.Method == "PATCH" {
					accepted = true
					meta.Generation = 5
					(*annotations)[rolloutRestartAnnotation] = restartTime
				}
				if r.Method == "GET" && accepted {
					polls++
					meta.ResourceVersion = fmt.Sprint(polls)
					restartReady(obj, 4)
					if polls >= 2 {
						restartReady(obj, 5)
					}
					if polls == 2 {
						switch o := obj.(type) {
						case *appsv1.Deployment:
							o.Status.AvailableReplicas = 0
						case *appsv1.DaemonSet:
							o.Status.NumberAvailable = 0
						case *appsv1.StatefulSet:
							o.Status.CurrentRevision = "old"
						}
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := restartAction(kind)
			a.WaitForRollout = nil
			out, err := a.Run(t.Context(), c)
			require.NoError(t, err)
			require.True(t, out.Waited)
			require.Equal(t, int64(5), out.Generation)
			require.Equal(t, []string{"GET", "PATCH", "GET", "GET", "GET"}, calls)
		})
	}
}

func TestRolloutRestartWaitRejectsChangedTarget(t *testing.T) {
	for _, problem := range []string{"missing", "UID", "deleting", "generation", "marker",
		"paused", "deadline"} {
		t.Run(problem, func(t *testing.T) {
			obj := restartWorkload("Deployment", true)
			meta, annotations := restartParts(obj)
			(*annotations)[rolloutRestartAnnotation] = restartTime
			restartReady(obj, 4)
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if len(calls) > 1 {
					switch problem {
					case "missing":
						apiError(t, w, 404)
						return
					case "UID":
						meta.UID = "replacement"
					case "deleting":
						meta.DeletionTimestamp = new(metav1.Now())
					case "generation":
						meta.Generation++
					case "marker":
						(*annotations)[rolloutRestartAnnotation] = "2026-11-27T00:00:00Z"
					case "paused":
						obj.(*appsv1.Deployment).Spec.Paused = true
					case "deadline":
						obj.(*appsv1.Deployment).Status.Conditions = []appsv1.DeploymentCondition{
							{Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded"}}
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := restartAction("Deployment")
			a.WaitForRollout = nil
			out, err := a.Run(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			require.ErrorContains(t, err, "wait Deployment default/example")
			require.Equal(t, []string{"GET", "GET"}, calls)
		})
	}
}

func TestRolloutRestartStatefulSelection(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		replicas, start, partition *int32
		selected                   int64
		revisionEqual              bool
	}{
		{name: "defaults", selected: 1, revisionEqual: true},
		{name: "zero partition", replicas: new(int32(3)), partition: new(int32(0)), selected: 3,
			revisionEqual: true},
		{name: "partial", replicas: new(int32(3)), partition: new(int32(2)), selected: 1},
		{name: "beyond last", replicas: new(int32(3)), partition: new(int32(3)), selected: 0},
		{name: "nonzero start full", replicas: new(int32(3)), start: new(int32(5)),
			partition: new(int32(2)), selected: 3, revisionEqual: true},
		{name: "nonzero start partial", replicas: new(int32(3)), start: new(int32(5)),
			partition: new(int32(7)), selected: 1},
		{name: "zero replicas", replicas: new(int32(0)), selected: 0},
		{name: "wide ordinals", replicas: new(int32(2147483647)), start: new(int32(2147483647)),
			partition: new(int32(2147483647)), selected: 2147483647, revisionEqual: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := restartWorkload("StatefulSet", true).(*appsv1.StatefulSet)
			obj.Spec.Replicas = tc.replicas
			if tc.start != nil {
				obj.Spec.Ordinals = &appsv1.StatefulSetOrdinals{Start: *tc.start}
			}
			if tc.partition != nil {
				obj.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: tc.partition,
				}
			}
			obj.Spec.Template.Annotations[rolloutRestartAnnotation] = restartTime
			replicas := int32(1)
			if tc.replicas != nil {
				replicas = *tc.replicas
			}
			obj.Status = appsv1.StatefulSetStatus{ObservedGeneration: 4, ReadyReplicas: replicas,
				UpdatedReplicas: int32(tc.selected), CurrentRevision: "old", UpdateRevision: "new"}
			if tc.revisionEqual {
				obj.Status.CurrentRevision = "new"
			}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := restartAction("StatefulSet")
			a.WaitForRollout = nil
			out, err := a.Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, new(tc.selected), out.SelectedReplicas)
			partition := int64(0)
			if tc.partition != nil {
				partition = int64(*tc.partition)
			}
			require.Equal(t, &partition, out.Partition)
			require.True(t, out.Waited)
			require.Equal(t, []string{"GET", "GET"}, calls)
		})
	}
}

func TestRolloutRestartReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		change     func(any)
	}{
		{"deployment old replicas", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.Replicas = 2
		}},
		{"deployment unready", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.ReadyReplicas = 0
		}},
		{"deployment unavailable", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.AvailableReplicas = 0
		}},
		{"deployment not updated", "Deployment", func(v any) {
			v.(*appsv1.Deployment).Status.UpdatedReplicas = 0
		}},
		{"daemon not updated", "DaemonSet", func(v any) {
			v.(*appsv1.DaemonSet).Status.UpdatedNumberScheduled = 0
		}},
		{"daemon not ready", "DaemonSet", func(v any) { v.(*appsv1.DaemonSet).Status.NumberReady = 0 }},
		{"daemon not available", "DaemonSet", func(v any) {
			v.(*appsv1.DaemonSet).Status.NumberAvailable = 0
		}},
		{"stateful not ready", "StatefulSet", func(v any) {
			v.(*appsv1.StatefulSet).Status.ReadyReplicas = 0
		}},
		{"stateful not updated", "StatefulSet", func(v any) {
			v.(*appsv1.StatefulSet).Status.UpdatedReplicas = 0
		}},
		{"stateful empty revisions", "StatefulSet", func(v any) {
			o := v.(*appsv1.StatefulSet)
			o.Status.CurrentRevision = ""
			o.Status.UpdateRevision = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := restartWorkload(tc.kind, true)
			restartReady(obj, 4)
			tc.change(obj)
			_, annotations := restartParts(obj)
			(*annotations)[rolloutRestartAnnotation] = restartTime
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := restartAction(tc.kind)
			a.WaitForRollout = nil
			a.Timeout = new("20ms")
			out, err := a.Run(t.Context(), c)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Nil(t, out)
			require.Equal(t, []string{"GET", "GET"}, calls)
		})
	}
}

func TestRolloutRestartTimeoutResume(t *testing.T) {
	obj := restartWorkload("Deployment", true)
	_, annotations := restartParts(obj)
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == "PATCH" {
			(*annotations)[rolloutRestartAnnotation] = restartTime
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	a := restartAction("Deployment")
	a.WaitForRollout = nil
	a.Timeout = new("20ms")
	out, err := a.Run(t.Context(), c)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, out)
	require.Equal(t, restartTime, (*annotations)[rolloutRestartAnnotation])
	require.Equal(t, []string{"GET", "PATCH", "GET"}, calls)
	calls = nil
	restartReady(obj, 4)
	a.Timeout = nil
	out, err = a.Run(t.Context(), c)
	require.NoError(t, err)
	require.True(t, out.Waited)
	require.Equal(t, []string{"GET", "GET"}, calls)
}

func TestRolloutRestartNamespaces(t *testing.T) {
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
			obj := restartWorkload("Deployment", true)
			obj.(*appsv1.Deployment).Spec.Template.Annotations[rolloutRestartAnnotation] = restartTime
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			c.Namespace = tc.configured
			if tc.kubeconfig {
				name, err := filepath.Abs("testdata/restart-context.yaml")
				require.NoError(t, err)
				c.Kubeconfig = &name
			}
			a := restartAction("Deployment")
			a.Namespace = tc.explicit
			out, err := a.Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, tc.want, out.Namespace)
			require.Equal(t,
				[]string{"GET /apis/apps/v1/namespaces/" + tc.want + "/deployments/example"}, calls)
		})
	}
}

func TestRolloutRestartCancellation(t *testing.T) {
	t.Run("before remote work", func(t *testing.T) {
		var calls []string
		c := testAPI(t, func(w http.ResponseWriter, r *http.Request) { calls = append(calls, r.Method) })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out, err := restartAction("Deployment").Run(ctx, c)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, out)
		require.Empty(t, calls)
	})
	for _, during := range []string{"get", "patch", "poll"} {
		t.Run(during, func(t *testing.T) {
			obj := restartWorkload("Deployment", true)
			_, annotations := restartParts(obj)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := []string{}
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.Method == "PATCH" {
					(*annotations)[rolloutRestartAnnotation] = restartTime
				}
				block := during == "get" || during == "patch" && r.Method == "PATCH" ||
					during == "poll" && len(calls) == 3
				if block {
					cancel()
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := restartAction("Deployment")
			a.WaitForRollout = nil
			out, err := a.Run(ctx, c)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, out)
			want := []string{"GET"}
			if during != "get" {
				want = append(want, "PATCH")
			}
			if during == "poll" {
				want = append(want, "GET")
			}
			require.Equal(t, want, calls)
			if during != "get" {
				require.Equal(t, restartTime, (*annotations)[rolloutRestartAnnotation])
			}
		})
	}
}

func TestRolloutRestartCallerDeadline(t *testing.T) {
	obj := restartWorkload("Deployment", true)
	obj.(*appsv1.Deployment).Spec.Template.Annotations[rolloutRestartAnnotation] = restartTime
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	a := restartAction("Deployment")
	a.WaitForRollout = nil
	_, err := a.Run(ctx, c)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, timeout, err := prepareRestart(a)
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, timeout)
}

func TestRolloutRestartZeroAndRecreate(t *testing.T) {
	for _, kind := range []string{"Deployment", "DaemonSet", "StatefulSet"} {
		t.Run(kind, func(t *testing.T) {
			obj := restartWorkload(kind, true)
			_, annotations := restartParts(obj)
			(*annotations)[rolloutRestartAnnotation] = restartTime
			switch o := obj.(type) {
			case *appsv1.Deployment:
				o.Spec.Strategy.Type = "Recreate"
				o.Spec.Replicas = new(int32(0))
				o.Status.ObservedGeneration = 4
			case *appsv1.DaemonSet:
				o.Status.ObservedGeneration = 4
			case *appsv1.StatefulSet:
				o.Spec.Replicas = new(int32(0))
				o.Status.ObservedGeneration = 4
			}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			a := restartAction(kind)
			a.WaitForRollout = nil
			out, err := a.Run(t.Context(), c)
			require.NoError(t, err)
			require.True(t, out.Waited)
			require.Equal(t, []string{"GET", "GET"}, calls)
		})
	}
}

func TestRolloutRestartTransportFailure(t *testing.T) {
	obj := restartWorkload("Deployment", true)
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == "PATCH" {
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := restartAction("Deployment").Run(t.Context(), c)
	require.ErrorContains(t, err, "patch Deployment default/example")
	require.Nil(t, out)
	require.Equal(t, []string{"GET", "PATCH"}, calls)
}

func TestRolloutRestartRetryRefetchesAfterConflict(t *testing.T) {
	obj := restartWorkload("Deployment", true)
	meta, annotations := restartParts(obj)
	var calls []string
	failed := false
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method)
		if r.Method == "PATCH" {
			if !failed {
				failed = true
				meta.ResourceVersion = "concurrent-version"
				meta.Generation++
				(*annotations)["concurrent"] = "keep"
				apiError(t, w, 409)
				return
			}
			var operations []patchOperation
			require.NoError(t, json.NewDecoder(r.Body).Decode(&operations))
			before, err := json.Marshal(obj)
			require.NoError(t, err)
			var document map[string]any
			require.NoError(t, json.Unmarshal(before, &document))
			template := document["spec"].(map[string]any)["template"].(map[string]any)
			require.Equal(t, []patchOperation{
				{Op: "test", Path: "/metadata/uid", Value: "uid"},
				{Op: "test", Path: "/metadata/generation", Value: float64(5)},
				{Op: "test", Path: "/spec/template/metadata", Value: template["metadata"]},
				{Op: "add", Path: "/spec/template/metadata/annotations/kubectl.kubernetes.io~1restartedAt",
					Value: restartTime}}, operations)
			(*annotations)[rolloutRestartAnnotation] = restartTime
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	a := restartAction("Deployment")
	out, err := a.Run(t.Context(), c)
	require.Error(t, err)
	require.Nil(t, out)
	require.Equal(t, []string{"GET", "PATCH"}, calls)
	out, err = a.Run(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "PATCH", "GET", "PATCH"}, calls)
	require.Equal(t, restartTime, out.RestartAt)
	require.Equal(t, int64(5), out.Generation)
	require.Equal(t, map[string]string{
		"unrelated": "keep", "concurrent": "keep", rolloutRestartAnnotation: restartTime,
	}, *annotations)
}
