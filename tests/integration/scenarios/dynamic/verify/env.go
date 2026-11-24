package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	envNamespace      = "default"
	envDeployment     = "unobin-it-dynamic-env"
	envReplacement    = "unobin-it-dynamic-env-next"
	envCronJob        = "unobin-it-dynamic-env-cron"
	envPod            = "unobin-it-dynamic-env-pod"
	envTemporary      = "resource.temporary-env['temporary']"
	envContainerImage = "busybox:1.36"
)

func verifyEnv(
	ctx context.Context, api kubernetes.Interface, snapshot *state.Snapshot, phase, dir string,
) error {
	deployments := api.AppsV1().Deployments(envNamespace)
	deployment, err := deployments.Get(ctx, envDeployment, metav1.GetOptions{})
	if err != nil {
		return err
	}
	next, err := deployments.Get(ctx, envReplacement, metav1.GetOptions{})
	if err != nil {
		return err
	}
	cron, err := api.BatchV1().CronJobs(envNamespace).Get(ctx, envCronJob, metav1.GetOptions{})
	if err != nil {
		return err
	}
	pod, err := api.CoreV1().Pods(envNamespace).Get(ctx, envPod, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if deployment.Annotations["unobin.cloudboss.dev/scenario"] != "env" ||
		deployment.Spec.Template.Labels["app"] != envDeployment {
		return fmt.Errorf("env management changed Deployment metadata")
	}
	uids := map[string]string{
		envDeployment: string(deployment.UID), envReplacement: string(next.UID),
		envCronJob: string(cron.UID), envPod: string(pod.UID),
	}
	external := []corev1.EnvVar{{Name: "EXTERNAL", Value: "keep"}}
	initExternal := []corev1.EnvVar{{Name: "INIT_EXTERNAL", Value: "keep-init"}}
	deploymentEnv, cronEnv, movedEnv := expectedEnv(phase)
	workloads := []struct {
		name       string
		pod        corev1.PodSpec
		main, init []corev1.EnvVar
	}{
		{envDeployment, deployment.Spec.Template.Spec, appendEnv(external, deploymentEnv),
			initExternal},
		{envReplacement, next.Spec.Template.Spec, external, initExternal},
		{envCronJob, cron.Spec.JobTemplate.Spec.Template.Spec, external, initExternal},
		{envPod, pod.Spec, external, initExternal},
	}
	movedName := envDeployment
	planName, config := "plan.ubp", "config.ub"
	decisions := map[string]runtime.Decision{
		"resource.deployment-env": runtime.DecisionCreate,
		"resource.cron-env":       runtime.DecisionCreate,
		"resource.relocated-env":  runtime.DecisionCreate,
		envTemporary:              runtime.DecisionCreate,
	}
	if phase == "updated" {
		movedName = envReplacement
		workloads[1].init = appendEnv(initExternal, movedEnv)
		workloads[2].main = cronEnv
		planName, config = "plan-update.ubp", "update/config.ub"
		decisions["resource.deployment-env"] = runtime.DecisionUpdate
		decisions["resource.cron-env"] = runtime.DecisionUpdate
		decisions["resource.relocated-env"] = runtime.DecisionReplace
		decisions[envTemporary] = runtime.DecisionDestroy
	} else {
		workloads[0].init = appendEnv(initExternal, movedEnv)
		workloads[1].main = appendEnv(external, []corev1.EnvVar{{Name: "TEMPORARY", Value: "remove"}})
		workloads[2].init = appendEnv(initExternal, cronEnv)
	}
	for _, workload := range workloads {
		err := verifyWorkloadEnv(workload.name, workload.pod, workload.main, workload.init)
		if err != nil {
			return err
		}
	}
	uidPath := filepath.Join(dir, "env-workload-uids.json")
	if err := verifyEnvUIDs(phase, uidPath, uids); err != nil {
		return err
	}
	for _, want := range []struct {
		output, workload string
		values           []corev1.EnvVar
	}{
		{"deployment-env", envDeployment, deploymentEnv},
		{"cron-env", envCronJob, cronEnv},
		{"relocated-env", movedName, movedEnv},
	} {
		if err := verifyEnvOutput(snapshot, want.output, uids[want.workload], want.values); err != nil {
			return err
		}
	}
	if err := verifyEnvPlan(filepath.Join(dir, planName), decisions); err != nil {
		return err
	}
	if phase == "updated" {
		delete(decisions, envTemporary)
	}
	for address := range decisions {
		decisions[address] = runtime.DecisionNoOp
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "dynamic"),
		"plan", "-c", config, "-o", "env-settled.ubp")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("plan settled env: %w\n%s", err, output)
	}
	if err := verifyEnvPlan(filepath.Join(dir, "env-settled.ubp"), decisions); err != nil {
		return err
	}
	if phase == "applied" {
		return verifyEnvAdmission(ctx, api, pod, next)
	}
	return nil
}

