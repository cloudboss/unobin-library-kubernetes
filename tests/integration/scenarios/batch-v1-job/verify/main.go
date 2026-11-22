package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedbatchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const apiJobName = "unobin-it-job-api"
const completionJobName = "unobin-it-job-completion"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	buildDir := os.Getenv("VERIFY_BUILD_DIR")
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	phase := os.Getenv("VERIFY_PHASE")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	jobs := api.BatchV1().Jobs("default")
	for _, name := range []string{apiJobName, completionJobName} {
		if phase == "destroyed" {
			_, err := jobs.Get(ctx, name, metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("get destroyed Job %s: %v", name, err)
			}
			continue
		}
		parallelism, backoff, stage := int32(1), int32(6), "initial"
		if phase == "updated" {
			parallelism, backoff, stage = 2, 4, "updated"
		}
		requireCompletion := name == completionJobName && os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1"
		obj, err := verifyPresent(ctx, jobs, name, parallelism, backoff, stage, requireCompletion)
		if err != nil {
			return err
		}
		uidPath := filepath.Join(buildDir, name+"-uid")
		if phase == "applied" {
			if err := os.WriteFile(uidPath, []byte(obj.UID), 0o600); err != nil {
				return err
			}
		} else {
			uid, err := os.ReadFile(uidPath)
			if err != nil {
				return err
			}
			if string(uid) != string(obj.UID) {
				return fmt.Errorf("job %s UID changed from %s to %s", name, uid, obj.UID)
			}
		}
	}
	fmt.Printf("ok: both Jobs match %s configuration\n", phase)
	return nil
}

func verifyPresent(
	ctx context.Context, api typedbatchv1.JobInterface, name string,
	parallelism, backoff int32, stage string, requireCompletion bool,
) (*batchv1.Job, error) {
	obj, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if obj.UID == "" || obj.Name != name || obj.Namespace != "default" {
		return nil, fmt.Errorf("invalid Job identity")
	}
	if obj.Labels["app"] != "job-api" || obj.Labels["stage"] != stage ||
		obj.Annotations["stage"] != stage {
		return nil, fmt.Errorf("job metadata does not match %s", stage)
	}
	if obj.Spec.Parallelism == nil || *obj.Spec.Parallelism != parallelism ||
		obj.Spec.BackoffLimit == nil || *obj.Spec.BackoffLimit != backoff {
		return nil, fmt.Errorf("unexpected Job parallelism/backoff")
	}
	if obj.Spec.Completions == nil || *obj.Spec.Completions != 1 {
		return nil, fmt.Errorf("completions default is not 1")
	}
	if obj.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return nil, fmt.Errorf("restart policy is not Never")
	}
	if obj.Spec.Template.Labels["app"] != name {
		return nil, fmt.Errorf("template app label missing")
	}
	containers := obj.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != "app" || containers[0].Image != "busybox:1.37" {
		return nil, fmt.Errorf("unexpected Job container")
	}
	if requireCompletion {
		complete := false
		for _, condition := range obj.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				complete = true
			}
		}
		if !complete {
			return nil, fmt.Errorf("job %s has not completed", name)
		}
	}
	return obj, nil
}
