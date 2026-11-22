package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func cronJob() *CronJobResource {
	return &CronJobResource{
		Metadata: CronJobMetadata{Name: new("example")},
		Spec: CronJobSpec{Schedule: "* * * * *", JobTemplate: CronJobTemplate{
			Spec: job().Spec,
		}},
	}
}

func TestCronJobPreparation(t *testing.T) {
	r := cronJob()
	obj, err := r.prepare(nil)
	require.NoError(t, err)
	require.Equal(t, "default", obj.Namespace)
	require.Equal(t, "* * * * *", obj.Spec.Schedule)
	require.EqualValues(t, "Allow", obj.Spec.ConcurrencyPolicy)
	require.Equal(t, new(false), obj.Spec.Suspend)
	require.Nil(t, obj.Spec.FailedJobsHistoryLimit)
	require.Nil(t, obj.Spec.SuccessfulJobsHistoryLimit)
	require.Nil(t, obj.Spec.StartingDeadlineSeconds)
	require.Nil(t, obj.Spec.TimeZone)
	require.Empty(t, obj.Spec.JobTemplate.Namespace)
	require.Equal(t, new(int32(6)), obj.Spec.JobTemplate.Spec.BackoffLimit)
	require.Equal(t, corev1.RestartPolicyNever, obj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy)
}

func TestCronJobScheduleParser(t *testing.T) {
	for _, schedule := range []string{
		"* * * * *", "  0 0 * * MON  ", "0 0 ? JAN MON", "*/5 1-3 * * mon-fri",
		"@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly",
		"@every 1h30m", "@every 0s", "@every -2h", "@every +1s",
	} {
		t.Run(schedule, func(t *testing.T) {
			r := cronJob()
			r.Spec.Schedule = schedule
			obj, err := r.prepare(nil)
			require.NoError(t, err)
			require.Equal(t, schedule, obj.Spec.Schedule)
		})
	}
	for _, schedule := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "0 24 * * *", "0 0 32 * *",
		"0 0 * 13 *", "*/0 * * * *", "@unknown", "@every bad", "@every 99999999999999h",
	} {
		t.Run("invalid "+schedule, func(t *testing.T) {
			r := cronJob()
			r.Spec.Schedule = schedule
			_, err := r.prepare(nil)
			require.ErrorContains(t, err, "schedule")
		})
	}
}

func TestCronJobScalarOmissions(t *testing.T) {
	for _, v := range []int64{-4294967295, -1, 0, 1, 3, 4294967296} {
		r := cronJob()
		r.Spec.FailedJobsHistoryLimit, r.Spec.SuccessfulJobsHistoryLimit = &v, &v
		r.Spec.StartingDeadlineSeconds = &v
		r.Spec.Timezone = new("")
		obj, err := r.prepare(nil)
		require.NoError(t, err)
		if v == 1 {
			require.Nil(t, obj.Spec.FailedJobsHistoryLimit)
		} else {
			require.Equal(t, new(int32(v)), obj.Spec.FailedJobsHistoryLimit)
		}
		if v == 3 {
			require.Nil(t, obj.Spec.SuccessfulJobsHistoryLimit)
		} else {
			require.Equal(t, new(int32(v)), obj.Spec.SuccessfulJobsHistoryLimit)
		}
		if v <= 0 {
			require.Nil(t, obj.Spec.StartingDeadlineSeconds)
		} else {
			require.Equal(t, &v, obj.Spec.StartingDeadlineSeconds)
		}
		require.Nil(t, obj.Spec.TimeZone)
	}
	for _, zone := range []string{"UTC", "Mars/Unknown"} {
		r := cronJob()
		r.Spec.Timezone = &zone
		obj, err := r.prepare(nil)
		require.NoError(t, err)
		require.Equal(t, &zone, obj.Spec.TimeZone)
	}
}
