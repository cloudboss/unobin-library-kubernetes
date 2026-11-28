package v1

import (
	"context"
	"encoding/json"
	"fmt"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	"io"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestDrainNodeEmptySnapshot(t *testing.T) {
	node := cordonNodeFixture()
	node.Spec.Unschedulable = true
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path == "/api/v1/pods" {
			respond(t, w, &corev1.PodList{Items: []corev1.Pod{}})
		} else {
			respond(t, w, node)
		}
	})
	out, err := (&DrainNodeAction{Name: "example", ExpectedUID: "uid"}).Run(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &DrainNodeActionOutput{Server: *c.Server, APIVersion: "v1", Kind: "Node",
		Name: "example", UID: "uid", Unschedulable: true, Drained: true,
		DrainedPods: []DrainPodIdentity{}, SkippedPods: []DrainSkippedPod{}}, out)
	require.Equal(t, []string{"GET /api/v1/nodes/example",
		"GET /api/v1/pods?fieldSelector=spec.nodeName%3Dexample&limit=500",
		"GET /api/v1/nodes/example"}, calls)
}

type drainStep struct {
	method string
	path   string
	object any
	code   int
	check  func(*http.Request)
}

func drainPod(namespace, name string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace,
		UID: types.UID(namespace + "-" + name), OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "owner", UID: "owner-uid",
			Controller: new(true),
		}}}, Spec: corev1.PodSpec{NodeName: "example"}}
}

func drainList(pods ...corev1.Pod) *corev1.PodList {
	return &corev1.PodList{Items: pods}
}

func runDrainSteps(t *testing.T, steps []drainStep, timeout string) (
	*DrainNodeActionOutput, error,
) {
	t.Helper()
	var calls []string
	index := 0
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if index >= len(steps) {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(500)
			return
		}
		step := steps[index]
		index++
		require.Equal(t, step.method+" "+step.path, calls[len(calls)-1])
		require.Contains(t, r.Header.Get("Accept"), "application/json")
		if step.check != nil {
			step.check(r)
		}
		if step.code != 0 {
			endpointsHTTPError(t, w, step.code, metav1.StatusReasonUnknown)
			return
		}
		respond(t, w, step.object)
	})
	a := &DrainNodeAction{Name: "example", ExpectedUID: "uid"}
	if timeout != "" {
		a.Timeout = &timeout
	}
	out, err := a.Run(t.Context(), c)
	want := make([]string, len(steps))
	for i, step := range steps {
		want[i] = step.method + " " + step.path
	}
	require.Equal(t, want, calls)
	if err != nil {
		require.Nil(t, out)
		require.ErrorContains(t, err, "drain-node")
		require.ErrorContains(t, err, "Node example expected UID uid")
	}
	return out, err
}

const drainListPath = "/api/v1/pods?fieldSelector=spec.nodeName%3Dexample&limit=500"

func drainStart(pods ...corev1.Pod) []drainStep {
	node := cordonNodeFixture()
	node.Spec.Unschedulable = true
	return []drainStep{{"GET", "/api/v1/nodes/example", node, 0, nil},
		{"GET", drainListPath, drainList(pods...), 0, nil}}
}

func drainFinish() drainStep {
	node := cordonNodeFixture()
	node.Spec.Unschedulable = true
	return drainStep{"GET", "/api/v1/nodes/example", node, 0, nil}
}

func podPath(p corev1.Pod) string {
	return "/api/v1/namespaces/" + p.Namespace + "/pods/" + p.Name
}

func evictionStep(t *testing.T, p corev1.Pod) drainStep {
	t.Helper()
	return drainStep{"POST", podPath(p) + "/eviction", &metav1.Status{Status: "Success"}, 0,
		func(r *http.Request) {
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			require.Empty(t, r.URL.RawQuery)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(body, &got))
			require.Equal(t, map[string]any{"apiVersion": "policy/v1", "kind": "Eviction",
				"metadata": map[string]any{"name": p.Name, "namespace": p.Namespace,
					"creationTimestamp": nil},
				"deleteOptions": map[string]any{"preconditions": map[string]any{"uid": string(p.UID)}}},
				got)
		}}
}

