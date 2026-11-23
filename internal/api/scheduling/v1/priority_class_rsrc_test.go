package v1_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	public "github.com/cloudboss/unobin-library-kubernetes/api/scheduling/v1"
	"github.com/cloudboss/unobin-library-kubernetes/config"
	scheduling "github.com/cloudboss/unobin-library-kubernetes/internal/api/scheduling/v1"
)

const classPath = "/apis/scheduling.k8s.io/v1/priorityclasses"

type request struct {
	method string
	path   string
	token  string
	body   string
}

func classAPI(
	t *testing.T, handler func(http.ResponseWriter, request),
) (*config.Configuration, *[]request) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var requests []request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		call := request{req.Method, req.URL.Path, req.Header.Get("Authorization"), string(body)}
		requests = append(requests, call)
		if req.Method == http.MethodPatch {
			if req.Header.Get("Content-Type") != string(types.JSONPatchType) {
				t.Errorf("unexpected patch type %q", req.Header.Get("Content-Type"))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, call)
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: new(true)}, &requests
}

func response(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func TestPriorityClassCreateRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*scheduling.PriorityClassResource)
		body string
	}{
		{"omitted defaults", func(r *scheduling.PriorityClassResource) {},
			`{"metadata":{"name":"example","creationTimestamp":null},` +
				`"value":0,"preemptionPolicy":"PreemptLowerPriority"}`},
		{"explicit defaults", func(r *scheduling.PriorityClassResource) {
			r.Description, r.GlobalDefault, r.PreemptionPolicy =
				new(""), new(false), new("PreemptLowerPriority")
			r.Metadata.Annotations, r.Metadata.Labels =
				new(map[string]string{}), new(map[string]string{})
		}, `{"metadata":{"name":"example","creationTimestamp":null},` +
			`"value":0,"preemptionPolicy":"PreemptLowerPriority"}`},
		{"generated negative", func(r *scheduling.PriorityClassResource) {
			r.Metadata.Name, r.Metadata.GenerateName = nil, new("example-")
			r.Metadata.Annotations = new(map[string]string{"example.com/desc": "annotation"})
			r.Metadata.Labels = new(map[string]string{"example.com/team": "label"})
			r.Value, r.Description, r.GlobalDefault, r.PreemptionPolicy =
				-19, new("example priority"), new(true), new("Never")
		}, `{"metadata":{"generateName":"example-","creationTimestamp":null,` +
			`"annotations":{"example.com/desc":"annotation"},"labels":{"example.com/team":"label"}},` +
			`"value":-19,"description":"example priority","globalDefault":true,` +
			`"preemptionPolicy":"Never"}`},
		{"narrowed", func(r *scheduling.PriorityClassResource) {
			r.Value = 2147483648
		}, `{"metadata":{"name":"example","creationTimestamp":null},` +
			`"value":-2147483648,"preemptionPolicy":"PreemptLowerPriority"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const actualName = "example-actual"
			c, calls := classAPI(t, func(w http.ResponseWriter, req request) {
				obj := &schedulingv1.PriorityClass{
					ObjectMeta: metav1.ObjectMeta{Name: actualName, UID: "uid", ResourceVersion: "5"},
					Value:      -42, Description: "refreshed", GlobalDefault: true,
				}
				switch req.method {
				case http.MethodPost:
					response(t, w, &schedulingv1.PriorityClass{
						ObjectMeta: metav1.ObjectMeta{Name: actualName, UID: "uid"},
						Value:      91,
					})
				case http.MethodGet:
					response(t, w, obj)
				default:
					t.Error(fmt.Errorf("unexpected method %s", req.method))
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			})
			r := &scheduling.PriorityClassResource{
				Metadata: scheduling.PriorityClassMetadata{Name: new("example")},
			}
			tc.edit(r)
			got, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			post := (*calls)[0]
			require.Equal(t, []request{
				{method: "POST", path: classPath, body: post.body},
				{method: "GET", path: classPath + "/" + actualName},
			}, *calls)
			want := `{"kind":"PriorityClass","apiVersion":"scheduling.k8s.io/v1",` + tc.body[1:]
			require.JSONEq(t, want, post.body)
			require.Equal(t, &scheduling.PriorityClassResourceOutput{
				Server: *c.Server, APIVersion: "scheduling.k8s.io/v1", Kind: "PriorityClass",
				Metadata: scheduling.PriorityClassMetadataOutput{
					Name: actualName, UID: "uid",
					Annotations: map[string]string{}, Labels: map[string]string{},
				},
				Value: -42, Description: "refreshed", GlobalDefault: true,
			}, got)
		})
	}
}

type prior = runtime.Prior[
	scheduling.PriorityClassResource, *scheduling.PriorityClassResourceOutput, *config.Configuration,
]

func savedClass(c *config.Configuration, name, uid string) *scheduling.PriorityClassResourceOutput {
	return &scheduling.PriorityClassResourceOutput{
		Server: *c.Server, APIVersion: "scheduling.k8s.io/v1", Kind: "PriorityClass",
		Metadata:         scheduling.PriorityClassMetadataOutput{Name: name, UID: uid},
		PreemptionPolicy: "PreemptLowerPriority",
	}
}

func failure(t *testing.T, w http.ResponseWriter, code int, reason metav1.StatusReason) {
	t.Helper()
	w.WriteHeader(code)
	response(t, w, metav1.Status{
		Status: "Failure", Reason: reason, Code: int32(code), Message: string(reason),
	})
}

func patchClass(body string, obj *schedulingv1.PriorityClass) (*schedulingv1.PriorityClass, error) {
	patch, err := jsonpatch.DecodePatch([]byte(body))
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(struct {
		*schedulingv1.PriorityClass
		Description   string `json:"description"`
		GlobalDefault bool   `json:"globalDefault"`
	}{obj, obj.Description, obj.GlobalDefault})
	if err != nil {
		return nil, err
	}
	changed, err := patch.Apply(encoded)
	if err != nil {
		return nil, err
	}
	out := &schedulingv1.PriorityClass{}
	return out, json.Unmarshal(changed, out)
}

func stateAPI(
	t *testing.T, afterCreate func(*schedulingv1.PriorityClass),
) (*config.Configuration, *[]request, map[string]*schedulingv1.PriorityClass) {
	t.Helper()
	objects := map[string]*schedulingv1.PriorityClass{}
	nextUID := 0
	c, calls := classAPI(t, func(w http.ResponseWriter, req request) {
		name := path.Base(req.path)
		obj := objects[name]
		switch req.method {
		case http.MethodPost:
			created := &schedulingv1.PriorityClass{}
			if err := json.Unmarshal([]byte(req.body), created); err != nil {
				t.Error(err)
				failure(t, w, 400, metav1.StatusReasonBadRequest)
				return
			}
			if created.Name == "" {
				created.Name = created.GenerateName + "actual"
			}
			if objects[created.Name] != nil {
				failure(t, w, 409, metav1.StatusReasonAlreadyExists)
				return
			}
			nextUID++
			created.UID = types.UID(fmt.Sprintf("uid-%d", nextUID))
			objects[created.Name] = created
			response(t, w, created)
			if afterCreate != nil {
				afterCreate(created)
			}
		case http.MethodGet:
			if obj == nil {
				failure(t, w, 404, metav1.StatusReasonNotFound)
				return
			}
			response(t, w, obj)
		case http.MethodPatch:
			if obj == nil {
				failure(t, w, 404, metav1.StatusReasonNotFound)
				return
			}
			updated, err := patchClass(req.body, obj)
			if err != nil {
				failure(t, w, 422, metav1.StatusReasonInvalid)
				return
			}
			objects[name] = updated
			response(t, w, &schedulingv1.PriorityClass{
				ObjectMeta: metav1.ObjectMeta{Name: name, UID: obj.UID},
				Value:      91, Description: "patch response",
			})
		case http.MethodDelete:
			if obj == nil {
				failure(t, w, 404, metav1.StatusReasonNotFound)
				return
			}
			var options metav1.DeleteOptions
			if err := json.Unmarshal([]byte(req.body), &options); err != nil {
				t.Error(err)
				failure(t, w, 400, metav1.StatusReasonBadRequest)
				return
			}
			if options.Preconditions == nil || options.Preconditions.UID == nil ||
				*options.Preconditions.UID != obj.UID {
				failure(t, w, 409, metav1.StatusReasonConflict)
				return
			}
			delete(objects, name)
			response(t, w, metav1.Status{Status: "Success"})
		default:
			t.Errorf("unexpected request %s %s", req.method, req.path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return c, calls, objects
}

func getClass(name string) request {
	return request{method: "GET", path: classPath + "/" + name}
}

func deleteClass(name, uid string) request {
	body := fmt.Sprintf(`{"kind":"DeleteOptions","apiVersion":"scheduling.k8s.io/v1",`+
		`"preconditions":{"uid":%q}}`+"\n", uid)
	return request{method: "DELETE", path: classPath + "/" + name, body: body}
}

func TestPriorityClassMetadataProjection(t *testing.T) {
	c, calls, objects := stateAPI(t, nil)
	raw := map[string]string{
		"tracking.kubernetes.io/explicit":                        "tracked",
		"ignored.example.org/explicit":                           "tracked regex",
		"controller.kubernetes.io/hidden":                        "internal",
		"deprecated.daemonset.template.generation":               "marker",
		"app.kubernetes.io/name":                                 "exempt",
		"service.beta.kubernetes.io/load-balancer-source-ranges": "exempt",
		"ignored.example.org/managed":                            "matched regex",
		"ordinary":                                               "ordinary",
	}
	objects["example"] = &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", UID: "uid", GenerateName: "example-",
			Annotations: raw, Labels: raw, ResourceVersion: "345", Generation: 36,
		},
		Value: -3, Description: "observed", GlobalDefault: true,
	}
	tracked := map[string]string{
		"tracking.kubernetes.io/explicit": "desired", "ignored.example.org/explicit": "desired",
	}
	r := &scheduling.PriorityClassResource{
		IgnoreAnnotations: new([]string{`^ignored[.]example[.]org/`}),
		IgnoreLabels:      new([]string{`^ignored[.]example[.]org/`}),
		Metadata:          scheduling.PriorityClassMetadata{Annotations: &tracked, Labels: &tracked},
	}
	saved := savedClass(c, "example", "uid")
	projected, err := r.Read(t.Context(), c, prior{Inputs: *r, Outputs: saved})
	require.NoError(t, err)
	want := map[string]string{
		"tracking.kubernetes.io/explicit":                        "tracked",
		"ignored.example.org/explicit":                           "tracked regex",
		"app.kubernetes.io/name":                                 "exempt",
		"service.beta.kubernetes.io/load-balancer-source-ranges": "exempt",
		"ordinary": "ordinary",
	}
	require.Equal(t, &scheduling.PriorityClassResourceOutput{
		Server: *c.Server, APIVersion: "scheduling.k8s.io/v1", Kind: "PriorityClass",
		Metadata: scheduling.PriorityClassMetadataOutput{
			Name: "example", UID: "uid", GenerateName: "example-", Labels: want, Annotations: want,
		},
		Value: -3, Description: "observed", GlobalDefault: true, PreemptionPolicy: "",
	}, projected)
	require.Equal(t, []request{getClass("example")}, *calls)
}

func TestPriorityClassUpdatePatch(t *testing.T) {
	c, calls, objects := stateAPI(t, nil)
	r := &scheduling.PriorityClassResource{
		Metadata: scheduling.PriorityClassMetadata{GenerateName: new("example-")},
	}
	baseline, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	name := "example-actual"
	require.Equal(t, name, baseline.Metadata.Name)
	require.Equal(t, "example-", baseline.Metadata.GenerateName)
	require.Equal(t, []request{
		{method: "POST", path: classPath, body: (*calls)[0].body}, getClass(name),
	}, *calls)

	live := objects[name]
	live.Annotations = map[string]string{
		"example.com/stale": "gone", "example.com/change": "old",
		"kubernetes.io/controller": "kept",
	}
	live.Labels = map[string]string{"example.com/owner": "old", "kubernetes.io/controller": "kept"}
	observation, err := r.Read(t.Context(), c, prior{Inputs: *r, Outputs: baseline})
	require.NoError(t, err)
	r.Metadata.Annotations = new(map[string]string{
		"example.com/add": "", "example.com/change": "new",
	})
	r.Metadata.Labels = new(map[string]string{"example.com/owner": "new"})
	r.Description, r.GlobalDefault = new("configured"), new(true)
	before := len(*calls)
	updated, err := r.Update(t.Context(), c, prior{Outputs: baseline, Observed: observation})
	require.NoError(t, err)
	require.Equal(t, []request{
		{method: "PATCH", path: classPath + "/" + name,
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"add","path":"/metadata/annotations/example.com~1add","value":""},` +
				`{"op":"replace","path":"/metadata/annotations/example.com~1change","value":"new"},` +
				`{"op":"remove","path":"/metadata/annotations/example.com~1stale"},` +
				`{"op":"replace","path":"/metadata/labels/example.com~1owner","value":"new"},` +
				`{"op":"replace","path":"/description","value":"configured"},` +
				`{"op":"replace","path":"/globalDefault","value":true}]`},
		getClass(name),
	}, (*calls)[before:])
	require.Equal(t, &scheduling.PriorityClassResourceOutput{
		Server: *c.Server, APIVersion: "scheduling.k8s.io/v1", Kind: "PriorityClass",
		Metadata: scheduling.PriorityClassMetadataOutput{
			Name: name, GenerateName: "example-", UID: "uid-1",
			Annotations: *r.Metadata.Annotations, Labels: *r.Metadata.Labels,
		},
		Description: "configured", GlobalDefault: true, PreemptionPolicy: "PreemptLowerPriority",
	}, updated)
	require.Equal(t, map[string]string{
		"example.com/add": "", "example.com/change": "new", "kubernetes.io/controller": "kept",
	}, objects[name].Annotations)
	require.Equal(t, map[string]string{
		"example.com/owner": "new", "kubernetes.io/controller": "kept",
	}, objects[name].Labels)

	p := prior{Inputs: *r, Outputs: updated, Observed: updated}
	r.Metadata.Annotations, r.Metadata.Labels = new(map[string]string{}), nil
	r.Description, r.GlobalDefault = nil, nil
	before = len(*calls)
	cleared, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []request{
		{method: "PATCH", path: classPath + "/" + name,
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"remove","path":"/metadata/annotations/example.com~1add"},` +
				`{"op":"remove","path":"/metadata/annotations/example.com~1change"},` +
				`{"op":"remove","path":"/metadata/labels/example.com~1owner"},` +
				`{"op":"replace","path":"/description","value":""},` +
				`{"op":"replace","path":"/globalDefault","value":false}]`},
		getClass(name),
	}, (*calls)[before:])
	require.Equal(t, "", cleared.Description)
	require.False(t, cleared.GlobalDefault)
	require.Equal(t, map[string]string{}, cleared.Metadata.Labels)
	require.Equal(t, map[string]string{}, cleared.Metadata.Annotations)
	require.Equal(t, map[string]string{"kubernetes.io/controller": "kept"}, objects[name].Annotations)
	require.Equal(t, map[string]string{"kubernetes.io/controller": "kept"}, objects[name].Labels)

	p = prior{Inputs: *r, Outputs: cleared, Observed: cleared}
	r.Metadata.Annotations = new(map[string]string{"example.com/add": ""})
	r.Metadata.Labels = new(map[string]string{"example.com/owner": "new"})
	before = len(*calls)
	added, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []request{
		{method: "PATCH", path: classPath + "/" + name,
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"add","path":"/metadata/annotations","value":{"example.com/add":""}},` +
				`{"op":"add","path":"/metadata/labels","value":{"example.com/owner":"new"}}]`},
		getClass(name),
	}, (*calls)[before:])
	require.Equal(t, map[string]string{"example.com/add": ""}, objects[name].Annotations)
	require.Equal(t, map[string]string{"example.com/owner": "new"}, objects[name].Labels)

	before = len(*calls)
	for range 2 {
		require.NoError(t, r.Delete(t.Context(), c, prior{Outputs: added}))
	}
	require.Equal(t, []request{
		deleteClass(name, "uid-1"), deleteClass(name, "uid-1"),
	}, (*calls)[before:])
}