func expectedEnv(phase string) (deployment, cron, relocated []corev1.EnvVar) {
	if phase == "updated" {
		return []corev1.EnvVar{
				{Name: "LAST", Value: "$$(BASE)"},
				{Name: "FIRST", Value: "updated"},
				{Name: "CPU", Value: "literal"},
				{Name: "BACK", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1", FieldPath: "metadata.namespace",
					},
				}},
			}, []corev1.EnvVar{{Name: "EXTERNAL", Value: "claimed"}, {Name: "MANAGED", Value: phase}},
			[]corev1.EnvVar{{Name: "RELOCATED", Value: phase}}
	}
	return []corev1.EnvVar{
			{Name: "FIRST", Value: "$(BASE)"},
			{Name: "CONFIG", ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "example"},
					Key:                  "value", Optional: new(true),
				},
			}},
			{Name: "SECRET", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "example"},
					Key:                  "key", Optional: new(false),
				},
			}},
			{Name: "FIELD", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"},
			}},
			{Name: "CPU", ValueFrom: &corev1.EnvVarSource{
				ResourceFieldRef: &corev1.ResourceFieldSelector{
					Resource: "limits.cpu", Divisor: resource.MustParse("1m"),
				},
			}},
			{Name: "LAST", Value: "$$(BASE)"},
			{Name: "EMPTY"},
			{Name: "BACK", Value: "literal"},
		}, []corev1.EnvVar{{Name: "MANAGED", Value: "first"}},
		[]corev1.EnvVar{{Name: "RELOCATED", Value: "first"}}
}

func appendEnv(base, managed []corev1.EnvVar) []corev1.EnvVar {
	return append(append([]corev1.EnvVar(nil), base...), managed...)
}

func verifyWorkloadEnv(
	name string, pod corev1.PodSpec, main, init []corev1.EnvVar,
) error {
	for _, check := range []struct {
		name       string
		containers []corev1.Container
		command    []string
		env        []corev1.EnvVar
	}{
		{"main", pod.Containers, []string{"sleep", "3600"}, main},
		{"init", pod.InitContainers, []string{"true"}, init},
	} {
		if len(check.containers) != 1 || check.containers[0].Name != check.name ||
			check.containers[0].Image != envContainerImage ||
			!reflect.DeepEqual(check.containers[0].Command, check.command) {
			return fmt.Errorf("%s container details changed for %s", name, check.name)
		}
		got, err := json.Marshal(check.containers[0].Env)
		if err != nil {
			return err
		}
		want, err := json.Marshal(check.env)
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			return fmt.Errorf("%s/%s env = %s, want %s", name, check.name, got, want)
		}
	}
	return nil
}

func verifyEnvOutput(
	snapshot *state.Snapshot, output, uid string, expected []corev1.EnvVar,
) error {
	entry := snapshot.Find("resource." + output)
	if entry == nil || entry.Outputs["uid"] != uid || entry.Outputs["namespace"] != envNamespace {
		return fmt.Errorf("unexpected identity in %s state output", output)
	}
	want := make([]any, 0, len(expected))
	for _, item := range expected {
		var value any
		if item.Value != "" {
			value = item.Value
		}
		want = append(want, map[string]any{"name": item.Name, "value": value})
	}
	if got := snapshot.Outputs[output]; !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s output = %v, want %v", output, got, want)
	}
	return nil
}

func verifyEnvUIDs(phase, path string, uids map[string]string) error {
	if phase == "applied" {
		body, err := json.Marshal(uids)
		if err != nil {
			return err
		}
		return os.WriteFile(path, body, 0o600)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var before map[string]string
	if err := json.Unmarshal(body, &before); err != nil {
		return err
	}
	if !reflect.DeepEqual(uids, before) {
		return fmt.Errorf("env update changed workload UIDs from %v to %v", before, uids)
	}
	return nil
}

func verifyEnvPlan(path string, want map[string]runtime.Decision) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	got := make(map[string]runtime.Decision, len(want))
	for _, step := range plan.Steps {
		if _, tracked := want[step.Address]; tracked {
			got[step.Address] = step.Decision
		}
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s env decisions = %v, want %v", filepath.Base(path), got, want)
	}
	return nil
}

func verifyEnvDestroyed(ctx context.Context, api kubernetes.Interface, dir string) error {
	if err := verifyEnvPlan(filepath.Join(dir, "destroy.ubp"), map[string]runtime.Decision{
		"resource.deployment-env": runtime.DecisionDestroy,
		"resource.cron-env":       runtime.DecisionDestroy,
		"resource.relocated-env":  runtime.DecisionDestroy,
	}); err != nil {
		return err
	}
	deployments := api.AppsV1().Deployments(envNamespace)
	for _, name := range []string{envDeployment, envReplacement} {
		if _, err := deployments.Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected deleted Deployment %s, got %v", name, err)
		}
	}
	cron := api.BatchV1().CronJobs(envNamespace)
	if _, err := cron.Get(ctx, envCronJob, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected deleted CronJob, got %v", err)
	}
	pods := api.CoreV1().Pods(envNamespace)
	if _, err := pods.Get(ctx, envPod, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected deleted Pod, got %v", err)
	}
	return nil
}