func TestDrainNodeSerialSnapshot(t *testing.T) {
	first, second := drainPod("a", "first"), drainPod("b", "second")
	mirror, daemon := drainPod("b", "mirror"), drainPod("a", "daemon")
	mirror.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: ""}
	daemon.OwnerReferences[0].Kind = "DaemonSet"
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name: "owner", Namespace: "a", UID: "owner-uid"}}
	node := cordonNodeFixture()
	steps := []drainStep{{"GET", "/api/v1/nodes/example", node, 0, nil},
		{"PATCH", "/api/v1/nodes/example", nil, 0, nil}}
	steps[1].check = func(r *http.Request) {
		require.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
		require.Empty(t, r.URL.RawQuery)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `[{"op":"test","path":"/metadata/uid","value":"uid"},
   {"op":"test","path":"/metadata/resourceVersion","value":"opaque"},
   {"op":"add","path":"/spec/unschedulable","value":true}]`, string(body))
		patch, err := jsonpatch.DecodePatch(body)
		require.NoError(t, err)
		before, err := json.Marshal(node)
		require.NoError(t, err)
		after, err := patch.Apply(before)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(after, node))
	}
	steps[1].object = node
	page1 := drainList(second, mirror)
	page1.Continue = "next"
	empty := drainList()
	empty.Continue = "last"
	steps = append(steps,
		drainStep{"GET", drainListPath, page1, 0, nil},
		drainStep{"GET", "/api/v1/pods?continue=next&fieldSelector=spec.nodeName%3Dexample&limit=500",
			empty, 0, nil},
		drainStep{"GET", "/api/v1/pods?continue=last&fieldSelector=spec.nodeName%3Dexample&limit=500",
			drainList(first, daemon), 0, nil},
		drainStep{"GET", "/apis/apps/v1/namespaces/a/daemonsets/owner", ds, 0, nil})
	for _, pod := range []corev1.Pod{first, second} {
		replacement := pod.DeepCopy()
		replacement.UID = "replacement"
		steps = append(steps, drainStep{"GET", podPath(pod), &pod, 0, nil}, evictionStep(t, pod),
			drainStep{"GET", podPath(pod), replacement, 0, nil})
	}
	steps = append(steps, drainFinish())
	out, err := runDrainSteps(t, steps, "")
	require.NoError(t, err)
	require.Equal(t, []DrainPodIdentity{{"a", "first", "a-first"},
		{"b", "second", "b-second"}}, out.DrainedPods)
	require.Equal(t, []DrainSkippedPod{{"a", "daemon", "a-daemon", "daemon-set"},
		{"b", "mirror", "b-mirror", "mirror"}}, out.SkippedPods)
	before := cordonNodeFixture()
	before.Spec.Unschedulable = true
	require.Equal(t, before, node)
}

func TestDrainNodeSnapshotFailures(t *testing.T) {
	for _, problem := range []string{"name", "namespace", "uid", "binding", "duplicate",
		"repeat token", "later failure", "expired"} {
		t.Run(problem, func(t *testing.T) {
			pod := drainPod("a", "pod")
			start := drainStart(pod)
			switch problem {
			case "name":
				pod.Name = ""
			case "namespace":
				pod.Namespace = ""
			case "uid":
				pod.UID = ""
			case "binding":
				pod.Spec.NodeName = "other"
			}
			start[1].object = drainList(pod)
			if problem == "duplicate" || problem == "repeat token" ||
				problem == "later failure" || problem == "expired" {
				start[1].object.(*corev1.PodList).Continue = "next"
				next := drainList()
				code := 0
				switch problem {
				case "duplicate":
					next.Items = []corev1.Pod{pod}
				case "repeat token":
					next.Continue = "next"
				case "later failure":
					code = 403
				case "expired":
					code = 410
				}
				start = append(start, drainStep{"GET",
					"/api/v1/pods?continue=next&fieldSelector=spec.nodeName%3Dexample&limit=500", next, code, nil})
			}
			_, err := runDrainSteps(t, start, "")
			require.ErrorContains(t, err, "cordon was not reverted")
		})
	}
}

