package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	node "github.com/cloudboss/unobin-library-kubernetes/internal/api/node/v1"
)

type prior = runtime.Prior[
	node.RuntimeClassResource, *node.RuntimeClassResourceOutput, *config.Configuration,
]

const runtimeClassPath = "/apis/node.k8s.io/v1/runtimeclasses"
const examplePath = runtimeClassPath + "/example"

type request struct {
	method string
	path   string
	token  string
	body   string
}

func runtimeClassAPI(
	t *testing.T, handler http.HandlerFunc,
) (*config.Configuration, func() []request) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var requests []request
	var lock sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests = append(requests, request{
			r.Method, r.URL.Path, r.Header.Get("Authorization"), string(body),
		})
		r.Body = io.NopCloser(bytes.NewReader(body))
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	c := &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: new(true)}
	return c, func() []request {
		lock.Lock()
		defer lock.Unlock()
		return append([]request(nil), requests...)
	}
}

func respond(t *testing.T, w http.ResponseWriter, obj any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(obj); err != nil {
		t.Error(err)
	}
}

func fail(t *testing.T, w http.ResponseWriter, code int, reason metav1.StatusReason) {
	t.Helper()
	w.WriteHeader(code)
	respond(t, w, metav1.Status{Status: "Failure", Code: int32(code), Reason: reason})
}

func liveClass() *nodev1.RuntimeClass {
	return &nodev1.RuntimeClass{
		TypeMeta: metav1.TypeMeta{APIVersion: "node.k8s.io/v1", Kind: "RuntimeClass"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", UID: "uid-1", ResourceVersion: "100", Generation: 1,
		},
		Handler: "run-1",
		Overhead: &nodev1.Overhead{
			PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
		},
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"runtime": "sandbox"}},
	}
}

func TestRuntimeClassCreate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		metadata  node.RuntimeClassMetadata
		request   string
		actual    string
		generated string
		labels    map[string]string
		ann       map[string]string
	}{
		{
			name: "explicit",
			metadata: node.RuntimeClassMetadata{
				Name:        new("example"),
				Labels:      new(map[string]string{"Mixed_Case": "MiXeD"}),
				Annotations: new(map[string]string{"EXAMPLE.org/Case": " spaces\n日本語 "}),
			},
			request: `"name":"example","labels":{"Mixed_Case":"MiXeD"},
				"annotations":{"EXAMPLE.org/Case":" spaces\n日本語 "}`,
			actual: "example",
			labels: map[string]string{"Mixed_Case": "MiXeD"},
			ann:    map[string]string{"EXAMPLE.org/Case": " spaces\n日本語 "},
		},
		{
			name: "generate with empty maps",
			metadata: node.RuntimeClassMetadata{
				GenerateName: new("test-"),
				Labels:       new(map[string]string{}),
				Annotations:  new(map[string]string{}),
			},
			request: `"generateName":"test-"`,
			actual:  "test-server-suffix", generated: "test-",
			labels: map[string]string{}, ann: map[string]string{},
		},
		{
			name: "omitted maps", metadata: node.RuntimeClassMetadata{Name: new("example")},
			request: `"name":"example"`, actual: "example",
			labels: map[string]string{}, ann: map[string]string{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			obj := liveClass()
			obj.Name, obj.GenerateName = tt.actual, tt.generated
			obj.Labels, obj.Annotations = tt.labels, tt.ann
			c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, r *http.Request) {
				respond(t, w, obj)
			})
			r := node.RuntimeClassResource{Handler: "run-1", Metadata: tt.metadata}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			want := &node.RuntimeClassResourceOutput{
				Server: *c.Server, APIVersion: "node.k8s.io/v1", Kind: "RuntimeClass",
				Handler: "run-1", Metadata: node.RuntimeClassMetadataOutput{
					Name: tt.actual, GenerateName: tt.generated, UID: "uid-1",
					Labels: tt.labels, Annotations: tt.ann,
				},
			}
			require.Equal(t, want, out)
			p := prior{Inputs: r, Outputs: out}
			current, wrongCalls := runtimeClassAPI(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request to current server: %s %s", r.Method, r.URL.Path)
			})
			current.Token = new("new-token")
			r.Metadata = node.RuntimeClassMetadata{Name: new("different")}
			out, err = r.Read(t.Context(), current, p)
			require.NoError(t, err)
			require.Equal(t, want, out)
			require.NoError(t, r.Delete(t.Context(), current, p))
			got := calls()
			path := runtimeClassPath + "/" + tt.actual
			require.Equal(t, []request{
				{method: "POST", path: runtimeClassPath, body: got[0].body},
				{method: "GET", path: path},
				{method: "GET", path: path, token: "Bearer new-token"},
				{method: "DELETE", path: path, token: "Bearer new-token", body: got[3].body},
			}, got)
			require.Empty(t, wrongCalls())
			require.JSONEq(t, `{
				"kind":"RuntimeClass","apiVersion":"node.k8s.io/v1",
				"metadata":{"creationTimestamp":null,`+tt.request+`},"handler":"run-1"
			}`, got[0].body)
			require.JSONEq(t, `{
				"kind":"DeleteOptions","apiVersion":"node.k8s.io/v1","preconditions":{"uid":"uid-1"}
			}`, got[3].body)
		})
	}
}

