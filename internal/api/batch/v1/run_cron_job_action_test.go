package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func runCronJobSource() *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "default", UID: "cron-uid"},
		Spec: batchv1.CronJobSpec{Suspend: new(true), JobTemplate: batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{"app": "batch"},
				Annotations: map[string]string{"test": "value"},
			},
			Spec: batchv1.JobSpec{
				ManagedBy: new("example.com/controller"),
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "app", Image: "busybox:1.37"}},
				}},
			},
		}},
	}
}

func TestRunCronJobCreatesAndReusesFullTemplate(t *testing.T) {
	source := runCronJobSource()
	before := source.DeepCopy()
	var job *batchv1.Job
	var calls []string
	cfg := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.URL.Path == "/apis/batch/v1/namespaces/default/cronjobs/source" {
			require.NoError(t, json.NewEncoder(w).Encode(source))
			return
		}
		if req.Method == http.MethodPost {
			job = &batchv1.Job{}
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			_, _, err = scheme.Codecs.UniversalDeserializer().Decode(body, nil, job)
			require.NoError(t, err)
			job.UID = "job-uid"
		}
		if job == nil {
			apiError(t, w, 404)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(job))
	})
	action := &RunCronJobAction{CronJobName: "source", JobName: "run", WaitForCompletion: new(false),
		Namespace: new("default")}
	out, err := action.Run(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, &RunCronJobActionOutput{
		Server: *cfg.Server, Namespace: "default", CronJobName: "source", CronJobUID: "cron-uid",
		JobName: "run", JobUID: "job-uid", Completed: false,
	}, out)
	require.Equal(t, before, source)
	require.Equal(t, source.Spec.JobTemplate.Spec, job.Spec)
	require.Equal(t, source.Spec.JobTemplate.Labels, job.Labels)
	require.Equal(t, "value", job.Annotations["test"])
	require.Equal(t, "manual", job.Annotations["cronjob.kubernetes.io/instantiate"])
	require.Equal(t, []metav1.OwnerReference{{
		APIVersion: "batch/v1", Kind: "CronJob", Name: "source", UID: "cron-uid", Controller: new(true),
	}}, job.OwnerReferences)
	second, err := action.Run(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, out, second)
	require.Equal(t, []string{
		"GET /apis/batch/v1/namespaces/default/cronjobs/source",
		"GET /apis/batch/v1/namespaces/default/jobs/run",
		"POST /apis/batch/v1/namespaces/default/jobs",
		"GET /apis/batch/v1/namespaces/default/cronjobs/source",
		"GET /apis/batch/v1/namespaces/default/jobs/run",
	}, calls)
}