func TestDrainNodeProtectedPreflight(t *testing.T) {
	for _, problem := range []string{"unmanaged", "noncontroller", "emptyDir", "missing DS",
		"forbidden DS", "stale DS", "deleting DS", "wrong DS name", "wrong DS namespace",
		"empty DS uid"} {
		t.Run(problem, func(t *testing.T) {
			good, bad := drainPod("a", "eligible"), drainPod("z", "protected")
			ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
				Name: "owner", Namespace: "z", UID: "owner-uid"}}
			switch problem {
			case "unmanaged":
				bad.OwnerReferences = nil
			case "noncontroller":
				bad.OwnerReferences[0].Controller = new(false)
			case "emptyDir":
				bad.Spec.Volumes = []corev1.Volume{{Name: "data",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
			default:
				bad.OwnerReferences[0].Kind = "DaemonSet"
			}
			start := drainStart(good)
			start[1].object.(*corev1.PodList).Continue = "next"
			start = append(start, drainStep{"GET",
				"/api/v1/pods?continue=next&fieldSelector=spec.nodeName%3Dexample&limit=500",
				drainList(bad), 0, nil})
			if bad.OwnerReferences != nil && bad.OwnerReferences[0].Kind == "DaemonSet" {
				code := 0
				switch problem {
				case "missing DS":
					code = 404
				case "forbidden DS":
					code = 403
				case "stale DS":
					ds.UID = "other"
				case "deleting DS":
					ds.DeletionTimestamp = new(metav1.Now())
				case "wrong DS name":
					ds.Name = "other"
				case "wrong DS namespace":
					ds.Namespace = "other"
				case "empty DS uid":
					ds.UID = ""
				}
				start = append(start, drainStep{"GET",
					"/apis/apps/v1/namespaces/z/daemonsets/owner", ds, code, nil})
			}
			_, err := runDrainSteps(t, start, "")
			require.ErrorContains(t, err, "Pod z/protected UID z-protected")
			require.ErrorContains(t, err, "preflight")
		})
	}
}

func TestDrainNodeTerminalAndTerminating(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed, ""} {
		t.Run(string(phase), func(t *testing.T) {
			pod := drainPod("a", "pod")
			pod.OwnerReferences = nil
			pod.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
			pod.Status.Phase = phase
			if phase == "" {
				pod.DeletionTimestamp = new(metav1.Now())
			}
			steps := append(drainStart(pod), drainStep{"GET", podPath(pod), &pod, 0, nil})
			if phase != "" {
				steps = append(steps, evictionStep(t, pod))
			}
			steps = append(steps, drainStep{"GET", podPath(pod), nil, 404, nil}, drainFinish())
			out, err := runDrainSteps(t, steps, "")
			require.NoError(t, err)
			require.Equal(t, []DrainPodIdentity{{"a", "pod", "a-pod"}}, out.DrainedPods)
			require.Equal(t, []DrainSkippedPod{}, out.SkippedPods)
		})
	}
}

