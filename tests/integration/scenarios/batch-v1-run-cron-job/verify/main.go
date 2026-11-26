package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/batch/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const sourceName = "unobin-it-run-cron-job"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := verify(ctx); err != nil {
		log.Fatal(err)
	}
}

func verify(ctx context.Context) error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" || (phase != "applied" && phase != "updated" && phase != "destroyed") {
		return fmt.Errorf("VERIFY_PHASE and VERIFY_BUILD_DIR are required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		_, err := api.BatchV1().CronJobs("default").Get(ctx, sourceName, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("source CronJob after destroy: %v", err)
		}
		// Native API servers may have no garbage collector. The verifier cleans its fixtures.
		for _, suffix := range []string{"first", "second", "failed"} {
			err := api.BatchV1().Jobs("default").Delete(ctx, sourceName+"-"+suffix,
				metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		return nil
	}
	config, suffix := "config.ub", "first"
	if phase == "updated" {
		config, suffix = "update/config.ub", "second"
	}
	output, err := invocationOutput(ctx, dir, config)
	if err != nil {
		return err
	}
	source, err := api.BatchV1().CronJobs("default").Get(ctx, sourceName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	job, err := api.BatchV1().Jobs("default").Get(ctx, sourceName+"-"+suffix, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if source.Spec.Suspend == nil || !*source.Spec.Suspend ||
		source.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent ||
		job.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" ||
		job.Annotations["purpose"] != "manual" || job.Labels["unobin-test"] != "run-cron-job" {
		return fmt.Errorf("manual invocation changed source controls or lost template metadata")
	}
	owner := metav1.GetControllerOf(job)
	if owner == nil || owner.UID != source.UID || owner.Kind != "CronJob" ||
		owner.Name != source.Name || owner.APIVersion != "batch/v1" ||
		owner.BlockOwnerDeletion != nil {
		return fmt.Errorf("unexpected invocation owner: %+v", owner)
	}
	rest, err := client.Load(nil)
	if err != nil {
		return err
	}
	apiOnly := os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1"
	want := map[string]any{"server": rest.Host, "namespace": "default", "cron-job-name": sourceName,
		"cron-job-uid": string(source.UID), "job-name": job.Name, "job-uid": string(job.UID),
		"completed": !apiOnly}
	if !reflect.DeepEqual(want, output) {
		return fmt.Errorf("factory invocation output = %#v, want %#v", output, want)
	}
	uidPath := filepath.Join(dir, "first-job-uid")
	if phase == "applied" {
		if err := os.WriteFile(uidPath, []byte(job.UID), 0o600); err != nil {
			return err
		}
	} else {
		uid, err := os.ReadFile(uidPath)
		if err != nil {
			return err
		}
		first, err := api.BatchV1().Jobs("default").Get(ctx, sourceName+"-first", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(first.UID) != string(uid) || first.UID == job.UID {
			return fmt.Errorf("second explicit invocation did not preserve two distinct Job UIDs")
		}
	}
	action := &svc.RunCronJobAction{CronJobName: sourceName, JobName: job.Name,
		Namespace: new("default"), ExpectedCronJobUID: new(string(source.UID)),
		WaitForCompletion: new(false), Timeout: new("30s")}
	reused, err := action.Run(ctx, nil)
	if err != nil {
		return err
	}
	if reused.JobUID != string(job.UID) || reused.Completed {
		return fmt.Errorf("no-wait reuse changed identity or claimed completion")
	}
	if apiOnly {
		if err := checkProtocolCompletion(ctx, api, action, job); err != nil {
			return err
		}
		fmt.Println("ok: compiled create/new name and no-wait reuse; manual status protocol checks")
	} else {
		if !complete(job) {
			return fmt.Errorf("kind Job has no Complete=True condition")
		}
		fmt.Println("ok: compiled invocation completed through the real Job controller")
	}
	return nil
}

func invocationOutput(ctx context.Context, dir, config string) (map[string]any, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "batch-v1-run-cron-job"),
		"output", "--format", "json", "-c", config)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("read invocation output: %w\n%s", err, data)
	}
	var result struct {
		Outputs struct{ Invocation map[string]any }
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result.Outputs.Invocation, nil
}

func complete(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func checkProtocolCompletion(
	ctx context.Context, api *kubernetes.Clientset, action *svc.RunCronJobAction, job *batchv1.Job,
) error {
	now := metav1.Now()
	job.Status.StartTime, job.Status.CompletionTime = &now, &now
	job.Status.Succeeded = 1
	job.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue,
			Reason: "ProtocolSuccess", LastTransitionTime: now, LastProbeTime: now},
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
			Reason: "ProtocolSuccess", LastTransitionTime: now, LastProbeTime: now},
	}
	if _, err := api.BatchV1().Jobs("default").UpdateStatus(ctx, job,
		metav1.UpdateOptions{}); err != nil {
		return err
	}
	action.WaitForCompletion = nil
	out, err := action.Run(ctx, nil)
	if err != nil {
		return err
	}
	if !out.Completed || out.JobUID != string(job.UID) {
		return fmt.Errorf("manual terminal protocol failed")
	}
	action.JobName = sourceName + "-failed"
	action.WaitForCompletion = new(false)
	failed, err := action.Run(ctx, nil)
	if err != nil {
		return err
	}
	failedJob, err := api.BatchV1().Jobs("default").Get(ctx, failed.JobName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if failedJob.Status.StartTime == nil {
		failedJob.Status.StartTime = new(metav1.Now())
	}
	failedJob.Status.Failed = 1
	failedJob.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue,
			Reason: "ProtocolFailure", LastTransitionTime: now, LastProbeTime: now},
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
			Reason: "ProtocolFailure", LastTransitionTime: now, LastProbeTime: now},
	}
	if _, err := api.BatchV1().Jobs("default").UpdateStatus(ctx, failedJob,
		metav1.UpdateOptions{}); err != nil {
		return err
	}
	action.WaitForCompletion = nil
	if _, err := action.Run(ctx, nil); err == nil {
		return fmt.Errorf("Failed=True invocation unexpectedly succeeded")
	}
	retained, err := api.BatchV1().Jobs("default").Get(ctx, failed.JobName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if retained.UID != failedJob.UID {
		return errors.New("failed Job was replaced or deleted")
	}
	return nil
}