func savedClass(c *config.Configuration) prior {
	return prior{
		Inputs: node.RuntimeClassResource{
			Handler: "run-1", Metadata: node.RuntimeClassMetadata{Name: new("example")},
		},
		Outputs: &node.RuntimeClassResourceOutput{
			Server: *c.Server, APIVersion: "node.k8s.io/v1", Kind: "RuntimeClass",
			Handler: "run-1", Metadata: node.RuntimeClassMetadataOutput{
				Name: "example", UID: "uid-1", Labels: map[string]string{},
				Annotations: map[string]string{},
			},
		},
	}
}

func TestRuntimeClassReadProjection(t *testing.T) {
	obj := liveClass()
	meta := map[string]string{
		"kubernetes.io":                                              "hidden",
		"controller.kubernetes.io/key":                               "hidden",
		"notkubernetes.io/key":                                       "suffix",
		"kubernetes.io.other/key":                                    "visible",
		"other/deprecated.daemonset.template.generation":             "hidden",
		"app.kubernetes.io/deprecated.daemonset.template.generation": "exception",
		"service.beta.kubernetes.io/external":                        "exception",
		"service.beta.kubernetes.io/custom":                          "custom-hidden",
		"custom.org/ordinary":                                        "custom-hidden",
		"custom.org/managed":                                         "custom-visible",
		"explicit.kubernetes.io/managed":                             "internal-visible",
		"basic":                                                      "visible",
	}
	obj.Labels, obj.Annotations = meta, meta
	c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, r *http.Request) {
		respond(t, w, obj)
	})
	p := savedClass(c)
	p.Inputs.IgnoreLabels = new([]string{"custom", "^unused"})
	p.Inputs.IgnoreAnnotations = p.Inputs.IgnoreLabels
	managed := map[string]string{
		"custom.org/managed": "desired", "explicit.kubernetes.io/managed": "desired",
	}
	p.Inputs.Metadata.Labels, p.Inputs.Metadata.Annotations = &managed, &managed
	p.Outputs.Metadata.Labels = map[string]string{"custom.org/ordinary": "previously visible"}
	wantMap := map[string]string{
		"kubernetes.io.other/key":                                    "visible",
		"app.kubernetes.io/deprecated.daemonset.template.generation": "exception",
		"service.beta.kubernetes.io/external":                        "exception",
		"custom.org/managed":                                         "custom-visible",
		"explicit.kubernetes.io/managed":                             "internal-visible",
		"basic":                                                      "visible",
	}
	want := *p.Outputs
	want.Metadata.Labels, want.Metadata.Annotations = wantMap, wantMap
	r := p.Inputs
	r.IgnoreAnnotations, r.IgnoreLabels = nil, nil
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, &want, out)
	require.Equal(t, []request{{method: "GET", path: examplePath}}, calls())
}

