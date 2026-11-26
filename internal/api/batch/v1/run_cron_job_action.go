package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	typedbatchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// RunCronJobAction runs an existing CronJob template under an explicit invocation name.
// An unchanged action skips. Change job-name to request another Job, or @trigger
// to observe the same Job again. Removal never deletes the Job. CronJob ownership,
// history limits and inherited TTL can later remove it; disappearance cannot prove completion.
// Manual creation does not enforce the CronJob controller's concurrency policy.
type RunCronJobAction struct {
	CronJobName        string
	JobName            string
	Namespace          *string
	ExpectedCronJobUID *string
	// WaitForCompletion defaults true. False returns a verified Job without checking completion.
	WaitForCompletion *bool
	// Timeout defaults to 10m for source lookup, creation and completion together.
	Timeout *string
}

type RunCronJobActionOutput struct {
	Server      string
	Namespace   string
	CronJobName string
	CronJobUID  string
	JobName     string
	JobUID      string
	Completed   bool
}

// This action replaces the template's value for the reserved invocation annotation.
// The fingerprint verifies retries; it cannot guarantee exactly-once remote effects.
const runCronJobFingerprintAnnotation = "unobin.cloudboss.io/cron-job-template-sha256"

func (a RunCronJobAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(a.CronJobName,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(a.CronJobName, 253),
		),
		constraint.Must(
			constraint.IsMatch(a.JobName,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(a.JobName, 63),
		),
		constraint.Must(
			constraint.IsMatch(a.Namespace, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
			constraint.MaxItems(a.Namespace, 63),
		),
		constraint.When(constraint.Present(a.ExpectedCronJobUID)).
			Require(constraint.NotEmpty(a.ExpectedCronJobUID)),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func (a RunCronJobAction) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(a.WaitForCompletion, true),
		defaults.NullableValue(a.Timeout, "10m"),
	}
}

func (a *RunCronJobAction) Run(
	ctx context.Context, cfg *config.Configuration,
) (*RunCronJobActionOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget, err := time.ParseDuration(textDefault(a.Timeout, "10m"))
	if err != nil {
		return nil, fmt.Errorf("parse timeout: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	namespace := ""
	if a.Namespace != nil {
		namespace = *a.Namespace
	} else {
		namespace, err = client.Namespace(cfg)
		if err != nil {
			return nil, err
		}
	}
	rest, err := client.Load(cfg)
	if err != nil {
		return nil, err
	}
	api, err := typedbatchv1.NewForConfig(rest)
	if err != nil {
		return nil, fmt.Errorf("create batch client: %w", err)
	}
	source, err := api.CronJobs(namespace).Get(ctx, a.CronJobName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get CronJob %s/%s: %w", namespace, a.CronJobName, err)
	}
	if source.UID == "" || source.DeletionTimestamp != nil {
		return nil, fmt.Errorf("CronJob %s/%s has no UID or is deleting", namespace, a.CronJobName)
	}
	if a.ExpectedCronJobUID != nil && string(source.UID) != *a.ExpectedCronJobUID {
		return nil, fmt.Errorf("CronJob %s/%s UID differs from expected UID", namespace, a.CronJobName)
	}
	desired, err := a.runCronJobRequest(source, namespace)
	if err != nil {
		return nil, err
	}
	jobs := api.Jobs(namespace)
	job, err := jobs.Get(ctx, a.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		job, err = jobs.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			job, err = jobs.Get(ctx, a.JobName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Job %s/%s: %w", namespace, a.JobName, err)
	}
	if err := verifyCronJobInvocation(job, desired); err != nil {
		return nil, err
	}
	completed := a.WaitForCompletion == nil || *a.WaitForCompletion
	if completed {
		if err := waitCronJobInvocation(ctx, jobs, job); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &RunCronJobActionOutput{
		Server: rest.Host, Namespace: namespace, CronJobName: a.CronJobName,
		CronJobUID: string(source.UID), JobName: job.Name, JobUID: string(job.UID), Completed: completed,
	}, nil
}

func (a *RunCronJobAction) runCronJobRequest(
	source *batchv1.CronJob, namespace string,
) (*batchv1.Job, error) {
	encoded, err := json.Marshal(map[string]any{
		"uid": string(source.UID), "namespace": namespace, "template": source.Spec.JobTemplate,
	})
	if err != nil {
		return nil, fmt.Errorf("encode CronJob template: %w", err)
	}
	digest := sha256.Sum256(encoded)
	template := source.Spec.JobTemplate.DeepCopy()
	if template.Annotations == nil {
		template.Annotations = map[string]string{}
	}
	template.Annotations[runCronJobFingerprintAnnotation] = hex.EncodeToString(digest[:])
	template.Annotations["cronjob.kubernetes.io/instantiate"] = "manual"
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: a.JobName, Namespace: namespace,
			Labels: template.Labels, Annotations: template.Annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "CronJob", Name: a.CronJobName,
				UID: source.UID, Controller: new(true),
			}},
		},
		Spec: template.Spec,
	}, nil
}

func verifyCronJobInvocation(job, desired *batchv1.Job) error {
	if job.UID == "" || job.DeletionTimestamp != nil ||
		job.Name != desired.Name || job.Namespace != desired.Namespace ||
		job.Annotations[runCronJobFingerprintAnnotation] !=
			desired.Annotations[runCronJobFingerprintAnnotation] {
		return fmt.Errorf("job %s/%s does not match this invocation", desired.Namespace, desired.Name)
	}
	owner := metav1.GetControllerOf(job)
	expected := desired.OwnerReferences[0]
	if owner == nil || owner.APIVersion != expected.APIVersion || owner.Kind != expected.Kind ||
		owner.Name != expected.Name || owner.UID != expected.UID {
		return fmt.Errorf("job %s/%s has a different CronJob owner", desired.Namespace, desired.Name)
	}
	return nil
}

func waitCronJobInvocation(
	ctx context.Context, jobs typedbatchv1.JobInterface, initial *batchv1.Job,
) error {
	current := initial
	first := true
	return wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			if !first {
				var err error
				current, err = jobs.Get(ctx, initial.Name, metav1.GetOptions{})
				if err != nil {
					return false, fmt.Errorf("wait Job %s/%s: %w", initial.Namespace, initial.Name, err)
				}
			}
			first = false
			if current.UID != initial.UID || current.DeletionTimestamp != nil {
				return false, fmt.Errorf("job %s/%s was replaced or is deleting",
					initial.Namespace, initial.Name)
			}
			complete, failed := false, false
			var reason, message string
			for _, condition := range current.Status.Conditions {
				if condition.Status != corev1.ConditionTrue {
					continue
				}
				complete = complete || condition.Type == batchv1.JobComplete
				if condition.Type == batchv1.JobFailed {
					failed = true
					reason, message = condition.Reason, condition.Message
				}
			}
			if failed {
				return false, fmt.Errorf("job %s/%s failed (complete=%t): %.128s: %.512s",
					initial.Namespace, initial.Name, complete, reason, message)
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			return complete, nil
		})
}
