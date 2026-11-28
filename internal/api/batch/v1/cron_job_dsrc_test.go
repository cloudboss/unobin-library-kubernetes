package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestCronJobLookupRead(t *testing.T) {
	for _, namespace := range []*string{nil, new("explicit")} {
		t.Run(textDefault(namespace, "default"), func(t *testing.T) {
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
				require.NoError(t, json.NewEncoder(w).Encode(&batchv1.CronJob{
					ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "returned", UID: "uid"},
				}))
			})
			out, err := (&CronJobDataSource{Metadata: CronJobLookupMetadata{
				Name: "example", Namespace: namespace,
			}}).
				Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []string{"GET /apis/batch/v1/namespaces/" +
				textDefault(namespace, "default") + "/cronjobs/example "}, calls)
			require.Equal(t, CronJobLookupMetadataOutput{Name: "example", Namespace: "returned", UID: "uid",
				Labels: map[string]string{}, Annotations: map[string]string{}}, out.Metadata)
			require.NotNil(t, out.Spec)
			require.Equal(t, &CronJobLookupStatus{Active: []CronJobLookupReference{}}, out.Status)
		})
	}
}

func TestCronJobLookupMissingAndErrors(t *testing.T) {
	for _, code := range []int{404, 403, 500, 200} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			calls := []string{}
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				if code == 200 {
					_, err := w.Write([]byte("invalid JSON"))
					require.NoError(t, err)
					return
				}
				apiError(t, w, code)
			})
			out, err := (&CronJobDataSource{Metadata: CronJobLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Equal(t, []string{"GET /apis/batch/v1/namespaces/default/cronjobs/example"}, calls)
			if code == 404 {
				require.NoError(t, err)
				require.Equal(t, &CronJobDataSourceOutput{Metadata: CronJobLookupMetadataOutput{
					Name: "example", Namespace: "default", Labels: map[string]string{},
					Annotations: map[string]string{},
				}}, out)
			} else {
				require.Nil(t, out)
				require.ErrorContains(t, err, "get CronJob default/example")
				if code == 403 {
					require.True(t, apierrors.IsForbidden(err))
				}
				if code == 500 {
					require.Equal(t, metav1.StatusReasonForbidden, apierrors.ReasonForError(err))
				}
			}
		})
	}
	_, err := (&CronJobDataSource{Metadata: CronJobLookupMetadata{Name: "example"}}).Read(t.Context(),
		&config.Configuration{CertificateAuthorityData: new("!")})
	require.ErrorContains(t, err, "get CronJob default/example")
}