func TestPriorityClassAdoptsPreviouslyHiddenKey(t *testing.T) {
	c, calls, objects := stateAPI(t, nil)
	objects["example"] = &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", UID: "uid",
			Annotations: map[string]string{"kubernetes.io/adopt": "old", "kubernetes.io/keep": "keep"},
		},
	}
	saved := savedClass(c, "example", "uid")
	r := &scheduling.PriorityClassResource{
		Metadata: scheduling.PriorityClassMetadata{Name: new("example")},
	}
	old, err := r.Read(t.Context(), c, prior{Outputs: saved})
	require.NoError(t, err)
	require.Equal(t, map[string]string{}, old.Metadata.Annotations)
	r.Metadata.Annotations = new(map[string]string{"kubernetes.io/adopt": "new"})
	updated, err := r.Update(t.Context(), c, prior{Outputs: saved, Observed: old})
	require.NoError(t, err)
	require.Equal(t, []request{
		getClass("example"), getClass("example"),
		{method: "PATCH", path: classPath + "/example",
			body: `[{"op":"test","path":"/metadata/uid","value":"uid"},` +
				`{"op":"replace","path":"/metadata/annotations/kubernetes.io~1adopt","value":"new"}]`},
		getClass("example"),
	}, *calls)
	require.Equal(t, map[string]string{"kubernetes.io/adopt": "new"}, updated.Metadata.Annotations)
	require.Equal(t, map[string]string{
		"kubernetes.io/adopt": "new", "kubernetes.io/keep": "keep",
	}, objects["example"].Annotations)
}