func patchClass(
	r *http.Request, obj *nodev1.RuntimeClass,
) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	patch, err := jsonpatch.DecodePatch(body)
	if err != nil {
		return err
	}
	live, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	updated, err := patch.Apply(live)
	if err != nil {
		return err
	}
	// Removed maps must be read as absent when decoding the patched object.
	*obj = nodev1.RuntimeClass{}
	return json.Unmarshal(updated, obj)
}

func TestRuntimeClassUpdatePatch(t *testing.T) {
	for _, tt := range []struct {
		name       string
		live       map[string]string
		want       map[string]string
		expected   map[string]string
		operations string
	}{
		{
			name:       "absent map",
			want:       map[string]string{"new": ""},
			expected:   map[string]string{"new": ""},
			operations: `,{"op":"add","path":"/metadata/labels","value":{"new":""}}`,
		},
		{
			name:       "filtered-only live map",
			live:       map[string]string{"test.kubernetes.io/controller": "filtered"},
			want:       map[string]string{"new": ""},
			expected:   map[string]string{"new": ""},
			operations: `,{"op":"add","path":"/metadata/labels","value":{"new":""}}`,
		},
		{
			name:     "patch entries",
			live:     map[string]string{"a/b": "was", "c/~d": "remove", "same": "same"},
			want:     map[string]string{"a/b": "", "new": "added", "same": "same"},
			expected: map[string]string{"a/b": "", "new": "added", "same": "same"},
			operations: `,{"op":"replace","path":"/metadata/labels/a~1b","value":""},` +
				`{"op":"remove","path":"/metadata/labels/c~1~0d"},` +
				`{"op":"add","path":"/metadata/labels/new","value":"added"}`,
		},
		{
			name:       "clear managed map",
			live:       map[string]string{"remove": "managed", "test.kubernetes.io/controller": "keep"},
			expected:   map[string]string{"test.kubernetes.io/controller": "keep"},
			operations: `,{"op":"remove","path":"/metadata/labels/remove"}`,
		},
		{
			name:     "unchanged projection",
			live:     map[string]string{"test.kubernetes.io/controller": "keep"},
			expected: map[string]string{"test.kubernetes.io/controller": "keep"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			obj := liveClass()
			obj.Labels = tt.live
			obj.Scheduling.Tolerations = []corev1.Toleration{
				{Key: "runtime", Operator: corev1.TolerationOpExists},
			}
			before := obj.DeepCopy()
			c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					if !assert.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type")) {
						w.WriteHeader(400)
						return
					}
					if err := patchClass(r, obj); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
				}
				respond(t, w, obj)
			})
			p := savedClass(c)
			r := p.Inputs
			r.Metadata.Labels = &tt.want
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			want := *p.Outputs
			want.Metadata.Labels = map[string]string{}
			maps.Copy(want.Metadata.Labels, tt.want)
			require.Equal(t, &want, out)
			require.Equal(t, tt.expected, obj.Labels)
			require.Equal(t, before.Overhead, obj.Overhead)
			require.Equal(t, before.Scheduling, obj.Scheduling)
			require.Equal(t, before.UID, obj.UID)
			get := request{method: "GET", path: examplePath}
			wantCalls := []request{get}
			if tt.operations != "" {
				wantCalls = append(wantCalls, request{
					method: "PATCH", path: examplePath,
					body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"}` +
						tt.operations + `]`,
				}, get)
			}
			require.Equal(t, wantCalls, calls())
		})
	}
}