func TestDrainNodeOriginalObservations(t *testing.T) {
	for _, position := range []string{"first", "wait", "post404"} {
		for _, observation := range []string{"absent", "replacement", "name", "namespace", "uid",
			"binding", "same"} {
			if position == "wait" && observation == "same" {
				continue
			}
			t.Run(position+"/"+observation, func(t *testing.T) {
				pod := drainPod("a", "pod")
				steps := drainStart(pod)
				if position != "first" {
					steps = append(steps, drainStep{"GET", podPath(pod), &pod, 0, nil})
					eviction := evictionStep(t, pod)
					if position == "post404" {
						eviction.code = 404
					}
					steps = append(steps, eviction)
				}
				got := pod.DeepCopy()
				code := 0
				switch observation {
				case "absent":
					code = 404
				case "replacement":
					got.UID = "new"
					got.Spec.NodeName = "other"
				case "name":
					got.Name = "other"
				case "namespace":
					got.Namespace = "other"
				case "uid":
					got.UID = ""
				case "binding":
					got.Spec.NodeName = "other"
				}
				steps = append(steps, drainStep{"GET", podPath(pod), got, code, nil})
				success := observation == "absent" || observation == "replacement"
				if success {
					steps = append(steps, drainFinish())
				}
				if observation == "same" && position == "first" {
					steps = append(steps, evictionStep(t, pod),
						drainStep{"GET", podPath(pod), nil, 404, nil}, drainFinish())
					success = true
				}
				_, err := runDrainSteps(t, steps, "")
				if success {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}

func TestDrainNodeSafetyRefresh(t *testing.T) {
	for _, problem := range []string{"mirror", "unmanaged", "emptyDir", "DS"} {
		t.Run(problem, func(t *testing.T) {
			pod := drainPod("a", "pod")
			live := pod.DeepCopy()
			switch problem {
			case "mirror":
				live.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: ""}
			case "unmanaged":
				live.OwnerReferences = nil
			case "emptyDir":
				live.Spec.Volumes = []corev1.Volume{{VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
			case "DS":
				live.OwnerReferences[0].Kind = "DaemonSet"
			}
			steps := append(drainStart(pod), drainStep{"GET", podPath(pod), live, 0, nil})
			if problem == "DS" {
				steps = append(steps, drainStep{"GET",
					"/apis/apps/v1/namespaces/a/daemonsets/owner", &appsv1.DaemonSet{
						ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "a", UID: "owner-uid"}}, 0, nil})
			}
			_, err := runDrainSteps(t, steps, "")
			require.ErrorContains(t, err, "recheck Pod safety")
		})
	}
}

func TestDrainNodePartialAndTerminalErrors(t *testing.T) {
	for _, code := range []int{403, 409, 422, 500, 405} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			a, b := drainPod("a", "first"), drainPod("b", "second")
			steps := append(drainStart(b, a), drainStep{"GET", podPath(a), &a, 0, nil}, evictionStep(t, a),
				drainStep{"GET", podPath(a), nil, 404, nil}, drainStep{"GET", podPath(b), &b, 0, nil})
			refused := evictionStep(t, b)
			refused.code = code
			steps = append(steps, refused)
			_, err := runDrainSteps(t, steps, "")
			require.ErrorContains(t, err, "earlier evictions may have completed")
			var status *apierrors.StatusError
			require.ErrorAs(t, err, &status)
			require.Equal(t, int32(code), status.ErrStatus.Code)
		})
	}
}

func TestDrainNodeFinalObservation(t *testing.T) {
	for _, problem := range []string{"missing", "uid", "name", "namespace", "empty uid", "version",
		"deleting", "uncordoned"} {
		t.Run(problem, func(t *testing.T) {
			final := drainFinish()
			node := final.object.(*corev1.Node)
			switch problem {
			case "missing":
				final.code = 404
			case "uid":
				node.UID = "other"
			case "name":
				node.Name = "other"
			case "namespace":
				node.Namespace = "other"
			case "empty uid":
				node.UID = ""
			case "version":
				node.ResourceVersion = ""
			case "deleting":
				node.DeletionTimestamp = new(metav1.Now())
			case "uncordoned":
				node.Spec.Unschedulable = false
			}
			_, err := runDrainSteps(t, append(drainStart(), final), "")
			require.ErrorContains(t, err, "final Node observation")
		})
	}
}

func TestDrainNodeRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		header int64
		body   int
		want   time.Duration
	}{
		{0, 0, 5 * time.Second}, {-1, -2, 5 * time.Second}, {2, 0, 2 * time.Second},
		{0, 3, 3 * time.Second}, {4, 2, 4 * time.Second}, {2, 4, 4 * time.Second},
		{int64(1<<63 - 1), 1, time.Duration(1<<63 - 1)},
	} {
		require.Equal(t, tc.want, drainRetryDelay(apierrors.NewTooManyRequests("refused", tc.body),
			tc.header))
	}
}

func TestDrainNodeInitialGuards(t *testing.T) {
	for _, position := range []string{"get", "patch", "already cordoned"} {
		for _, problem := range []string{"name", "scope", "uid", "empty uid", "version", "deleting",
			"schedulable", "404", "409"} {
			if problem == "schedulable" && position != "patch" {
				continue
			}
			t.Run(position+"/"+problem, func(t *testing.T) {
				live := cordonNodeFixture()
				if position == "already cordoned" {
					live.Spec.Unschedulable = true
				}
				bad := live.DeepCopy()
				switch problem {
				case "name":
					bad.Name = "other"
				case "scope":
					bad.Namespace = "other"
				case "uid":
					bad.UID = "other"
				case "empty uid":
					bad.UID = ""
				case "version":
					bad.ResourceVersion = ""
				case "deleting":
					bad.DeletionTimestamp = new(metav1.Now())
				}
				code := 0
				if problem == "404" {
					code = 404
				}
				if problem == "409" {
					code = 409
				}
				steps := []drainStep{}
				if position == "patch" {
					steps = append(steps, drainStep{"GET", "/api/v1/nodes/example", live, 0, nil},
						drainStep{"PATCH", "/api/v1/nodes/example", bad, code, nil})
				} else {
					steps = append(steps, drainStep{"GET", "/api/v1/nodes/example", bad, code, nil})
				}
				_, err := runDrainSteps(t, steps, "")
				require.Error(t, err)
			})
		}
	}
}