func TestPriorityClassSavedIdentityGuards(t *testing.T) {
	oldConfig, oldCalls, objects := stateAPI(t, nil)
	current, currentCalls, _ := stateAPI(t, nil)
	current.Token = new("current")
	objects["example"] = &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "previous"},
	}
	r := &scheduling.PriorityClassResource{
		Metadata: scheduling.PriorityClassMetadata{Name: new("example")}, Description: new("changed"),
	}
	saved := savedClass(oldConfig, "example", "previous")
	p := prior{Outputs: saved}
	observed, err := r.Read(t.Context(), current, p)
	require.NoError(t, err)
	p.Observed = observed
	objects["example"].UID = "foreign"
	_, err = r.Read(t.Context(), current, p)
	require.ErrorContains(t, err, "UID changed from previous to foreign")
	_, err = r.Update(t.Context(), current, p)
	require.True(t, apierrors.IsInvalid(err), "%v", err)
	err = r.Delete(t.Context(), current, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	get := getClass("example")
	get.token = "Bearer current"
	del := deleteClass("example", "previous")
	del.token = "Bearer current"
	require.Equal(t, []request{
		get, get,
		{method: "PATCH", path: classPath + "/example", token: "Bearer current",
			body: `[{"op":"test","path":"/metadata/uid","value":"previous"},` +
				`{"op":"replace","path":"/description","value":"changed"}]`},
		del,
	}, *oldCalls)
	require.Empty(t, *currentCalls)
	require.Equal(t, types.UID("foreign"), objects["example"].UID)
	require.Empty(t, objects["example"].Description)
}