func TestCronJobLookupCancellation(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before", false: "in flight"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := testAPI(t, func(_ http.ResponseWriter, r *http.Request) {
				if before {
					t.Error("pre-canceled lookup reached API")
					return
				}
				cancel()
				<-r.Context().Done()
			})
			if before {
				cancel()
			}
			_, err := (&CronJobDataSource{Metadata: CronJobLookupMetadata{Name: "example"}}).Read(ctx, c)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestCronJobLookupLiveProjection(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600)))
	zero := int32(0)
	no := false
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", GenerateName: "prefix-",
			ResourceVersion: "42", Generation: 7, DeletionTimestamp: &now,
			Labels:      map[string]string{"batch.kubernetes.io/controller-uid": "generated"},
			Annotations: map[string]string{"internal.kubernetes.io/value": "retained"}},
		Spec: batchv1.JobSpec{
			ActiveDeadlineSeconds: new(int64(0)), BackoffLimit: &zero, BackoffLimitPerIndex: &zero,
			Completions: &zero, MaxFailedIndexes: &zero, Parallelism: &zero,
			CompletionMode:       new(batchv1.CompletionMode("unknown")),
			PodReplacementPolicy: new(batchv1.PodReplacementPolicy("unknown")), ManagedBy: new("external"),
			ManualSelector: &no, Suspend: &no, TTLSecondsAfterFinished: &zero,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"controller-uid": "generated"},
				MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k", Operator: "unknown",
					Values: []string{"z", "a", "z"}}}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"job-name": "generated"}},
				Spec: corev1.PodSpec{RuntimeClassName: new("runtime"),
					Containers: []corev1.Container{{Name: "app", VolumeMounts: []corev1.VolumeMount{
						{Name: "kube-api-access-abc", MountPath: "/token"}},
						Resources: corev1.ResourceRequirements{}}},
					Volumes: []corev1.Volume{{Name: "kube-api-access-abc", VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
							{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{
								Name: "config"}, Optional: new(true)}},
							{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
						}},
					}}},
				},
			},
			PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
				Action: "unknown", OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
					ContainerName: new(""), Operator: "unknown", Values: []int32{7, 1, 7}},
				OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
					{Type: "Unknown", Status: "Unknown"}, {Type: "Failed", Status: "True"}},
			}}},
			SuccessPolicy: &batchv1.SuccessPolicy{Rules: []batchv1.SuccessPolicyRule{
				{SucceededIndexes: new("2,0-1"), SucceededCount: &zero}, {},
			}},
		},
	}
	native := &batchv1.CronJob{
		ObjectMeta: job.ObjectMeta,
		Spec: batchv1.CronJobSpec{
			Schedule: "returned schedule text", TimeZone: new(""), ConcurrencyPolicy: "unknown",
			StartingDeadlineSeconds: new(int64(0)), Suspend: new(false),
			SuccessfulJobsHistoryLimit: new(int32(0)), FailedJobsHistoryLimit: new(int32(0)),
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Name: "template", GenerateName: "prefix-", Namespace: "returned",
					Labels:      map[string]string{"job-name": "generated"},
					Annotations: map[string]string{"internal.kubernetes.io/value": "retained"}},
				Spec: job.Spec,
			},
		},
		Status: batchv1.CronJobStatus{
			Active: []corev1.ObjectReference{
				{APIVersion: "unknown/v1", Kind: "Unknown", Namespace: "other", Name: "second", UID: "uid-2",
					ResourceVersion: "42", FieldPath: "spec.second"},
				{APIVersion: "batch/v1", Kind: "Job", Namespace: "default", Name: "first", UID: "uid-1",
					ResourceVersion: "43", FieldPath: "spec.first"},
				{},
			},
			LastScheduleTime: &now, LastSuccessfulTime: &metav1.Time{},
		},
	}
	expectedJob := &JobDataSourceOutput{
		Metadata: JobLookupMetadataOutput{Name: "example", Namespace: "default", GenerateName: "prefix-",
			ResourceVersion: "42", Generation: 7,
			Labels:      map[string]string{"batch.kubernetes.io/controller-uid": "generated"},
			Annotations: map[string]string{"internal.kubernetes.io/value": "retained"}},
		Spec: &JobLookupSpec{
			ActiveDeadlineSeconds: new(int64(0)), BackoffLimit: new(int64(0)),
			BackoffLimitPerIndex: new(int64(0)), Completions: new(int64(0)),
			MaxFailedIndexes: new(int64(0)), Parallelism: new(int64(0)),
			CompletionMode: new("unknown"), PodReplacementPolicy: new("unknown"), ManagedBy: new("external"),
			ManualSelector: new(false), Suspend: new(false), TTLSecondsAfterFinished: new("0"),
			Selector: &Selector{MatchLabels: new(map[string]string{"controller-uid": "generated"}),
				MatchExpressions: new([]SelectorRequirement{{Key: new("k"), Operator: new("unknown"),
					Values: new([]string{"z", "a", "z"})}})},
			Template: PodTemplate{
				Metadata: TemplateMetadata{Name: new(""), GenerateName: new(""),
					Labels: new(map[string]string{"job-name": "generated"})},
				Spec: &PodSpec{DNSPolicy: new(""), HostIPC: new(false), HostNetwork: new(false),
					HostPID:  new(false),
					Hostname: new(""), NodeName: new(""), RuntimeClassName: new("runtime"),
					PriorityClassName: new(""),
					RestartPolicy:     new(""), SchedulerName: new(""), ServiceAccountName: new(""),
					Subdomain: new(""),
					Container: new([]Container{{Name: "app", Image: new(""), ImagePullPolicy: new(""),
						Resources: &Resources{}, Stdin: new(false), StdinOnce: new(false), TTY: new(false),
						TerminationMessagePath: new(""), TerminationMessagePolicy: new(""), WorkingDir: new(""),
						VolumeMount: new([]Mount{{Name: "kube-api-access-abc", MountPath: "/token",
							ReadOnly: new(false),
							SubPath:  new(""), SubPathExpr: new("")}}),
					}}),
					Volume: new([]Volume{{Name: new("kube-api-access-abc"), Projected: &ProjectedVolume{
						Sources: []ProjectionGroup{
							{ConfigMap: new([]ConfigMapProjection{{Name: new("config"), Optional: new(true)}})},
							{ServiceAccountToken: &ServiceAccountTokenProjection{Path: "token", Audience: new("")}},
						},
					}}}),
				},
			},
			PodFailurePolicy: &PodFailurePolicy{Rule: []PodFailureRule{{Action: new("unknown"),
				OnExitCodes: &ExitCodes{ContainerName: new(""), Operator: new("unknown"),
					Values: []int64{7, 1, 7}},
				OnPodCondition: new([]PodCondition{{Type: new("Unknown"), Status: new("Unknown")},
					{Type: new("Failed"), Status: new("True")}}),
			}}},
			SuccessPolicy: &JobLookupSuccessPolicy{Rule: []JobLookupSuccessRule{
				{SucceededIndexes: new("2,0-1"), SucceededCount: new(int64(0))}, {},
			}},
		},
	}
	before := native.DeepCopy()
	out, err := cronJobLookupOutput(native)
	require.NoError(t, err)
	require.Equal(t, before, native)
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		require.NoError(t, json.NewEncoder(w).Encode(native))
	})
	observed, err := (&CronJobDataSource{Metadata: CronJobLookupMetadata{Name: "example"}}).
		Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"GET /apis/batch/v1/namespaces/default/cronjobs/example"}, calls)
	require.Equal(t, out, observed)
	require.Equal(t, &CronJobDataSourceOutput{
		Metadata: CronJobLookupMetadataOutput(expectedJob.Metadata),
		Spec: &CronJobLookupSpec{
			Schedule: "returned schedule text", Timezone: new(""), ConcurrencyPolicy: "unknown",
			StartingDeadlineSeconds: new(int64(0)), Suspend: new(false),
			SuccessfulJobsHistoryLimit: new(int64(0)), FailedJobsHistoryLimit: new(int64(0)),
			JobTemplate: CronJobLookupTemplate{
				Metadata: CronJobLookupTemplateMetadata{Name: "template", GenerateName: "prefix-",
					Namespace: "returned", Labels: map[string]string{"job-name": "generated"},
					Annotations: map[string]string{"internal.kubernetes.io/value": "retained"}},
				Spec: *expectedJob.Spec,
			},
		},
		Status: &CronJobLookupStatus{
			Active: []CronJobLookupReference{
				{APIVersion: "unknown/v1", Kind: "Unknown", Namespace: "other", Name: "second", UID: "uid-2",
					ResourceVersion: "42", FieldPath: "spec.second"},
				{APIVersion: "batch/v1", Kind: "Job", Namespace: "default", Name: "first", UID: "uid-1",
					ResourceVersion: "43", FieldPath: "spec.first"},
				{},
			},
			LastScheduleTime: new("2026-01-02T02:04:05Z"),
		},
	}, out)
	out.Metadata.Labels["new"] = "value"
	out.Spec.JobTemplate.Metadata.Annotations["new"] = "value"
	(*out.Spec.JobTemplate.Spec.Selector.MatchLabels)["new"] = "value"
	(*out.Spec.JobTemplate.Spec.Template.Spec.Container)[0].Name = "changed"
	*out.Spec.Timezone = "changed"
	out.Status.Active[0].Name = "changed"
	require.Equal(t, before, native)
}
func TestCronJobLookupTemplateFormatsAndOrder(t *testing.T) {
	obj := &batchv1.Job{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: new(int64(0)),
				SupplementalGroups: []int64{3, 1, 3}},
			Containers: []corev1.Container{{Name: "app",
				Resources: corev1.ResourceRequirements{
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1.5")},
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				},
				LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt32(8080)},
				}},
			}},
			Volumes: []corev1.Volume{{Name: "secret", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "secret", DefaultMode: new(int32(420)),
					Items: []corev1.KeyToPath{{Key: "k", Path: "p", Mode: new(int32(0))}}},
			}}},
		},
	}}}
	before := obj.DeepCopy()
	out, err := cronJobLookupOutput(&batchv1.CronJob{
		Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: obj.Spec}},
	})
	require.NoError(t, err)
	require.Equal(t, before, obj)
	spec := out.Spec.JobTemplate.Spec.Template.Spec
	require.Equal(t, &PodSecurityContext{RunAsUser: new("0"),
		SupplementalGroups: new([]int64{3, 1, 3})}, spec.SecurityContext)
	require.Equal(t, &Resources{Limits: new(map[string]string{"cpu": "1500m"}),
		Requests: new(map[string]string{"memory": "1Gi"})}, (*spec.Container)[0].Resources)
	require.Equal(t, new("8080"), (*spec.Container)[0].LivenessProbe.HTTPGet.Port)
	require.Equal(t, &SecretVolume{SecretName: new("secret"), DefaultMode: new("0644"),
		Items: new([]KeyPath{{Key: new("k"), Path: new("p"), Mode: new("00")}})},
		(*spec.Volume)[0].Secret)
}