func TestRunCronJobNamespaceAndSourcePreconditions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		namespace  *string
		configured *string
		want       string
		edit       func(*batchv1.CronJob, *RunCronJobAction)
		invalid    bool
	}{
		{name: "explicit", namespace: new("explicit"), configured: new("configured"), want: "explicit"},
		{name: "configured", configured: new("configured"), want: "configured"},
		{name: "default", want: "default"},
		{name: "missing UID", want: "default", invalid: true,
			edit: func(s *batchv1.CronJob, _ *RunCronJobAction) { s.UID = "" }},
		{name: "deleting", want: "default", invalid: true,
			edit: func(s *batchv1.CronJob, _ *RunCronJobAction) {
				s.DeletionTimestamp = new(metav1.Now())
			}},
		{name: "expected UID", want: "default",
			edit: func(_ *batchv1.CronJob, a *RunCronJobAction) {
				a.ExpectedCronJobUID = new("cron-uid")
			}},
		{name: "wrong UID", want: "default", invalid: true,
			edit: func(_ *batchv1.CronJob, a *RunCronJobAction) {
				a.ExpectedCronJobUID = new("other")
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := runCronJobSource()
			action := &RunCronJobAction{CronJobName: "source", JobName: "run",
				Namespace: tc.namespace, WaitForCompletion: new(false)}
			if tc.edit != nil {
				tc.edit(source, action)
			}
			var calls []string
			cfg := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if strings.Contains(req.URL.Path, "/cronjobs/") {
					require.NoError(t, json.NewEncoder(w).Encode(source))
					return
				}
				job, err := action.runCronJobRequest(source, tc.want)
				require.NoError(t, err)
				job.UID = "job-uid"
				require.NoError(t, json.NewEncoder(w).Encode(job))
			})
			cfg.Namespace = tc.configured
			out, err := action.Run(t.Context(), cfg)
			want := []string{"GET /apis/batch/v1/namespaces/" + tc.want + "/cronjobs/source"}
			if tc.invalid {
				require.Error(t, err)
				require.Nil(t, out)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, out.Namespace)
				want = append(want, "GET /apis/batch/v1/namespaces/"+tc.want+"/jobs/run")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestRunCronJobInvocationConflictsAndRace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		race    bool
		edit    func(*batchv1.Job, *batchv1.CronJob)
		invalid bool
	}{
		{name: "existing"},
		{name: "racing verified", race: true},
		{name: "changed native field", invalid: true,
			edit: func(_ *batchv1.Job, s *batchv1.CronJob) {
				s.Spec.JobTemplate.Spec.ManagedBy = new("changed")
			}},
		{name: "changed metadata", invalid: true,
			edit: func(_ *batchv1.Job, s *batchv1.CronJob) { s.Spec.JobTemplate.Labels["app"] = "changed" }},
		{name: "changed source UID", invalid: true,
			edit: func(_ *batchv1.Job, s *batchv1.CronJob) { s.UID = "replacement" }},
		{name: "missing marker", invalid: true,
			edit: func(j *batchv1.Job, _ *batchv1.CronJob) { j.Annotations = nil }},
		{name: "racing unrelated", race: true, invalid: true,
			edit: func(j *batchv1.Job, _ *batchv1.CronJob) { j.OwnerReferences = nil }},
		{name: "wrong owner", invalid: true,
			edit: func(j *batchv1.Job, _ *batchv1.CronJob) { j.OwnerReferences[0].UID = "other" }},
		{name: "not controller", invalid: true,
			edit: func(j *batchv1.Job, _ *batchv1.CronJob) { j.OwnerReferences[0].Controller = new(false) }},
		{name: "missing Job UID", invalid: true,
			edit: func(j *batchv1.Job, _ *batchv1.CronJob) { j.UID = "" }},
		{name: "deleting Job", invalid: true,
			edit: func(j *batchv1.Job, _ *batchv1.CronJob) { j.DeletionTimestamp = new(metav1.Now()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := runCronJobSource()
			action := &RunCronJobAction{CronJobName: "source", JobName: "run",
				Namespace: new("default"), WaitForCompletion: new(false)}
			job, err := action.runCronJobRequest(source, "default")
			require.NoError(t, err)
			job.UID = "job-uid"
			if tc.edit != nil {
				tc.edit(job, source)
			}
			var calls []string
			gets := 0
			cfg := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if strings.Contains(req.URL.Path, "/cronjobs/") {
					require.NoError(t, json.NewEncoder(w).Encode(source))
					return
				}
				if req.Method == http.MethodPost {
					apiError(t, w, 409)
					return
				}
				gets++
				if tc.race && gets == 1 {
					apiError(t, w, 404)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(job))
			})
			_, err = action.Run(t.Context(), cfg)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			want := []string{
				"GET /apis/batch/v1/namespaces/default/cronjobs/source",
				"GET /apis/batch/v1/namespaces/default/jobs/run",
			}
			if tc.race {
				want = append(want, "POST /apis/batch/v1/namespaces/default/jobs",
					"GET /apis/batch/v1/namespaces/default/jobs/run")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestRunCronJobTerminalWait(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*batchv1.Job)
		code      int
		wantError string
		deadline  bool
	}{
		{name: "complete", change: func(j *batchv1.Job) {
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete,
				Status: corev1.ConditionTrue}}
		}},
		{name: "failed", wantError: "failure reason", change: func(j *batchv1.Job) {
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed,
				Status: corev1.ConditionTrue, Reason: "failure reason", Message: strings.Repeat("x", 4096)}}
		}},
		{name: "both", wantError: "complete=true", change: func(j *batchv1.Job) {
			j.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			}
		}},
		{name: "replaced", wantError: "replaced", change: func(j *batchv1.Job) {
			j.UID = "replacement"
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete,
				Status: corev1.ConditionTrue}}
		}},
		{name: "deleting", wantError: "deleting", change: func(j *batchv1.Job) {
			j.DeletionTimestamp = new(metav1.Now())
		}},
		{name: "missing", code: 404, wantError: "API diagnostic"},
		{name: "forbidden", code: 403, wantError: "API diagnostic"},
		{name: "counters", deadline: true, change: func(j *batchv1.Job) {
			j.Status.Succeeded, j.Status.Failed = 1, 1
		}},
		{name: "false complete", deadline: true, change: func(j *batchv1.Job) {
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete,
				Status: corev1.ConditionFalse}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := runCronJobSource()
			action := &RunCronJobAction{CronJobName: "source", JobName: "run",
				Namespace: new("default"), Timeout: new("600ms")}
			job, err := action.runCronJobRequest(source, "default")
			require.NoError(t, err)
			job.UID = "job-uid"
			gets := 0
			var mutations []string
			cfg := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					mutations = append(mutations, req.Method)
				}
				if strings.Contains(req.URL.Path, "/cronjobs/") {
					require.NoError(t, json.NewEncoder(w).Encode(source))
					return
				}
				gets++
				if gets > 1 {
					if tc.code != 0 {
						apiError(t, w, tc.code)
						return
					}
					tc.change(job)
				}
				require.NoError(t, json.NewEncoder(w).Encode(job))
			})
			out, err := action.Run(t.Context(), cfg)
			switch {
			case tc.deadline:
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case tc.wantError != "":
				require.ErrorContains(t, err, tc.wantError)
				require.Less(t, len(err.Error()), 800)
				if tc.code == 404 {
					require.True(t, apierrors.IsNotFound(err))
				}
				if tc.code == 403 {
					require.True(t, apierrors.IsForbidden(err))
				}
			default:
				require.NoError(t, err)
				require.True(t, out.Completed)
				require.Equal(t, "job-uid", out.JobUID)
			}
			require.Empty(t, mutations)
		})
	}
}

func TestRunCronJobCancellationAndDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		timeout *string
		want    error
	}{
		{name: "canceled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx, cancel
		}, want: context.Canceled},
		{name: "parent deadline", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(t.Context(), 30*time.Millisecond)
		}, want: context.DeadlineExceeded},
		{name: "action deadline", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(t.Context())
		}, timeout: new("30ms"), want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			cfg := cronJobAPI(t, func(_ http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				<-req.Context().Done()
			})
			ctx, cancel := tc.ctx()
			defer cancel()
			a := &RunCronJobAction{CronJobName: "source", JobName: "run", Timeout: tc.timeout}
			_, err := a.Run(ctx, cfg)
			require.ErrorIs(t, err, tc.want)
			if tc.want == context.Canceled {
				require.Empty(t, calls)
			} else {
				require.Equal(t, []string{"GET"}, calls)
			}
		})
	}
}

func TestRunCronJobFingerprintAndMetadataPrecedence(t *testing.T) {
	source := runCronJobSource()
	source.Spec.JobTemplate.Annotations[runCronJobFingerprintAnnotation] = "template value"
	source.Spec.JobTemplate.Annotations["cronjob.kubernetes.io/instantiate"] = "template value"
	before := source.DeepCopy()
	action := &RunCronJobAction{CronJobName: "source", JobName: "run"}
	job, err := action.runCronJobRequest(source, "namespace")
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"namespace": "namespace", "template": before.Spec.JobTemplate, "uid": "cron-uid",
	})
	require.NoError(t, err)
	digest := sha256.Sum256(payload)
	require.Equal(t, hex.EncodeToString(digest[:]), job.Annotations[runCronJobFingerprintAnnotation])
	require.Equal(t, "manual", job.Annotations["cronjob.kubernetes.io/instantiate"])
	require.Equal(t, before, source)
	other, err := action.runCronJobRequest(source, "different")
	require.NoError(t, err)
	require.NotEqual(t, job.Annotations[runCronJobFingerprintAnnotation],
		other.Annotations[runCronJobFingerprintAnnotation])
	job.Labels["app"] = "changed"
	job.Spec.Template.Spec.Containers[0].Image = "changed"
	require.Equal(t, before, source)
}

