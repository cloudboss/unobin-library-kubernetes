package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyCronJob() error {
	phase, build := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if build == "" || (phase != "applied" && phase != "updated" && phase != "destroyed") {
		return fmt.Errorf("VERIFY_PHASE and VERIFY_BUILD_DIR are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	jobs := api.BatchV1().CronJobs("default")
	for _, suffix := range []string{"mutable", "replaced"} {
		name := "unobin-it-cron-job-" + suffix
		obj, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if phase == "destroyed" {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("destroyed CronJob %s: %v", name, err)
			}
			continue
		}
		if err != nil {
			return err
		}
		if obj.UID == "" || obj.Name != name || obj.Namespace != "default" {
			return fmt.Errorf("invalid CronJob identity")
		}
		uidPath := filepath.Join(build, name+"-uid")
		if phase == "applied" {
			if err := os.WriteFile(uidPath, []byte(obj.UID), 0o600); err != nil {
				return err
			}
		} else {
			uid, err := os.ReadFile(uidPath)
			if err != nil {
				return err
			}
			same := string(uid) == string(obj.UID)
			if same != (suffix == "mutable") {
				return fmt.Errorf("unexpected CronJob replacement: %s", name)
			}
		}
		if suffix != "mutable" {
			continue
		}
		stage, ttl, suspended := "initial", int32(300), false
		if phase == "updated" {
			stage, ttl, suspended = "updated", 600, true
		}
		if obj.Labels["stage"] != stage || obj.Spec.JobTemplate.Labels["stage"] != stage ||
			obj.Spec.JobTemplate.Spec.TTLSecondsAfterFinished == nil ||
			*obj.Spec.JobTemplate.Spec.TTLSecondsAfterFinished != ttl ||
			obj.Spec.Suspend == nil || *obj.Spec.Suspend != suspended ||
			obj.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent ||
			obj.Spec.TimeZone == nil || *obj.Spec.TimeZone != "Etc/UTC" ||
			obj.Spec.SuccessfulJobsHistoryLimit == nil || *obj.Spec.SuccessfulJobsHistoryLimit != 2 ||
			obj.Spec.FailedJobsHistoryLimit == nil || *obj.Spec.FailedJobsHistoryLimit != 0 {
			return fmt.Errorf("CronJob values differ from %s inputs", stage)
		}
		if phase == "applied" && os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1" {
			err := wait.PollUntilContextCancel(ctx, time.Second, true,
				func(ctx context.Context) (bool, error) {
					children, err := api.BatchV1().Jobs("default").List(ctx, metav1.ListOptions{})
					if err != nil {
						return false, err
					}
					for _, child := range children.Items {
						for _, owner := range child.OwnerReferences {
							if owner.UID == obj.UID && owner.Kind == "CronJob" && owner.Controller != nil &&
								*owner.Controller && child.Spec.TTLSecondsAfterFinished != nil &&
								*child.Spec.TTLSecondsAfterFinished == ttl {
								return true, nil
							}
						}
					}
					return false, nil
				})
			if err != nil {
				return fmt.Errorf("wait for scheduled owned Job: %w", err)
			}
		}
	}
	return nil
}
