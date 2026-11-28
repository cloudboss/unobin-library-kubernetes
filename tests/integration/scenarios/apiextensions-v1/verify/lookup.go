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
	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kubejson "k8s.io/apimachinery/pkg/util/json"
)

func verifyLookup(
	ctx context.Context, dir, stage string, definition *api.CustomResourceDefinition,
) error {
	configPath, planPath, revision := "config.ub", "plan.ubp", "initial"
	if stage == "updated" {
		configPath, planPath, revision = "update/config.ub", "plan-update.ubp", "updated"
	}
	planBody, err := os.ReadFile(filepath.Join(dir, planPath))
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(planBody, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	steps := map[string]runtime.PlanStep{}
	for _, step := range plan.Steps {
		steps[step.Address] = step
	}
	hit, missing := steps["data-source.definition"], steps["data-source.missing-definition"]
	if hit.Decision != runtime.DecisionRead || missing.Decision != runtime.DecisionRead {
		return fmt.Errorf("CRD lookups must always read")
	}
	if hit.ObservedOutputs != nil {
		return fmt.Errorf("CRD lookup must defer while its definition changes")
	}
	if !reflect.DeepEqual(missing.ObservedOutputs, map[string]any{
		"metadata": map[string]any{
			"name": "missing.unobin-it.example.test", "generation": int64(0),
			"uid": "", "resource-version": "", "labels": map[string]any{"fallback": revision},
			"annotations": map[string]any{"EXAMPLE.COM/Lookup": "fallback"},
		}, "spec": nil, "status": nil,
	}) {
		return fmt.Errorf("unexpected missing CRD plan output: %#v", missing.ObservedOutputs)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "apiextensions-v1"),
		"output", "-c", configPath, "--format", "json")
	cmd.Dir = dir
	body, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read CRD lookup outputs: %w", err)
	}
	var result struct {
		Outputs struct {
			Lookup map[string]any `json:"lookup"`
		} `json:"outputs"`
	}
	if err := kubejson.Unmarshal(body, &result); err != nil {
		return err
	}
	versions := []any{}
	for _, version := range definition.Spec.Versions {
		encoded, err := json.Marshal(version.Schema.OpenAPIV3Schema)
		if err != nil {
			return err
		}
		var schema map[string]any
		if err := kubejson.Unmarshal(encoded, &schema); err != nil {
			return err
		}
		versions = append(versions, map[string]any{
			"name": version.Name, "served": version.Served, "storage": version.Storage, "schema": schema,
		})
	}
	conditions := []any{}
	for _, condition := range definition.Status.Conditions {
		conditions = append(conditions, map[string]any{
			"type": string(condition.Type), "status": string(condition.Status),
			"last-transition-time": condition.LastTransitionTime.String(),
			"reason":               condition.Reason, "message": condition.Message,
		})
	}
	stored := []any{}
	for _, version := range definition.Status.StoredVersions {
		stored = append(stored, version)
	}
	want := map[string]any{
		"hit": map[string]any{
			"metadata": map[string]any{
				"name": definition.Name, "generation": definition.Generation,
				"uid": string(definition.UID), "resource-version": definition.ResourceVersion,
				"labels":      lookupMetadataMap(definition.Labels),
				"annotations": lookupMetadataMap(definition.Annotations),
			},
			"group": definition.Spec.Group, "scope": string(definition.Spec.Scope),
			"names": lookupNames(definition.Spec.Names), "versions": versions,
			"preserve-unknown-fields": definition.Spec.PreserveUnknownFields,
			"conversion": map[string]any{"strategy": string(definition.Spec.Conversion.Strategy),
				"webhook": nil},
			"status": map[string]any{
				"conditions": conditions, "stored-versions": stored,
				"accepted-names": lookupNames(definition.Status.AcceptedNames),
			},
		},
		"missing": missing.ObservedOutputs,
	}
	if !reflect.DeepEqual(want, result.Outputs.Lookup) {
		return fmt.Errorf("CRD lookup differs from typed API\n got: %#v\nwant: %#v",
			result.Outputs.Lookup, want)
	}
	return nil
}

func lookupNames(names api.CustomResourceDefinitionNames) map[string]any {
	var shortNames, categories any
	if names.ShortNames != nil {
		values := []any{}
		for _, name := range names.ShortNames {
			values = append(values, name)
		}
		shortNames = values
	}
	if names.Categories != nil {
		values := []any{}
		for _, category := range names.Categories {
			values = append(values, category)
		}
		categories = values
	}
	return map[string]any{
		"plural": names.Plural, "singular": names.Singular, "kind": names.Kind,
		"list-kind": names.ListKind, "short-names": shortNames, "categories": categories,
	}
}

func lookupMetadataMap(values map[string]string) map[string]any {
	out := map[string]any{}
	for key, value := range values {
		out[key] = value
	}
	return out
}
