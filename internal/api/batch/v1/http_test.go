package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func testAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			body = metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "batch", Versions: []metav1.GroupVersionForDiscovery{{
					GroupVersion: "batch/v1", Version: "v1",
				}}, PreferredVersion: metav1.GroupVersionForDiscovery{
					GroupVersion: "batch/v1", Version: "v1",
				},
			}}}
		case "/api/v1":
			body = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			}}
		case "/apis/batch/v1":
			body = metav1.APIResourceList{GroupVersion: "batch/v1", APIResources: []metav1.APIResource{
				{Name: "jobs", Kind: "Job", Namespaced: true},
			}}
		default:
			handle(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{
		Server: &server.URL, Namespace: new("ignored"), InsecureSkipTLSVerify: new(true),
	}
}

func apiError(t *testing.T, w http.ResponseWriter, code int) {
	t.Helper()
	w.WriteHeader(code)
	reason := metav1.StatusReasonForbidden
	switch code {
	case 404:
		reason = metav1.StatusReasonNotFound
	case 409:
		reason = metav1.StatusReasonAlreadyExists
	}
	require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   "Failure", Message: "API diagnostic", Reason: reason, Code: int32(code),
	}))
}

func readyJob(t *testing.T) *batchv1.Job {
	t.Helper()
	obj, err := job().expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	obj.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	return obj
}

func patchJob(t *testing.T, req *http.Request, obj *batchv1.Job) []patchOperation {
	t.Helper()
	require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	var ops []patchOperation
	require.NoError(t, json.Unmarshal(body, &ops))
	patch, err := jsonpatch.DecodePatch(body)
	require.NoError(t, err)
	before, err := json.Marshal(obj)
	require.NoError(t, err)
	after, err := patch.Apply(before)
	require.NoError(t, err)
	updated := &batchv1.Job{}
	require.NoError(t, json.Unmarshal(after, updated))
	*obj = *updated
	return ops
}

func TestJobLifecycle(t *testing.T) {
	var stored *batchv1.Job
	var methods []string
	var patches [][]patchOperation
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		base := "/apis/batch/v1/namespaces/default/jobs"
		if req.Method == "POST" {
			require.Equal(t, base, req.URL.Path)
			stored = &batchv1.Job{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			require.Empty(t, stored.Name)
			require.Equal(t, "example-", stored.GenerateName)
			stored.Name = "example-abcde"
			stored.UID = "uid"
			stored.Spec.Template.Spec.ServiceAccountName = "default"
			stored.Spec.Template.Spec.Containers[0].ImagePullPolicy = "IfNotPresent"
		} else {
			require.Equal(t, base+"/example-abcde", req.URL.Path)
		}
		require.Empty(t, req.URL.RawQuery)
		switch req.Method {
		case "PATCH":
			patches = append(patches, patchJob(t, req, stored))
		case "DELETE":
			var opts metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&opts))
			require.Equal(t, types.UID("uid"), *opts.Preconditions.UID)
			require.Equal(t, metav1.DeletePropagationForeground, *opts.PropagationPolicy)
			require.Nil(t, opts.GracePeriodSeconds)
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		case "GET":
			if stored == nil {
				apiError(t, w, 404)
				return
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	r := job()
	r.Metadata.Name = nil
	r.Metadata.GenerateName = new("example-")
	r.WaitForCompletion = new(false)
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST"}, methods)
	require.Equal(t, "example-abcde", out.Name)
	p := prior{Inputs: *r, Outputs: out, Configuration: c}
	read, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, out, read)
	r.Spec.Parallelism = new(int64(2))
	r.Spec.BackoffLimit = new(int64(8))
	r.Metadata.Labels = new(map[string]string{"example.com/key": ""})
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, "uid", out.UID)
	require.Equal(t, int64(2), *out.Spec.Parallelism)
	require.Contains(t, patches[0], patchOperation{Op: "replace", Path: "/spec/parallelism",
		Value: float64(2)})
	require.Equal(t, "default", stored.Spec.Template.Spec.ServiceAccountName)
	require.Equal(t, corev1.PullIfNotPresent, stored.Spec.Template.Spec.Containers[0].ImagePullPolicy)
	p = prior{Inputs: *r, Outputs: out}
	require.NoError(t, r.Delete(t.Context(), c, p))
	_, err = r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	require.NoError(t, r.Delete(t.Context(), c, p))
}

