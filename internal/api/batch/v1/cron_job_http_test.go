package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func cronJobTestRegistration(t *testing.T) runtime.ResourceRegistration {
	t.Helper()
	base := runtime.MakeResource[
		CronJobResource, *CronJobResourceOutput, *config.Configuration,
	]((&CronJobResource{}).ResourceDefinition())
	return MakeCronJobRegistration[CronJobResource, *CronJobResourceOutput](
		base, base.Read, base.KnownReplacementReasons)
}

func cronJobFullInput(t *testing.T) *CronJobResource {
	t.Helper()
	r := cronJob()
	full := fullInput(t)
	r.Spec.JobTemplate.Spec = full.Spec
	r.Metadata = CronJobMetadata(full.Metadata)
	return r
}
func cronJobAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
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
				{Name: "cronjobs", Kind: "CronJob", Namespaced: true},
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

func TestCronJobFullPUT(t *testing.T) {
	var stored *batchv1.CronJob
	var calls []string
	c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		switch req.Method {
		case "POST":
			stored = &batchv1.CronJob{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			stored.UID, stored.ResourceVersion = "uid", "rv-current"
		case "PUT":
			var desired batchv1.CronJob
			require.NoError(t, json.NewDecoder(req.Body).Decode(&desired))
			require.Equal(t, stored.UID, desired.UID)
			require.Equal(t, "rv-current", desired.ResourceVersion)
			require.Empty(t, desired.Finalizers)
			require.Empty(t, desired.OwnerReferences)
			stored = &desired
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	r := cronJob()
	r.Spec.JobTemplate.Spec.CompletionMode = new("Indexed")
	r.Spec.JobTemplate.Spec.MaxFailedIndexes = new(int64(3))
	r.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = new("60")
	r.Spec.Timezone, r.Spec.StartingDeadlineSeconds = new("UTC"), new(int64(30))
	r.Metadata.Labels = new(map[string]string{"tracked": "initial"})
	r.Metadata.Annotations = new(map[string]string{"tracked": "initial"})
	r.Spec.JobTemplate.Metadata.Labels = new(map[string]string{"nested": "initial"})
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	base := "/apis/batch/v1/namespaces/default/cronjobs"
	require.Equal(t, []string{"POST " + base, "GET " + base + "/example"}, calls)
	old := *r
	stored.Labels["external"] = "remove"
	stored.Annotations["external"] = "remove"
	stored.Finalizers = []string{"external"}
	stored.OwnerReferences = []metav1.OwnerReference{{Name: "external"}}
	r.Metadata.Labels, r.Metadata.Annotations = nil, nil
	r.Spec.JobTemplate.Metadata.Labels = nil
	r.Spec.Timezone, r.Spec.StartingDeadlineSeconds = nil, nil
	r.Spec.Suspend = new(true)
	r.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = new("90")
	r.Spec.JobTemplate.Spec.MaxFailedIndexes = new(int64(1))
	calls = nil
	updated, err := r.Update(t.Context(), c, cronJobPrior{Inputs: old, Outputs: created})
	require.NoError(t, err)
	require.Equal(t, []string{"GET " + base + "/example", "PUT " + base + "/example",
		"GET " + base + "/example"}, calls)
	require.Equal(t, created.UID, updated.UID)
	require.Nil(t, updated.Metadata.Labels)
	require.Nil(t, updated.Metadata.Annotations)
	require.Nil(t, updated.Spec.JobTemplate.Metadata.Labels)
	require.Nil(t, stored.Spec.TimeZone)
	require.Nil(t, stored.Spec.StartingDeadlineSeconds)
	require.Equal(t, new(true), stored.Spec.Suspend)
	require.Equal(t, new(int32(90)), stored.Spec.JobTemplate.Spec.TTLSecondsAfterFinished)
	require.Equal(t, new(int32(1)), stored.Spec.JobTemplate.Spec.MaxFailedIndexes)
	old = *r
	r.Spec.JobTemplate.Spec.TTLSecondsAfterFinished = nil
	r.Spec.JobTemplate.Spec.MaxFailedIndexes = nil
	_, err = r.Update(t.Context(), c, cronJobPrior{Inputs: old, Outputs: updated})
	require.NoError(t, err)
	require.Nil(t, stored.Spec.JobTemplate.Spec.TTLSecondsAfterFinished)
	require.Equal(t, new(int32(0)), stored.Spec.JobTemplate.Spec.MaxFailedIndexes)
}

func readyCronJob(t *testing.T) *batchv1.CronJob {
	t.Helper()
	obj, err := cronJob().prepare(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	return obj
}

func TestCronJobAPIErrorsAndIdentity(t *testing.T) {
	for _, op := range []string{"create", "read", "update", "delete"} {
		for _, code := range []int{403, 404, 409, 500} {
			t.Run(fmt.Sprintf("%s/%d", op, code), func(t *testing.T) {
				var calls []string
				c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method)
					apiError(t, w, code)
				})
				r := cronJob()
				out, err := r.output(readyCronJob(t), *c.Server, r.Metadata)
				require.NoError(t, err)
				p := cronJobPrior{Inputs: *r, Outputs: out}
				switch op {
				case "create":
					_, err = r.Create(t.Context(), c)
				case "read":
					_, err = r.Read(t.Context(), c, p)
				case "update":
					_, err = r.Update(t.Context(), c, p)
				case "delete":
					err = r.Delete(t.Context(), c, p)
				}
				if op == "delete" && code == 404 {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				if op == "read" && code == 404 {
					require.ErrorIs(t, err, runtime.ErrNotFound)
				}
				method := "GET"
				if op == "create" {
					method = "POST"
				}
				if op == "delete" {
					method = "DELETE"
				}
				require.Equal(t, []string{method}, calls)
			})
		}
	}
	for _, code := range []int{403, 409, 500} {
		t.Run(fmt.Sprint("PUT/", code), func(t *testing.T) {
			var calls []string
			c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "PUT" {
					apiError(t, w, code)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(readyCronJob(t)))
			})
			r := cronJob()
			out, err := r.output(readyCronJob(t), *c.Server, r.Metadata)
			require.NoError(t, err)
			_, err = r.Update(t.Context(), c, cronJobPrior{Inputs: *r, Outputs: out})
			require.Error(t, err)
			require.Equal(t, []string{"GET", "PUT"}, calls)
		})
	}
}