func TestPriorityClassOperationErrors(t *testing.T) {
	r := &scheduling.PriorityClassResource{
		Metadata: scheduling.PriorityClassMetadata{Name: new("example")}, Description: new("changed"),
	}
	for _, tc := range []struct {
		name   string
		method string
		code   int
		reason metav1.StatusReason
		check  func(error) bool
	}{
		{"existing", "POST", 409, metav1.StatusReasonAlreadyExists, apierrors.IsAlreadyExists},
		{"admission", "POST", 422, metav1.StatusReasonInvalid, apierrors.IsInvalid},
		{"forbidden", "GET", 403, metav1.StatusReasonForbidden, apierrors.IsForbidden},
		{"patch conflict", "PATCH", 409, metav1.StatusReasonConflict, apierrors.IsConflict},
		{"delete forbidden", "DELETE", 403, metav1.StatusReasonForbidden, apierrors.IsForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := classAPI(t, func(w http.ResponseWriter, req request) {
				failure(t, w, tc.code, tc.reason)
			})
			saved := savedClass(c, "example", "uid")
			p := prior{Outputs: saved, Observed: saved}
			var err error
			switch tc.method {
			case "POST":
				r.Value = 1000000001
				_, err = r.Create(t.Context(), c)
			case "GET":
				_, err = r.Read(t.Context(), c, p)
			case "PATCH":
				_, err = r.Update(t.Context(), c, p)
			case "DELETE":
				err = r.Delete(t.Context(), c, p)
			}
			require.True(t, tc.check(err), "%v", err)
			want := getClass("example")
			want.method = tc.method
			switch tc.method {
			case "POST":
				want.path, want.body = classPath, (*calls)[0].body
			case "PATCH":
				want.body = `[{"op":"test","path":"/metadata/uid","value":"uid"},` +
					`{"op":"replace","path":"/description","value":"changed"}]`
			case "DELETE":
				want = deleteClass("example", "uid")
			}
			require.Equal(t, []request{want}, *calls)
		})
	}
}

