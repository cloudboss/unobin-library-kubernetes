package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/batch/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const cronJobLookupName = "unobin-it-cron-job-replaced"

func verifyCronJobLookup() error {
	phase, build := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase == "destroyed" {
		return nil
	}
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	configuration := "config.ub"
	if phase == "updated" {
		configuration = "update/config.ub"
	}
	command := exec.CommandContext(ctx, filepath.Join(build, "batch-v1"), "output",
		"--format", "json", "-c", configuration)
	command.Dir = build
	data, err := command.Output()
	if err != nil {
		return fmt.Errorf("read compiled CronJob lookup output: %w", err)
	}
	var output struct {
		Outputs map[string]map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return err
	}
	var got svc.CronJobDataSourceOutput
	if err := runtime.Decode(&got, output.Outputs["cron-job-lookup"]); err != nil {
		return err
	}
	if err := checkCronJobLookup(ctx, &got); err != nil {
		return err
	}
	if phase == "applied" {
		return checkCronJobLookupRemoval(ctx, build, got.Metadata.UID)
	}
	return nil
}

func checkCronJobLookup(ctx context.Context, got *svc.CronJobDataSourceOutput) error {
	lookup := &svc.CronJobDataSource{Metadata: svc.CronJobLookupMetadata{Name: cronJobLookupName}}
	want, err := lookup.Read(ctx, nil)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("compiled CronJob lookup differs from live observation: got %#v, want %#v",
			got, want)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	live, err := api.BatchV1().CronJobs("default").Get(ctx, cronJobLookupName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if got.Spec == nil || got.Status == nil || got.Metadata.UID != string(live.UID) ||
		got.Spec.Schedule != live.Spec.Schedule || got.Spec.Suspend == nil ||
		*got.Spec.Suspend != *live.Spec.Suspend ||
		got.Spec.JobTemplate.Spec.Template.Spec == nil ||
		(*got.Spec.JobTemplate.Spec.Template.Spec.Container)[0].Image == nil ||
		*(*got.Spec.JobTemplate.Spec.Template.Spec.Container)[0].Image !=
			live.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image ||
		!reflect.DeepEqual(got.Status.Active, []svc.CronJobLookupReference{}) ||
		live.Status.LastScheduleTime != nil ||
		live.Status.LastSuccessfulTime != nil {
		return fmt.Errorf("typed CronJob lookup identity, spec or status differs from Kubernetes")
	}
	return nil
}

func checkCronJobLookupRemoval(ctx context.Context, build, uid string) error {
	root, err := filepath.Abs("../../../../..")
	if err != nil {
		return err
	}
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(root, "_output/integration/bin/unobin")
	}
	work := filepath.Join(build, "cron-job-lookup-only")
	command := exec.CommandContext(ctx, cli, "compile", "-p", "testdata/ub/cron-job-lookup",
		"-o", work, "--name", "cron-job-lookup", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("compile CronJob lookup fixture: %w\n%s", err, output)
	}
	body, err := os.ReadFile("testdata/ub/cron-job-lookup/config.ub")
	if err != nil {
		return err
	}
	body = []byte(strings.ReplaceAll(string(body), "CRON_JOB_LOOKUP_NAME", cronJobLookupName))
	if err := os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"pin", "-c", "config.ub"},
		{"plan", "-c", "config.ub", "-o", "lookup.ubp"},
		{"apply", "lookup.ubp"},
		{"plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp"},
		{"apply", "destroy.ubp"},
	} {
		command := exec.CommandContext(ctx, filepath.Join(work, "cron-job-lookup"), args...)
		command.Dir = work
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("cron-job lookup %v: %w\n%s", args, err, output)
		}
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	live, err := api.BatchV1().CronJobs("default").Get(ctx, cronJobLookupName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(live.UID) != uid {
		return fmt.Errorf("cron-job lookup destroy changed remote identity")
	}
	return nil
}