func TestDrainNodeCancellation(t *testing.T) {
	for _, phase := range []string{"before", "node get", "patch", "list", "owner get", "pod get",
		"post", "poll", "delay"} {
		for _, mode := range []string{"cancel", "caller deadline", "action deadline"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				node := cordonNodeFixture()
				pod := drainPod("a", "pod")
				if phase == "owner get" {
					pod.OwnerReferences[0].Kind = "DaemonSet"
				}
				var calls []string
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				expected := context.Canceled
				a := &DrainNodeAction{Name: "example", ExpectedUID: "uid", Timeout: new("5s")}
				if mode == "caller deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 40*time.Millisecond)
					defer stop()
					expected = context.DeadlineExceeded
				}
				if mode == "action deadline" {
					a.Timeout = new("40ms")
					expected = context.DeadlineExceeded
				}
				getPodCount := 0
				c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method+" "+r.URL.RequestURI())
					current := ""
					switch {
					case r.URL.Path == "/api/v1/nodes/example" && r.Method == "GET":
						current = "node get"
					case r.Method == "PATCH":
						current = "patch"
					case r.URL.Path == "/api/v1/pods":
						current = "list"
					case strings.Contains(r.URL.Path, "daemonsets"):
						current = "owner get"
					case r.Method == "POST":
						current = "post"
					default:
						getPodCount++
						current = "pod get"
						if getPodCount > 1 {
							current = "poll"
						}
					}
					if current == phase {
						_, readErr := io.Copy(io.Discard, r.Body)
						require.NoError(t, readErr)
						if mode == "cancel" {
							cancel()
						}
						<-r.Context().Done()
						return
					}
					if current == "post" && phase == "delay" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(429)
						respond(t, w, &metav1.Status{
							TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
							Status:   "Failure", Reason: metav1.StatusReasonTooManyRequests,
							Code: 429, Message: "PDB refused"})
						if mode == "cancel" {
							time.AfterFunc(10*time.Millisecond, cancel)
						}
						return
					}
					switch current {
					case "node get":
						respond(t, w, node)
					case "patch":
						node.Spec.Unschedulable = true
						respond(t, w, node)
					case "list":
						respond(t, w, drainList(pod))
					case "post":
						respond(t, w, &metav1.Status{Status: "Success"})
					default:
						respond(t, w, &pod)
					}
				})
				if phase == "before" {
					cancel()
					expected = context.Canceled
				}
				out, err := a.Run(ctx, c)
				require.Nil(t, out)
				require.ErrorIs(t, err, expected)
				want := []string(nil)
				if phase != "before" {
					want = append(want, "GET /api/v1/nodes/example")
					if phase != "node get" {
						want = append(want, "PATCH /api/v1/nodes/example")
						if phase != "patch" {
							want = append(want, "GET "+drainListPath)
							if phase == "owner get" {
								want = append(want, "GET /apis/apps/v1/namespaces/a/daemonsets/owner")
							} else if phase != "list" {
								want = append(want, "GET "+podPath(pod))
								if phase != "pod get" {
									want = append(want, "POST "+podPath(pod)+"/eviction")
									if phase == "poll" {
										want = append(want, "GET "+podPath(pod))
									}
								}
							}
						}
					}
				}
				require.Equal(t, want, calls)
			})
		}
	}
}