func TestPriorityClassRefreshMissingAndCancellation(t *testing.T) {
	c, calls := classAPI(t, func(w http.ResponseWriter, req request) {
		switch req.method {
		case "GET":
			failure(t, w, 404, metav1.StatusReasonNotFound)
		case "POST", "PATCH":
			response(t, w, &schedulingv1.PriorityClass{
				ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "uid"},
			})
		default:
			t.Errorf("unexpected method %s", req.method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	r := &scheduling.PriorityClassResource{
		Metadata: scheduling.PriorityClassMetadata{Name: new("example")}, Description: new("changed"),
	}
	saved := savedClass(c, "example", "uid")
	p := prior{Outputs: saved, Observed: saved}
	_, err := r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	_, err = r.Create(t.Context(), c)
	require.True(t, apierrors.IsNotFound(err), "%v", err)
	require.NotErrorIs(t, err, runtime.ErrNotFound)
	_, err = r.Update(t.Context(), c, p)
	require.True(t, apierrors.IsNotFound(err), "%v", err)
	require.NotErrorIs(t, err, runtime.ErrNotFound)
	get := getClass("example")
	require.Equal(t, []request{
		get, {method: "POST", path: classPath, body: (*calls)[1].body}, get,
		{method: "PATCH", path: classPath + "/example",
			body: `[{"op":"test","path":"/metadata/uid","value":"uid"},` +
				`{"op":"replace","path":"/description","value":"changed"}]`},
		get,
	}, *calls)
	before := len(*calls)
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	err = r.Delete(ctx, c, p)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, (*calls)[before:])
}

var priorityClassSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/scheduling/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

func priorityLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := priorityClassSchema()
	require.NoError(t, err)
	lib := public.Library()
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.priority-class": schema.Resources["priority-class"].Constraints,
	}
	return lib
}

func classExecutor(
	t *testing.T, fixture, dir string, lib *runtime.Library, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub", fixture))
	require.NoError(t, err)
	src, err := syntax.ParseSource(filepath.Base(fixture), body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"scheduling": lib}
	store, err := local.NewStore(dir, "priority-class", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(src.Factory.Body, libs), SyntaxSource: &src.Factory.Body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "priority-class", Version: "v0", ContentRevision: "c0"},
	}
}

func applySaved(t *testing.T, e *runtime.Executor, p *runtime.Plan) error {
	t.Helper()
	encoded, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), file)
	return err
}

func classStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.class" {
			return step
		}
	}
	t.Fatal("plan omitted resource.class")
	return nil
}

func planClass(
	t *testing.T, e *runtime.Executor, want runtime.Decision,
) (*runtime.Plan, *runtime.PlanStep) {
	t.Helper()
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	step := classStep(t, p)
	require.Equal(t, want, step.Decision)
	return p, step
}

func classInputs(c *config.Configuration) map[string]any {
	return map[string]any{
		"server": *c.Server, "metadata": map[string]any{"name": "example"}, "value": 1,
		"token": nil, "kubeconfig": nil, "context": nil, "description": nil, "global-default": nil,
		"policy": nil, "ignore-annotations": nil, "ignore-labels": nil,
	}
}