func TestRuntimeClassFailures(t *testing.T) {
	for _, tt := range []struct {
		name      string
		code      int
		reason    metav1.StatusReason
		operation string
		methods   []string
		want      error
	}{
		{"not found", 404, metav1.StatusReasonNotFound, "read", []string{"GET"}, runtime.ErrNotFound},
		{"read forbidden", 403, metav1.StatusReasonForbidden, "read", []string{"GET"}, nil},
		{"create collision", 409, metav1.StatusReasonAlreadyExists, "create", []string{"POST"}, nil},
		{"read after create", 404, metav1.StatusReasonNotFound, "create", []string{"POST", "GET"}, nil},
		{"read after patch", 500, metav1.StatusReasonInternalError, "update",
			[]string{"GET", "PATCH", "GET"}, nil},
		{"absent on delete", 404, metav1.StatusReasonNotFound, "delete", []string{"DELETE"}, nil},
		{"forbidden delete", 403, metav1.StatusReasonForbidden, "delete", []string{"DELETE"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			obj := liveClass()
			count := 0
			c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				if count == len(tt.methods) {
					fail(t, w, tt.code, tt.reason)
					return
				}
				respond(t, w, obj)
			})
			p := savedClass(c)
			r := p.Inputs
			var err error
			switch tt.operation {
			case "create":
				_, err = r.Create(t.Context(), c)
			case "read":
				_, err = r.Read(t.Context(), c, p)
			case "update":
				r.Metadata.Labels = new(map[string]string{"change": "new"})
				_, err = r.Update(t.Context(), c, p)
			case "delete":
				err = r.Delete(t.Context(), c, p)
			}
			switch {
			case tt.want != nil:
				require.ErrorIs(t, err, tt.want)
			case tt.name == "absent on delete":
				require.NoError(t, err)
			default:
				require.ErrorContains(t, err, "RuntimeClass")
				require.Equal(t, tt.reason, apierrors.ReasonForError(err))
				require.NotErrorIs(t, err, runtime.ErrNotFound)
			}
			var methods []string
			for _, call := range calls() {
				methods = append(methods, call.method)
			}
			require.Equal(t, tt.methods, methods)
		})
	}
}

func TestRuntimeClassUIDGuards(t *testing.T) {
	obj := liveClass()
	obj.UID = "successor"
	live := obj.DeepCopy()
	getUID := obj.UID
	c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			copy := obj.DeepCopy()
			copy.UID = getUID
			respond(t, w, copy)
		case "PATCH":
			if err := patchClass(r, obj); err != nil {
				fail(t, w, 409, metav1.StatusReasonConflict)
				return
			}
			t.Error("patch changed the successor")
			respond(t, w, obj)
		case "DELETE":
			var options metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
			}
			assert.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid-1"))},
				options.Preconditions)
			fail(t, w, 409, metav1.StatusReasonConflict)
		}
	})
	p := savedClass(c)
	r := p.Inputs
	_, err := r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed from uid-1 to successor")
	r.Metadata.Annotations = new(map[string]string{"different": "value"})
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed from uid-1 to successor")
	getUID = "uid-1"
	_, err = r.Update(t.Context(), c, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.True(t, apierrors.IsConflict(r.Delete(t.Context(), c, p)))
	require.Equal(t, live, obj)
	got := calls()
	get := request{method: "GET", path: examplePath}
	require.Equal(t, []request{get, get, get, {
		method: "PATCH", path: examplePath,
		body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
			`{"op":"add","path":"/metadata/annotations","value":{"different":"value"}}]`,
	}, {method: "DELETE", path: examplePath, body: got[4].body}}, got)

	p.Outputs.Metadata.UID = ""
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "missing RuntimeClass identity")
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "missing RuntimeClass identity")
	require.ErrorContains(t, r.Delete(t.Context(), c, p), "missing RuntimeClass identity")
	require.Equal(t, got, calls())
}

func TestRuntimeClassDirectUpdateRejectsReplacement(t *testing.T) {
	c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	p := savedClass(c)
	r := p.Inputs
	r.Handler = "run-2"
	_, err := r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	r = p.Inputs
	r.Metadata.Name = new("different")
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	require.Empty(t, calls())
}

func TestRuntimeClassDeleteAcknowledgmentAndCancellation(t *testing.T) {
	obj := liveClass()
	obj.Finalizers = []string{"example.org/hold"}
	c, calls := runtimeClassAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		respond(t, w, obj)
	})
	p := savedClass(c)
	r := p.Inputs
	require.NoError(t, r.Delete(t.Context(), c, p))
	got := calls()
	require.Equal(t, []request{{method: "DELETE", path: examplePath, body: got[0].body}}, got)
	require.Equal(t, []string{"example.org/hold"}, obj.Finalizers)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := r.Delete(ctx, c, p)
	require.ErrorContains(t, err, "delete RuntimeClass")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, got, calls())
}