func TestRunCronJobAcceptedCreateTransportErrorReusesOnNextInvocation(t *testing.T) {
	source := runCronJobSource()
	var stored *batchv1.Job
	var calls []string
	cfg := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if strings.Contains(req.URL.Path, "/cronjobs/") {
			require.NoError(t, json.NewEncoder(w).Encode(source))
			return
		}
		if req.Method == http.MethodPost {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			stored = &batchv1.Job{}
			_, _, err = scheme.Codecs.UniversalDeserializer().Decode(body, nil, stored)
			require.NoError(t, err)
			stored.UID = "accepted-uid"
			conn, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, conn.Close())
			return
		}
		if stored == nil {
			apiError(t, w, 404)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	action := &RunCronJobAction{CronJobName: "source", JobName: "run",
		Namespace: new("default"), WaitForCompletion: new(false)}
	out, err := action.Run(t.Context(), cfg)
	require.Error(t, err)
	require.Nil(t, out)
	require.Equal(t, "accepted-uid", string(stored.UID))
	out, err = action.Run(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, "accepted-uid", out.JobUID)
	require.Equal(t, []string{
		"GET /apis/batch/v1/namespaces/default/cronjobs/source",
		"GET /apis/batch/v1/namespaces/default/jobs/run",
		"POST /apis/batch/v1/namespaces/default/jobs",
		"GET /apis/batch/v1/namespaces/default/cronjobs/source",
		"GET /apis/batch/v1/namespaces/default/jobs/run",
	}, calls)
}

func TestRunCronJobCreateFailureAndTimeoutRetainJob(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal=%t", terminal), func(t *testing.T) {
			source := runCronJobSource()
			var stored *batchv1.Job
			var mutations []string
			cfg := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if strings.Contains(req.URL.Path, "/cronjobs/") {
					require.NoError(t, json.NewEncoder(w).Encode(source))
					return
				}
				if req.Method != http.MethodGet {
					mutations = append(mutations, req.Method)
				}
				if req.Method == http.MethodPost {
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					stored = &batchv1.Job{}
					_, _, err = scheme.Codecs.UniversalDeserializer().Decode(body, nil, stored)
					require.NoError(t, err)
					stored.UID = "retained"
					if terminal {
						stored.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed,
							Status: corev1.ConditionTrue, Reason: "failure"}}
					}
				}
				if stored == nil {
					apiError(t, w, 404)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			action := &RunCronJobAction{CronJobName: "source", JobName: "run",
				Namespace: new("default"), Timeout: new("50ms")}
			_, err := action.Run(t.Context(), cfg)
			if terminal {
				require.ErrorContains(t, err, "failure")
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			require.Equal(t, []string{"POST"}, mutations)
			require.Equal(t, "retained", string(stored.UID))
		})
	}
}

func TestRunCronJobSourceErrorAndCanceledSuccess(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			action := &RunCronJobAction{CronJobName: "source", JobName: "run",
				Namespace: new("default")}
			var calls []string
			cfg := cronJobAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if !canceled {
					apiError(t, w, 404)
					return
				}
				source := runCronJobSource()
				if strings.Contains(req.URL.Path, "/cronjobs/") {
					require.NoError(t, json.NewEncoder(w).Encode(source))
					return
				}
				job, err := action.runCronJobRequest(source, "default")
				require.NoError(t, err)
				job.UID = "job-uid"
				job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete,
					Status: corev1.ConditionTrue}}
				cancel()
				require.NoError(t, json.NewEncoder(w).Encode(job))
			})
			_, err := action.Run(ctx, cfg)
			if canceled {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.True(t, apierrors.IsNotFound(err))
				require.Equal(t, []string{
					"GET /apis/batch/v1/namespaces/default/cronjobs/source",
				}, calls)
			}
		})
	}
}
