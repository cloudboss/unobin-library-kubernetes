package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/state/local"
	corev1 "k8s.io/api/core/v1"
)

const (
	serviceAccountResource   = "resource.service-account-explicit"
	serviceAccountExisting   = "data-source.service-account-lookup-existing"
	serviceAccountMissing    = "data-source.service-account-lookup-missing"
	serviceAccountAbsentName = "unobin-it-service-account-lookup-absent"
)

func openServiceAccountPlan(build, name string) (*runtime.PlanFile, error) {
	body, err := os.ReadFile(filepath.Join(build, name))
	if err != nil {
		return nil, err
	}
	return runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
}

func serviceAccountLookupOutputs(
	meta map[string]any, automount bool, secrets, pulls []any,
) map[string]any {
	return map[string]any{
		"metadata": meta, "automount-service-account-token": automount,
		"secrets": secrets, "image-pull-secrets": pulls, "default-secret-name": "",
	}
}

func expectedServiceAccountLookups(account *corev1.ServiceAccount) map[string]map[string]any {
	stringMap := func(values map[string]string) map[string]any {
		result := make(map[string]any, len(values))
		for key, value := range values {
			result[key] = value
		}
		return result
	}
	metadata := map[string]any{
		"name": account.Name, "namespace": account.Namespace, "uid": string(account.UID),
		"resource-version": account.ResourceVersion, "generation": account.Generation,
		"labels": stringMap(account.Labels), "annotations": stringMap(account.Annotations),
	}
	secrets := make([]any, 0, len(account.Secrets))
	for _, reference := range account.Secrets {
		secrets = append(secrets, map[string]any{"name": reference.Name})
	}
	pulls := make([]any, 0, len(account.ImagePullSecrets))
	for _, reference := range account.ImagePullSecrets {
		pulls = append(pulls, map[string]any{"name": reference.Name})
	}
	automount := account.AutomountServiceAccountToken != nil && *account.AutomountServiceAccountToken
	missingMetadata := map[string]any{
		"name": serviceAccountAbsentName, "namespace": "default", "uid": "",
		"resource-version": "", "generation": int64(0),
		"labels": map[string]any{}, "annotations": map[string]any{"EXAMPLE.COM/Lookup": "fallback"},
	}
	return map[string]map[string]any{
		serviceAccountExisting: serviceAccountLookupOutputs(metadata, automount, secrets, pulls),
		serviceAccountMissing:  serviceAccountLookupOutputs(missingMetadata, false, []any{}, []any{}),
	}
}

func verifyServiceAccountLookupState(
	build string, plan *runtime.PlanFile, want map[string]map[string]any,
) error {
	store, err := local.NewStore(
		filepath.Join(build, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	for address, outputs := range want {
		entry := snapshot.Find(address)
		if entry == nil {
			return fmt.Errorf("applied state lacks %s", address)
		}
		if !reflect.DeepEqual(entry.Outputs, outputs) {
			return fmt.Errorf("%s applied outputs = %v, want %v", address, entry.Outputs, outputs)
		}
	}
	existing, missing := want[serviceAccountExisting], want[serviceAccountMissing]
	wantOutput := map[string]any{
		"metadata": existing["metadata"], "automount": existing["automount-service-account-token"],
		"secrets": existing["secrets"], "image-pull-secrets": existing["image-pull-secrets"],
		"default-secret-name": existing["default-secret-name"], "missing-metadata": missing["metadata"],
		"missing-automount":           missing["automount-service-account-token"],
		"missing-secrets":             missing["secrets"],
		"missing-image-pull-secrets":  missing["image-pull-secrets"],
		"missing-default-secret-name": missing["default-secret-name"],
	}
	output := snapshot.Outputs["service-account-lookup"]
	if !reflect.DeepEqual(output, wantOutput) {
		return fmt.Errorf("applied service-account-lookup output = %v, want %v", output, wantOutput)
	}
	return nil
}

func verifySteadyServiceAccountPlan(
	plan *runtime.PlanFile, want map[string]map[string]any,
) error {
	steps := make(map[string]runtime.PlanStep, len(plan.Steps))
	for _, step := range plan.Steps {
		steps[step.Address] = step
	}
	account, ok := steps[serviceAccountResource]
	if !ok {
		return fmt.Errorf("steady plan lacks %s", serviceAccountResource)
	}
	if account.Decision != runtime.DecisionNoOp {
		return fmt.Errorf("steady plan step %s = %s, want NoOp", account.Address, account.Decision)
	}
	for address, outputs := range want {
		step := steps[address]
		if step.Decision != runtime.DecisionRead || !reflect.DeepEqual(step.ObservedOutputs, outputs) {
			return fmt.Errorf("steady plan step %s = %s with %v, want read with %v",
				address, step.Decision, step.ObservedOutputs, outputs)
		}
	}
	return nil
}

func verifyServiceAccountLookups(
	ctx context.Context, build, phase string, account *corev1.ServiceAccount,
) error {
	planName := "plan.ubp"
	if phase == "updated" {
		planName = "plan-update.ubp"
	}
	plan, err := openServiceAccountPlan(build, planName)
	if err != nil {
		return err
	}
	want := expectedServiceAccountLookups(account)
	if err := verifyServiceAccountLookupState(build, plan, want); err != nil {
		return err
	}
	if phase != "updated" {
		return nil
	}

	const steadyPlan = "plan-service-account-steady.ubp"
	cmd := exec.CommandContext(ctx, "./core-v1", "plan", "-c", "update/config.ub", "-o", steadyPlan)
	cmd.Dir = build
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("plan steady ServiceAccount lookup: %w\n%s", err, output)
	}
	plan, err = openServiceAccountPlan(build, steadyPlan)
	if err != nil {
		return err
	}
	return verifySteadyServiceAccountPlan(plan, want)
}