func TestCompletionSequences(t *testing.T) {
	for _, tc := range []struct {
		name       string
		conditions []batchv1.JobCondition
		code       int
		failure    bool
	}{
		{name: "complete", conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete,
			Status: corev1.ConditionTrue}}},
		{
			name: "failed", conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}, failure: true},
		{name: "complete first", conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete,
			Status: corev1.ConditionTrue}, {Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
		{name: "failed first", conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed,
			Status: corev1.ConditionTrue}, {Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			failure: true},
		{name: "missing", code: 404}, {name: "forbidden", code: 403, failure: true}, {
			name: "server error", code: 500, failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, operation := range []string{"create", "update"} {
				t.Run(operation, func(t *testing.T) {
					obj := readyJob(t)
					gets := 0
					var methods []string
					c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
						methods = append(methods, req.Method)
						if req.Method == "GET" {
							gets++
							if operation == "update" && gets == 1 {
								require.NoError(t, json.NewEncoder(w).Encode(obj))
								return
							}
							poll := gets
							if operation == "update" {
								poll--
							}
							if poll == 1 {
								pending := obj.DeepCopy()
								pending.Status = batchv1.JobStatus{
									Succeeded: 99, Failed: 99, Conditions: []batchv1.JobCondition{
										{
											Type: batchv1.JobComplete, Status: corev1.ConditionFalse}, {
											Type: batchv1.JobFailed, Status: corev1.ConditionUnknown}}}
								require.NoError(t, json.NewEncoder(w).Encode(pending))
								return
							}
							if tc.code != 0 {
								apiError(t, w, tc.code)
								return
							}
							obj.Status.Conditions = tc.conditions
						}
						require.NoError(t, json.NewEncoder(w).Encode(obj))
					})
					r := job()
					r.CreateTimeout = new("2s")
					r.UpdateTimeout = new("2s")
					var out *JobResourceOutput
					var err error
					if operation == "create" {
						out, err = r.Create(t.Context(), c)
					} else {
						saved, e := r.output(obj, *c.Server)
						require.NoError(t, e)
						out, err = r.Update(t.Context(), c, prior{Inputs: *r, Outputs: saved})
					}
					if tc.failure {
						require.Error(t, err)
						require.Nil(t, out)
					} else {
						require.NoError(t, err)
						require.Equal(t, "uid", out.UID)
					}
					require.NotContains(t, methods, "DELETE")
					require.Equal(t, 2+map[string]int{"create": 0, "update": 1}[operation], gets)
					if tc.code == 404 {
						_, err = r.Read(t.Context(), c, prior{Inputs: *r, Outputs: out})
						require.ErrorIs(t, err, runtime.ErrNotFound)
					}
				})
			}
		})
	}
}

func TestDisabledWaitAndFailures(t *testing.T) {
	for _, code := range []int{403, 409, 422, 429, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var methods []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				apiError(t, w, code)
			})
			_, err := job().Create(t.Context(), c)
			require.ErrorContains(t, err, "API diagnostic")
			require.Equal(t, []string{"POST"}, methods)
		})
	}
	for _, cause := range []string{"timeout", "cancellation", "changed UID"} {
		t.Run(cause, func(t *testing.T) {
			obj := readyJob(t)
			obj.Status = batchv1.JobStatus{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var methods []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				if req.Method == "GET" {
					if cause == "cancellation" {
						cancel()
					}
					if cause == "changed UID" {
						obj.UID = "replacement"
					}
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := job()
			r.CreateTimeout = new("40ms")
			out, err := r.Create(ctx, c)
			require.Error(t, err)
			require.Nil(t, out)
			require.Equal(t, []string{"POST", "GET"}, methods)
			if cause == "timeout" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
		})
	}
	r := job()
	r.WaitForCompletion = new(false)
	obj := readyJob(t)
	obj.Status = batchv1.JobStatus{}
	var methods []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST"}, methods)
	methods = nil
	_, err = r.Update(t.Context(), c, prior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "PATCH"}, methods)
}