func TestDrainNodeRetryOriginal(t *testing.T) {
	for _, result := range []string{"replacement", "absent", "terminating", "unsafe", "retry"} {
		t.Run(result, func(t *testing.T) {
			pod := drainPod("a", "pod")
			gets, posts := 0, 0
			node := cordonNodeFixture()
			node.Spec.Unschedulable = true
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				switch {
				case r.URL.Path == "/api/v1/nodes/example":
					respond(t, w, node)
				case r.URL.Path == "/api/v1/pods":
					respond(t, w, drainList(pod))
				case r.Method == "POST":
					posts++
					require.Equal(t, posts, gets)
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Contains(t, string(body), `"uid":"a-pod"`)
					if posts == 1 {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(429)
						respond(t, w, &metav1.Status{
							TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
							Status:   "Failure", Reason: metav1.StatusReasonTooManyRequests,
							Code: 429, Details: &metav1.StatusDetails{RetryAfterSeconds: 1}})
					} else {
						respond(t, w, &metav1.Status{Status: "Success"})
					}
				default:
					gets++
					current := pod.DeepCopy()
					if gets > 1 {
						switch result {
						case "replacement":
							current.UID = "other"
						case "absent":
							endpointsHTTPError(t, w, 404, metav1.StatusReasonNotFound)
							return
						case "terminating":
							if gets > 2 {
								endpointsHTTPError(t, w, 404, metav1.StatusReasonNotFound)
								return
							}
							current.DeletionTimestamp = new(metav1.Now())
						case "unsafe":
							current.OwnerReferences = nil
						case "retry":
							if gets > 2 {
								endpointsHTTPError(t, w, 404, metav1.StatusReasonNotFound)
								return
							}
						}
					}
					respond(t, w, current)
				}
			})
			out, err := (&DrainNodeAction{Name: "example", ExpectedUID: "uid", Timeout: new("4s")}).Run(
				t.Context(), c)
			want := []string{"GET /api/v1/nodes/example", "GET " + drainListPath,
				"GET " + podPath(pod), "POST " + podPath(pod) + "/eviction", "GET " + podPath(pod)}
			if result == "unsafe" {
				require.ErrorContains(t, err, "no controlling owner")
				require.Nil(t, out)
			} else {
				require.NoError(t, err)
				if result == "retry" {
					want = append(want, "POST "+podPath(pod)+"/eviction")
				}
				if result == "retry" || result == "terminating" {
					want = append(want, "GET "+podPath(pod))
				}
				want = append(want, "GET /api/v1/nodes/example")
				require.Equal(t, []DrainPodIdentity{{"a", "pod", "a-pod"}}, out.DrainedPods)
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestDrainNodeFinalRetryHeader(t *testing.T) {
	for _, hint := range []int32{0, 2} {
		t.Run(fmt.Sprint(hint), func(t *testing.T) {
			pod := drainPod("a", "pod")
			node := cordonNodeFixture()
			node.Spec.Unschedulable = true
			posts, gets := 0, 0
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				switch {
				case r.URL.Path == "/api/v1/nodes/example":
					respond(t, w, node)
				case r.URL.Path == "/api/v1/pods":
					respond(t, w, drainList(pod))
				case r.Method == "POST":
					posts++
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Contains(t, string(body), `"uid":"a-pod"`)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "0")
					if posts == 11 {
						w.Header().Set("Retry-After", "1")
					}
					w.WriteHeader(429)
					respond(t, w, metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
						Status: "Failure", Code: 429, Reason: metav1.StatusReasonTooManyRequests,
						Details: &metav1.StatusDetails{RetryAfterSeconds: hint}})
				default:
					gets++
					if gets == 1 {
						respond(t, w, &pod)
					} else {
						endpointsHTTPError(t, w, 404, metav1.StatusReasonNotFound)
					}
				}
			})
			start := time.Now()
			out, err := (&DrainNodeAction{Name: "example", ExpectedUID: "uid", Timeout: new("4s")}).Run(
				t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []DrainPodIdentity{{"a", "pod", "a-pod"}}, out.DrainedPods)
			require.GreaterOrEqual(t, time.Since(start), time.Duration(max(hint, 1))*time.Second)
			want := []string{"GET /api/v1/nodes/example", "GET " + drainListPath, "GET " + podPath(pod)}
			for range 11 {
				want = append(want, "POST "+podPath(pod)+"/eviction")
			}
			want = append(want, "GET "+podPath(pod), "GET /api/v1/nodes/example")
			require.Equal(t, want, calls)
		})
	}
}

func TestDrainNodeRetryHeaderParsing(t *testing.T) {
	for _, tc := range []struct {
		header  string
		seconds int64
	}{
		{"", 0}, {"bad", 0}, {"0", 0}, {"-1", -1}, {"2", 2},
		{"9223372036854775807", 1<<63 - 1}, {"999999999999999999999999", 1<<63 - 1},
	} {
		t.Run(tc.header, func(t *testing.T) {
			d := &drainInvocation{}
			transport := drainTransport{invocation: d, base: drainTestTransport(func(r *http.Request) (
				*http.Response, error,
			) {
				return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {tc.header}}}, nil
			})}
			response, err := transport.RoundTrip(&http.Request{Method: "POST"})
			require.NoError(t, err)
			require.Equal(t, 429, response.StatusCode)
			require.Equal(t, tc.seconds, d.retryHeader.Load())
		})
	}
}

type drainTestTransport func(*http.Request) (*http.Response, error)