func TestCronJobSavedTargetAndUID(t *testing.T) {
	for _, changed := range []bool{false, true} {
		r := cronJob()
		obj := readyCronJob(t)
		c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
			require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
			require.Equal(t, "/apis/batch/v1/namespaces/default/cronjobs/example", req.URL.Path)
			if changed {
				obj.UID = "other"
			}
			require.NoError(t, json.NewEncoder(w).Encode(obj))
		})
		saved, err := r.output(obj, *c.Server, r.Metadata)
		require.NoError(t, err)
		current := *c
		current.Server = new("https://unused.invalid")
		current.Token = new("current")
		actual, err := r.Read(t.Context(), &current, cronJobPrior{Inputs: *r, Outputs: saved})
		if changed {
			require.ErrorContains(t, err, "UID changed")
		} else {
			require.NoError(t, err)
			require.Equal(t, saved, actual)
		}
	}
}

func TestCronJobDeleteOptionsAndPolling(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		var calls []string
		gets := 0
		r := cronJob()
		c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method)
			if req.Method == "DELETE" {
				var options metav1.DeleteOptions
				require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
				require.Nil(t, options.PropagationPolicy)
				require.Nil(t, options.GracePeriodSeconds)
				require.Equal(t, new(types.UID("uid")), options.Preconditions.UID)
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
				return
			}
			gets++
			if gets == 1 || replaced {
				obj := readyCronJob(t)
				if replaced {
					obj.UID = "replacement"
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
				return
			}
			apiError(t, w, 404)
		})
		out, err := r.output(readyCronJob(t), *c.Server, r.Metadata)
		require.NoError(t, err)
		require.NoError(t, r.Delete(t.Context(), c, cronJobPrior{Inputs: *r, Outputs: out}))
		want := []string{"DELETE", "GET", "GET"}
		if replaced {
			want = []string{"DELETE", "GET"}
		}
		require.Equal(t, want, calls)
	}
}

func TestCronJobRefreshFailureDoesNotRollback(t *testing.T) {
	var calls []string
	c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == "POST" {
			require.NoError(t, json.NewEncoder(w).Encode(readyCronJob(t)))
			return
		}
		apiError(t, w, 500)
	})
	_, err := cronJob().Create(t.Context(), c)
	require.ErrorContains(t, err, "API diagnostic")
	require.Equal(t, []string{"POST", "GET"}, calls)
}

func TestCronJobDeleteUsesPriorTimeoutAndPreservesFinalizers(t *testing.T) {
	var calls []string
	obj := readyCronJob(t)
	obj.Finalizers = []string{"do-not-remove"}
	c := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == "DELETE" {
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	input := cronJob()
	input.DeleteTimeout = new("25ms")
	out, err := input.output(obj, *c.Server, input.Metadata)
	require.NoError(t, err)
	desired := *input
	desired.DeleteTimeout = new("10m")
	started := time.Now()
	err = desired.Delete(t.Context(), c, cronJobPrior{Inputs: *input, Outputs: out})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
	require.Equal(t, []string{"DELETE", "GET"}, calls)
	require.Equal(t, []string{"do-not-remove"}, obj.Finalizers)
}