func TestSavedIdentityAndReadErrors(t *testing.T) {
	obj := readyJob(t)
	var token string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		token = req.Header.Get("Authorization")
		require.Equal(t, "/apis/batch/v1/namespaces/default/jobs/example", req.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	r := job()
	out, err := r.output(obj, *c.Server)
	require.NoError(t, err)
	current := *c
	current.Server = new("http://unused.invalid")
	current.Namespace = new("changed")
	current.Token = new("new-credential")
	_, err = r.Read(t.Context(), &current, prior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, "Bearer new-credential", token)
	obj.UID = "other"
	_, err = r.Read(t.Context(), c, prior{Inputs: *r, Outputs: out})
	require.ErrorContains(t, err, "UID changed")
	for _, code := range []int{404, 403, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c := testAPI(t, func(w http.ResponseWriter, _ *http.Request) { apiError(t, w, code) })
			out.Server = *c.Server
			_, err := r.Read(t.Context(), c, prior{Inputs: *r, Outputs: out})
			if code == 404 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.ErrorContains(t, err, "API diagnostic")
			}
		})
	}
}

func TestDeleteBudgetAndIdentity(t *testing.T) {
	for _, cause := range []string{"pending then missing", "initial missing", "replacement UID",
		"finalizer stall", "DELETE403", "DELETE409", "DELETE500", "GET403", "GET500", "cancellation"} {
		t.Run(cause, func(t *testing.T) {
			obj := readyJob(t)
			obj.Finalizers = []string{"example.com/hold"}
			gets := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "DELETE" {
					var opts metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&opts))
					require.Equal(t, metav1.DeletePropagationForeground, *opts.PropagationPolicy)
					require.Equal(t, types.UID("uid"), *opts.Preconditions.UID)
					switch cause {
					case "initial missing":
						apiError(t, w, 404)
						return
					case "DELETE403":
						apiError(t, w, 403)
						return
					case "DELETE409":
						apiError(t, w, 409)
						return
					case "DELETE500":
						apiError(t, w, 500)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
					return
				}
				require.Equal(t, "GET", req.Method)
				gets++
				switch cause {
				case "pending then missing":
					if gets == 2 {
						apiError(t, w, 404)
						return
					}
				case "replacement UID":
					obj.UID = "other"
				case "GET403":
					apiError(t, w, 403)
					return
				case "GET500":
					apiError(t, w, 500)
					return
				case "cancellation":
					cancel()
				}
				require.Equal(t, []string{"example.com/hold"}, obj.Finalizers)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := job()
			r.DeleteTimeout = new("1s")
			if cause == "finalizer stall" {
				r.DeleteTimeout = new("30ms")
			}
			out, err := r.output(obj, *c.Server)
			require.NoError(t, err)
			err = r.Delete(ctx, c, prior{Inputs: *r, Outputs: out})
			if cause == "pending then missing" || cause == "initial missing" || cause == "replacement UID" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestOperationCancellationAndControls(t *testing.T) {
	for _, value := range []string{"", "0s", "-1s", "broken"} {
		for _, op := range []string{"create", "update", "delete"} {
			t.Run(op+value, func(t *testing.T) {
				r := job()
				switch op {
				case "create":
					r.CreateTimeout = &value
				case "update":
					r.UpdateTimeout = &value
				case "delete":
					r.DeleteTimeout = &value
				}
				require.NotEmpty(t, checkNativeJobInputs(t, r))
			})
		}
	}
	for _, op := range []string{"create", "update", "delete"} {
		duration, err := duration(nil, op)
		require.NoError(t, err)
		require.Equal(t, time.Minute, duration)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := testAPI(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("canceled operation reached API")
	})
	_, err := job().Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
}

func TestMissingReadIdentity(t *testing.T) {
	c := testAPI(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("missing read identity reached API")
	})
	_, err := job().Read(t.Context(), c, prior{})
	require.ErrorContains(t, err, "missing Job identity")
}