func (f drainTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDrainNodeAmbiguousEviction(t *testing.T) {
	pod := drainPod("a", "pod")
	node := cordonNodeFixture()
	node.Spec.Unschedulable = true
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/api/v1/nodes/example":
			respond(t, w, node)
		case r.URL.Path == "/api/v1/pods":
			respond(t, w, drainList(pod))
		case r.Method == "POST":
			_, err := io.Copy(io.Discard, r.Body)
			require.NoError(t, err)
			conn, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, conn.Close())
		default:
			respond(t, w, &pod)
		}
	})
	out, err := (&DrainNodeAction{Name: "example", ExpectedUID: "uid"}).Run(t.Context(), c)
	require.ErrorContains(t, err, "EOF")
	require.Nil(t, out)
	require.Equal(t, []string{"GET /api/v1/nodes/example", "GET " + drainListPath,
		"GET " + podPath(pod), "POST " + podPath(pod) + "/eviction"}, calls)
}

func TestDrainNodePatchRace(t *testing.T) {
	for _, field := range []string{"uid", "resourceVersion"} {
		t.Run(field, func(t *testing.T) {
			node := cordonNodeFixture()
			steps := []drainStep{{"GET", "/api/v1/nodes/example", node, 0, nil},
				{"PATCH", "/api/v1/nodes/example", nil, 422, func(r *http.Request) {
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					changed := node.DeepCopy()
					if field == "uid" {
						changed.UID = "replacement"
					} else {
						changed.ResourceVersion = "changed"
					}
					before, err := json.Marshal(changed)
					require.NoError(t, err)
					_, err = patch.Apply(before)
					require.Error(t, err)
				}}}
			_, err := runDrainSteps(t, steps, "")
			var status *apierrors.StatusError
			require.ErrorAs(t, err, &status)
			require.Equal(t, int32(422), status.ErrStatus.Code)
		})
	}
}

func TestDrainNodeTerminatingFinalizer(t *testing.T) {
	pod := drainPod("a", "pod")
	pod.DeletionTimestamp = new(metav1.Now())
	pod.Finalizers = []string{"test.example/retain"}
	pod.OwnerReferences = nil
	steps := append(drainStart(pod), drainStep{"GET", podPath(pod), &pod, 0, nil},
		drainStep{"GET", podPath(pod), &pod, 0, nil})
	_, err := runDrainSteps(t, steps, "40ms")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "wait for original Pod deletion")
}

func TestDrainNodeSkippedOnlyAndTerminalDaemon(t *testing.T) {
	mirror := drainPod("a", "mirror")
	mirror.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: ""}
	mirror.Status.Phase = corev1.PodSucceeded
	steps := append(drainStart(mirror), drainFinish())
	out, err := runDrainSteps(t, steps, "")
	require.NoError(t, err)
	require.Equal(t, []DrainPodIdentity{}, out.DrainedPods)
	require.Equal(t, []DrainSkippedPod{{"a", "mirror", "a-mirror", "mirror"}}, out.SkippedPods)
	daemon := drainPod("a", "daemon")
	daemon.OwnerReferences[0].Kind = "DaemonSet"
	daemon.Status.Phase = corev1.PodSucceeded
	steps = append(drainStart(daemon), drainStep{"GET", podPath(daemon), &daemon, 0, nil},
		evictionStep(t, daemon), drainStep{"GET", podPath(daemon), nil, 404, nil}, drainFinish())
	out, err = runDrainSteps(t, steps, "")
	require.NoError(t, err)
	require.Equal(t, []DrainPodIdentity{{"a", "daemon", "a-daemon"}}, out.DrainedPods)
	require.Equal(t, []DrainSkippedPod{}, out.SkippedPods)
}

func TestDrainNodeReadErrors(t *testing.T) {
	for _, position := range []string{"first", "poll", "confirm404"} {
		t.Run(position, func(t *testing.T) {
			pod := drainPod("a", "pod")
			steps := drainStart(pod)
			if position != "first" {
				steps = append(steps, drainStep{"GET", podPath(pod), &pod, 0, nil})
				post := evictionStep(t, pod)
				if position == "confirm404" {
					post.code = 404
				}
				steps = append(steps, post)
			}
			steps = append(steps, drainStep{"GET", podPath(pod), nil, 403, nil})
			_, err := runDrainSteps(t, steps, "")
			require.True(t, apierrors.IsForbidden(err))
		})
	}
}