func TestCronJobLookupClientRetries(t *testing.T) {
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if len(calls) == 1 {
			w.Header().Set("Retry-After", "0")
			apiError(t, w, http.StatusTooManyRequests)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(&batchv1.CronJob{}))
	})
	_, err := (&CronJobDataSource{Metadata: CronJobLookupMetadata{Name: "example"}}).
		Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET /apis/batch/v1/namespaces/default/cronjobs/example",
		"GET /apis/batch/v1/namespaces/default/cronjobs/example",
	}, calls)
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	calls = nil
	_, err = (&CronJobDataSource{Metadata: CronJobLookupMetadata{Name: "example"}}).Read(ctx, c)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, calls)
}

func TestCronJobLookupEmptyAndOptionalStatus(t *testing.T) {
	empty, err := cronJobLookupOutput(&batchv1.CronJob{})
	require.NoError(t, err)
	require.Equal(t, &CronJobDataSourceOutput{
		Metadata: CronJobLookupMetadataOutput{
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &CronJobLookupSpec{JobTemplate: CronJobLookupTemplate{
			Metadata: CronJobLookupTemplateMetadata{
				Labels: map[string]string{}, Annotations: map[string]string{},
			},
			Spec: empty.Spec.JobTemplate.Spec,
		}},
		Status: &CronJobLookupStatus{Active: []CronJobLookupReference{}},
	}, empty)
	require.Nil(t, empty.Spec.JobTemplate.Spec.Parallelism)
	require.Nil(t, empty.Spec.JobTemplate.Spec.TTLSecondsAfterFinished)
	require.Nil(t, empty.Spec.JobTemplate.Spec.ManualSelector)
	require.Nil(t, empty.Spec.JobTemplate.Spec.PodFailurePolicy)
	require.Nil(t, empty.Spec.JobTemplate.Spec.SuccessPolicy)
	stamp := metav1.NewTime(time.Date(2026, 2, 3, 4, 5, 6, 0, time.FixedZone("offset", -3600)))
	obj := &batchv1.CronJob{Status: batchv1.CronJobStatus{
		LastScheduleTime: &metav1.Time{}, LastSuccessfulTime: &stamp,
	}}
	out, err := cronJobLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, &CronJobLookupStatus{Active: []CronJobLookupReference{},
		LastSuccessfulTime: new("2026-02-03T05:05:06Z")}, out.Status)
}

func TestCronJobLookupObservesAllStates(t *testing.T) {
	for _, state := range []string{"never scheduled", "active", "suspended", "terminating"} {
		t.Run(state, func(t *testing.T) {
			obj := &batchv1.CronJob{}
			switch state {
			case "active":
				obj.Status.Active = []corev1.ObjectReference{{Name: "child"}}
			case "suspended":
				obj.Spec.Suspend = new(true)
			case "terminating":
				now := metav1.Now()
				obj.DeletionTimestamp = &now
			}
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			out, err := (&CronJobDataSource{Metadata: CronJobLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.NoError(t, err)
			want, err := cronJobLookupOutput(obj)
			require.NoError(t, err)
			require.Equal(t, want, out)
			require.Equal(t, []string{"GET /apis/batch/v1/namespaces/default/cronjobs/example"}, calls)
		})
	}
}