func TestPriorityClassPlanLifecycle(t *testing.T) {
	c, calls, objects := stateAPI(t, nil)
	kubeconfig, err := filepath.Abs("../../../client/testdata/contexts.yaml")
	require.NoError(t, err)
	inputs := classInputs(c)
	dir, lib := t.TempDir(), priorityLibrary(t)
	executor := func() *runtime.Executor { return classExecutor(t, "factory.ub", dir, lib, inputs) }
	apply := func(decision runtime.Decision) *runtime.PlanStep {
		t.Helper()
		e := executor()
		p, step := planClass(t, e, decision)
		require.NoError(t, applySaved(t, e, p))
		return step
	}
	apply(runtime.DecisionCreate)
	require.Equal(t, types.UID("uid-1"), objects["example"].UID)

	inputs["description"], inputs["global-default"] = "", false
	inputs["policy"] = "PreemptLowerPriority"
	inputs["metadata"] = map[string]any{"name": "example", "annotations": map[string]any{}}
	objects["example"].ResourceVersion = "next"
	objects["example"].Generation = 3
	before := len(*calls)
	step := apply(runtime.DecisionNoOp)
	require.False(t, step.Drift())
	require.Equal(t, []request{getClass("example")}, (*calls)[before:])

	for _, tc := range []struct {
		field  string
		value  any
		reason string
	}{
		{"metadata", map[string]any{"name": "renamed"}, "metadata"},
		{"value", 2, "value"},
		{"policy", "Never", "preemption-policy"},
		{"kubeconfig", kubeconfig, "kubeconfig"},
		{"context", "prod", "current-context"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			if tc.field == "context" {
				t.Setenv("KUBECONFIG", kubeconfig)
			}
			initial := inputs[tc.field]
			inputs[tc.field] = tc.value
			defer func() { inputs[tc.field] = initial }()
			_, step := planClass(t, executor(), runtime.DecisionReplace)
			require.NotNil(t, step.ExpectedStableID)
			require.Equal(t, *c.Server+"/uid-1", *step.ExpectedStableID)
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
		})
	}

	obj := objects["example"]
	for _, tc := range []struct {
		field  string
		change func(*schedulingv1.PriorityClass)
	}{
		{"value", func(o *schedulingv1.PriorityClass) { o.Value-- }},
		{"preemption-policy", func(o *schedulingv1.PriorityClass) { o.PreemptionPolicy = nil }},
	} {
		t.Run("drift/"+tc.field, func(t *testing.T) {
			objects["example"] = obj.DeepCopy()
			defer func() { objects["example"] = obj }()
			tc.change(objects["example"])
			_, step := planClass(t, executor(), runtime.DecisionReplace)
			require.NotNil(t, step.ExpectedStableID)
			require.Equal(t, *c.Server+"/uid-1", *step.ExpectedStableID)
			require.Equal(t, []string{tc.field}, step.ReplacementReasons)
		})
	}

	obj.Description, obj.GlobalDefault = "outside", true
	before = len(*calls)
	apply(runtime.DecisionUpdate)
	require.Equal(t, []request{
		getClass("example"),
		{method: "PATCH", path: classPath + "/example",
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"replace","path":"/description","value":""},` +
				`{"op":"replace","path":"/globalDefault","value":false}]`},
		getClass("example"),
	}, (*calls)[before:])
	require.Equal(t, types.UID("uid-1"), objects["example"].UID)

	inputs["policy"], inputs["value"] = "Never", 9
	before = len(*calls)
	apply(runtime.DecisionReplace)
	require.Equal(t, []request{
		getClass("example"), getClass("example"), deleteClass("example", "uid-1"),
		{method: "POST", path: classPath, body: (*calls)[before+3].body}, getClass("example"),
	}, (*calls)[before:])
	require.JSONEq(t, `{
		"kind":"PriorityClass","apiVersion":"scheduling.k8s.io/v1",
		"metadata":{"name":"example","creationTimestamp":null},"value":9,"preemptionPolicy":"Never"
	}`, (*calls)[before+3].body)
	require.Equal(t, types.UID("uid-2"), objects["example"].UID)
	require.Equal(t, corev1.PreemptNever, *objects["example"].PreemptionPolicy)

	inputs["policy"] = nil
	_, step = planClass(t, executor(), runtime.DecisionReplace)
	require.Equal(t, []string{"preemption-policy"}, step.ReplacementReasons)
	delete(objects, "example")
	before = len(*calls)
	step = apply(runtime.DecisionCreate)
	require.True(t, step.Gone())
	require.Equal(t, []request{
		getClass("example"), {method: "POST", path: classPath, body: (*calls)[before+1].body},
		getClass("example"),
	}, (*calls)[before:])
	require.Equal(t, types.UID("uid-3"), objects["example"].UID)
}

func TestPriorityClassPlanReplacesSavedServer(t *testing.T) {
	current, currentCalls, currentObjects := stateAPI(t, nil)
	future, futureCalls, futureObjects := stateAPI(t, nil)
	inputs := classInputs(current)
	dir, lib := t.TempDir(), priorityLibrary(t)
	e := classExecutor(t, "factory.ub", dir, lib, inputs)
	p, _ := planClass(t, e, runtime.DecisionCreate)
	require.NoError(t, applySaved(t, e, p))

	inputs["server"], inputs["token"] = *future.Server, "replacement"
	e = classExecutor(t, "factory.ub", dir, lib, inputs)
	before := len(*currentCalls)
	p, step := planClass(t, e, runtime.DecisionReplace)
	require.Equal(t, []string{"server"}, step.ReplacementReasons)
	require.NoError(t, applySaved(t, e, p))
	get, del := getClass("example"), deleteClass("example", "uid-1")
	get.token, del.token = "Bearer replacement", "Bearer replacement"
	require.Equal(t, []request{get, get, del}, (*currentCalls)[before:])
	require.Equal(t, []request{
		{method: "POST", path: classPath, token: "Bearer replacement", body: (*futureCalls)[0].body},
		get,
	}, *futureCalls)
	require.Empty(t, currentObjects)
	require.Equal(t, "example", futureObjects["example"].Name)
}

func TestPriorityClassSavedMetadataFilters(t *testing.T) {
	c, calls, objects := stateAPI(t, func(obj *schedulingv1.PriorityClass) {
		obj.Annotations["example.org/controller"] = "kept"
		obj.Labels["kubernetes.io/controller"] = "hidden"
	})
	inputs := classInputs(c)
	inputs["metadata"] = map[string]any{
		"name": "example", "annotations": map[string]any{"example.org/managed": "tracked"},
		"labels": map[string]any{"app.kubernetes.io/name": "visible"},
	}
	dir, lib := t.TempDir(), priorityLibrary(t)
	executor := func() *runtime.Executor { return classExecutor(t, "factory.ub", dir, lib, inputs) }
	e := executor()
	p, _ := planClass(t, e, runtime.DecisionCreate)
	require.NoError(t, applySaved(t, e, p))
	objects["example"].Labels["kubernetes.io/controller"] = "modified"
	_, step := planClass(t, executor(), runtime.DecisionNoOp)
	require.False(t, step.Drift())

	inputs["ignore-annotations"] = []any{"^example[.]org/"}
	before := len(*calls)
	e = executor()
	p, _ = planClass(t, e, runtime.DecisionUpdate)
	require.NoError(t, applySaved(t, e, p))
	require.Equal(t, []request{getClass("example"), getClass("example")}, (*calls)[before:])
	require.Equal(t, map[string]string{
		"example.org/controller": "kept", "example.org/managed": "tracked",
	}, objects["example"].Annotations)
	_, step = planClass(t, executor(), runtime.DecisionNoOp)
	require.False(t, step.Drift())
	require.Equal(t, map[string]any{
		"name": "example", "generate-name": "", "uid": "uid-1",
		"annotations": map[string]any{"example.org/managed": "tracked"},
		"labels":      map[string]any{"app.kubernetes.io/name": "visible"},
	}, step.ObservedOutputs["metadata"])
}

func TestPriorityClassSavedMetadataFilterChanges(t *testing.T) {
	const (
		broad  = "^(newly-visible|external[.]example/)"
		narrow = "^external[.]example/"
	)
	for _, tc := range []struct {
		name    string
		field   string
		prior   map[string]any
		live    map[string]string
		desired map[string]any
		want    map[string]string
		patch   string
	}{
		{
			name: "clear newly visible annotations", field: "annotations",
			prior: map[string]any{"external.example/managed": "tracked"},
			live: map[string]string{
				"external.example/managed": "tracked",
				"external.example/hidden":  "kept",
				"newly-visible":            "remove",
			},
			desired: map[string]any{},
			want:    map[string]string{"external.example/hidden": "kept"},
			patch: `{"op":"remove","path":"/metadata/annotations/external.example~1managed"},` +
				`{"op":"remove","path":"/metadata/annotations/newly-visible"}`,
		},
		{
			name: "add label while preserving a hidden sibling", field: "labels",
			live: map[string]string{
				"external.example/hidden": "kept", "newly-visible": "remove",
			},
			desired: map[string]any{"added": "desired"},
			want:    map[string]string{"added": "desired", "external.example/hidden": "kept"},
			patch: `{"op":"add","path":"/metadata/labels/added","value":"desired"},` +
				`{"op":"remove","path":"/metadata/labels/newly-visible"}`,
		},
		{
			name: "add a whole map when the current projection is empty", field: "annotations",
			live:    map[string]string{"external.example/hidden": "replaced"},
			desired: map[string]any{"added": "desired"},
			want:    map[string]string{"added": "desired"},
			patch:   `{"op":"add","path":"/metadata/annotations","value":{"added":"desired"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, objects := stateAPI(t, nil)
			inputs := classInputs(c)
			ignores := "ignore-" + tc.field
			inputs[ignores] = []any{broad, narrow}
			if tc.prior != nil {
				inputs["metadata"] = map[string]any{"name": "example", tc.field: tc.prior}
			}
			dir, lib := t.TempDir(), priorityLibrary(t)
			executor := func() *runtime.Executor {
				return classExecutor(t, "factory.ub", dir, lib, inputs)
			}
			e := executor()
			p, _ := planClass(t, e, runtime.DecisionCreate)
			require.NoError(t, applySaved(t, e, p))
			if tc.field == "labels" {
				objects["example"].Labels = tc.live
			} else {
				objects["example"].Annotations = tc.live
			}

			inputs[ignores] = []any{narrow}
			inputs["metadata"] = map[string]any{"name": "example", tc.field: tc.desired}
			e = executor()
			p, _ = planClass(t, e, runtime.DecisionUpdate)
			before := len(*calls)
			require.NoError(t, applySaved(t, e, p))
			require.Equal(t, []request{
				getClass("example"),
				{method: "PATCH", path: classPath + "/example",
					body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` + tc.patch + "]"},
				getClass("example"),
			}, (*calls)[before:])
			if tc.field == "labels" {
				require.Equal(t, tc.want, objects["example"].Labels)
			} else {
				require.Equal(t, tc.want, objects["example"].Annotations)
			}
			_, step := planClass(t, executor(), runtime.DecisionNoOp)
			require.False(t, step.Drift())
			require.Equal(t, tc.desired, step.ObservedOutputs["metadata"].(map[string]any)[tc.field])
		})
	}
}

func requireConstraint(t *testing.T, err error, rule string) {
	t.Helper()
	require.ErrorContains(t, err, "resource.class")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, rule)
}

func TestPriorityClassSavedNoOpAndHistoricalDestroy(t *testing.T) {
	c, calls, objects := stateAPI(t, nil)
	inputs := classInputs(c)
	metadata := map[string]any{"generate-name": "example-"}
	inputs["metadata"] = metadata
	dir, lib := t.TempDir(), priorityLibrary(t)
	executor := func() *runtime.Executor { return classExecutor(t, "factory.ub", dir, lib, inputs) }
	e := executor()
	p, _ := planClass(t, e, runtime.DecisionCreate)
	require.NoError(t, applySaved(t, e, p))
	require.Equal(t, "example-actual", objects["example-actual"].Name)

	metadata["generate-name"] = "changed-"
	_, step := planClass(t, executor(), runtime.DecisionReplace)
	require.NotNil(t, step.ExpectedStableID)
	require.Equal(t, *c.Server+"/uid-1", *step.ExpectedStableID)
	require.Equal(t, []string{"metadata"}, step.ReplacementReasons)
	metadata["generate-name"] = "example-"

	e = executor()
	p, _ = planClass(t, e, runtime.DecisionNoOp)
	before := len(*calls)
	require.NoError(t, applySaved(t, e, p))
	require.Empty(t, (*calls)[before:])

	metadata["name"] = ""
	constraints := lib.Constraints
	lib.Constraints = nil
	e = executor()
	p, _ = planClass(t, e, runtime.DecisionNoOp)
	lib.Constraints = constraints
	before = len(*calls)
	requireConstraint(t, applySaved(t, e, p), "metadata.name")
	require.Empty(t, (*calls)[before:])
	_, err := executor().Plan(t.Context())
	requireConstraint(t, err, "metadata.name")
	for _, req := range (*calls)[before:] {
		require.Equal(t, getClass("example-actual"), req)
	}

	inputs["metadata"] = map[string]any{}
	_, err = executor().Plan(t.Context())
	requireConstraint(t, err, "exactly one")

	e = executor()
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Entries)
	for _, entry := range snapshot.Entries {
		entry.Inputs["metadata"] = map[string]any{}
	}
	revision, err := e.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, e.Store.SetCurrent(revision))

	before = len(*calls)
	destroy := executor()
	destroy.Destroy = true
	p, step = planClass(t, destroy, runtime.DecisionDestroy)
	require.Equal(t, map[string]any{}, step.PriorInputs["metadata"])
	require.NoError(t, applySaved(t, destroy, p))
	require.Equal(t, []request{
		getClass("example-actual"), getClass("example-actual"), deleteClass("example-actual", "uid-1"),
	}, (*calls)[before:])
	require.Empty(t, objects)
}

func TestPriorityClassEvaluatedConstraints(t *testing.T) {
	c, calls, _ := stateAPI(t, nil)
	base := classInputs(c)
	dir, lib := t.TempDir(), priorityLibrary(t)
	for _, tc := range []struct {
		name  string
		field string
		value any
		rule  string
	}{
		{"invalid name", "metadata", map[string]any{"name": "Invalid"}, "metadata.name"},
		{"long name", "metadata", map[string]any{"name": strings.Repeat("a", 254)}, "metadata.name"},
		{"empty prefix", "metadata", map[string]any{"generate-name": ""}, "metadata.generate-name"},
		{"uppercase prefix", "metadata", map[string]any{
			"generate-name": "Ab-",
		}, "metadata.generate-name"},
		{"long prefix", "metadata", map[string]any{
			"generate-name": strings.Repeat("a", 64) + "-",
		}, "metadata.generate-name"},
		{"annotation key", "metadata", map[string]any{
			"name": "example", "annotations": map[string]any{"example.org/?": "value"},
		}, "metadata.annotations keys"},
		{"label key", "metadata", map[string]any{
			"name": "example", "labels": map[string]any{"EXAMPLE.org/Label": "value"},
		}, "metadata.labels keys"},
		{"label value", "metadata", map[string]any{
			"name": "example", "labels": map[string]any{"key": "invalid/value"},
		}, "metadata.labels values"},
		{"label regex", "ignore-labels", []any{"["}, "ignore-labels"},
		{"annotation regex", "ignore-annotations", []any{"["}, "ignore-annotations"},
		{"policy", "policy", "never", "preemption-policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := base[tc.field]
			base[tc.field] = tc.value
			defer func() { base[tc.field] = old }()
			e := classExecutor(t, "factory.ub", dir, lib, base)
			_, err := e.Plan(t.Context())
			requireConstraint(t, err, tc.rule)
			require.Empty(t, *calls)
		})
	}
	base["metadata"] = map[string]any{
		"name":        strings.Repeat("a", 253),
		"annotations": map[string]any{"EXAMPLE.K/İtem": "arbitrary"},
		"labels":      map[string]any{"example.org/Label": ""},
	}
	base["ignore-annotations"], base["ignore-labels"] = []any{""}, []any{}
	planClass(t, classExecutor(t, "factory.ub", dir, lib, base), runtime.DecisionCreate)
	for _, prefix := range []string{"A-", strings.Repeat("a", 63), strings.Repeat("a", 63) + "-"} {
		base["metadata"] = map[string]any{"name": nil, "generate-name": prefix}
		planClass(t, classExecutor(t, "factory.ub", dir, lib, base), runtime.DecisionCreate)
	}
	require.Empty(t, *calls)
}

func TestPriorityClassDeferredConstraints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		expression string
		existing   bool
		value      int
		decision   runtime.Decision
		valid      bool
	}{
		{"invalid create", "[", false, 1, runtime.DecisionCreate, false},
		{"valid create", ".*", false, 1, runtime.DecisionCreate, true},
		{"invalid update", "[", true, 1, runtime.DecisionUpdate, false},
		{"valid update", ".*", true, 1, runtime.DecisionUpdate, true},
		{"invalid replace", "[", true, 2, runtime.DecisionReplace, false},
		{"valid replace", ".*", true, 2, runtime.DecisionReplace, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, objects := stateAPI(t, nil)
			dir, lib := t.TempDir(), priorityLibrary(t)
			if tc.existing {
				initial := classExecutor(t, "factory.ub", dir, lib, classInputs(c))
				p, _ := planClass(t, initial, runtime.DecisionCreate)
				require.NoError(t, applySaved(t, initial, p))
			}
			inputs := map[string]any{
				"server": *c.Server, "expression": tc.expression, "value": tc.value, "policy": nil,
			}
			e := classExecutor(t, "deferred/factory.ub", dir, lib, inputs)
			p, step := planClass(t, e, tc.decision)
			require.Equal(t, map[string][]string{
				"ignore-labels": {"resource.seed.metadata.uid"},
			}, step.UnresolvedInputs)
			before := len(*calls)
			err := applySaved(t, e, p)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, int32(tc.value), objects["example"].Value)
				if tc.decision == runtime.DecisionUpdate {
					require.Equal(t, types.UID("uid-1"), objects["example"].UID)
					require.Equal(t, []request{
						{method: "POST", path: classPath, body: (*calls)[before].body},
						getClass("seed"), getClass("example"), getClass("seed"),
					}, (*calls)[before:])
				}
				return
			}
			requireConstraint(t, err, "ignore-labels")
			require.Equal(t, []request{
				{method: "POST", path: classPath, body: (*calls)[before].body}, getClass("seed"),
			}, (*calls)[before:])
			require.NotEmpty(t, objects["seed"].UID)
			if tc.existing {
				require.Equal(t, types.UID("uid-1"), objects["example"].UID)
			} else {
				require.NotContains(t, objects, "example")
			}
		})
	}
}

func TestPriorityClassRejectsSettledRuleWithPendingInput(t *testing.T) {
	c, calls, _ := stateAPI(t, nil)
	inputs := map[string]any{
		"server": *c.Server, "expression": "^valid", "value": 1, "policy": "never",
	}
	e := classExecutor(t, "deferred/factory.ub", t.TempDir(), priorityLibrary(t), inputs)
	_, err := e.Plan(t.Context())
	requireConstraint(t, err, "preemption-policy")
	require.Empty(t, *calls)
}
