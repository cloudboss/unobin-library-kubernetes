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

func verifyJobLookup() error {
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
		return fmt.Errorf("read compiled Job lookup output: %w", err)
	}
	var output struct {
		Outputs map[string]map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return err
	}
	var got svc.JobDataSourceOutput
	if err := runtime.Decode(&got, output.Outputs["job-lookup"]); err != nil {
		return err
	}
	if err := checkJobLookup(ctx, &got); err != nil {
		return err
	}
	if phase == "applied" {
		return checkJobLookupRemoval(ctx, build, got.Metadata.UID)
	}
	return nil
}

func checkJobLookup(ctx context.Context, got *svc.JobDataSourceOutput) error {
	lookup := &svc.JobDataSource{Metadata: svc.JobLookupMetadata{Name: completionJobName}}
	want, err := lookup.Read(ctx, nil)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("compiled Job lookup differs from live observation: got %#v, want %#v",
			got, want)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	live, err := api.BatchV1().Jobs("default").Get(ctx, completionJobName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if got.Spec == nil || got.Status == nil || got.Metadata.UID != string(live.UID) ||
		got.Spec.Parallelism == nil || *got.Spec.Parallelism != int64(*live.Spec.Parallelism) ||
		got.Status.Succeeded != int64(live.Status.Succeeded) ||
		got.Status.Failed != int64(live.Status.Failed) || got.Status.Active != int64(live.Status.Active) {
		return fmt.Errorf("typed Job lookup identity, spec or status differs from Kubernetes")
	}
	return nil
}

func checkJobLookupRemoval(ctx context.Context, build, uid string) error {
	root, err := filepath.Abs("../../../../..")
	if err != nil {
		return err
	}
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(root, "_output/integration/bin/unobin")
	}
	work := filepath.Join(build, "job-lookup-only")
	command := exec.CommandContext(ctx, cli, "compile", "-p", "testdata/ub/job-lookup",
		"-o", work, "--name", "job-lookup", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("compile Job lookup fixture: %w\n%s", err, output)
	}
	body, err := os.ReadFile("testdata/ub/job-lookup/config.ub")
	if err != nil {
		return err
	}
	body = []byte(strings.ReplaceAll(string(body), "JOB_LOOKUP_NAME", completionJobName))
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
		command := exec.CommandContext(ctx, filepath.Join(work, "job-lookup"), args...)
		command.Dir = work
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("job lookup %v: %w\n%s", args, err, output)
		}
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	live, err := api.BatchV1().Jobs("default").Get(ctx, completionJobName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(live.UID) != uid {
		return fmt.Errorf("job lookup destroy changed remote identity")
	}
	return nil
}
