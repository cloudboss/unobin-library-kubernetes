package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"

	admissionv1 "k8s.io/api/admissionregistration/v1"
)

type webhookLookupResult struct {
	Metadata struct {
		Name            string
		Annotations     map[string]string
		Labels          map[string]string
		Generation      int64
		ResourceVersion string `json:"resource-version"`
		UID             string
	}
	Webhook []webhookLookupValues
}

type webhookLookupValues struct {
	Name                    string
	AdmissionReviewVersions []string `json:"admission-review-versions"`
	FailurePolicy           string   `json:"failure-policy"`
	MatchPolicy             string   `json:"match-policy"`
	ReinvocationPolicy      string   `json:"reinvocation-policy"`
	SideEffects             string   `json:"side-effects"`
	TimeoutSeconds          int32    `json:"timeout-seconds"`
}

func verifyMutatingWebhookLookup(
	ctx context.Context, dir, stage string, live admissionv1.MutatingWebhookConfiguration,
) error {
	if live.Annotations["kubectl.kubernetes.io/last-applied-configuration"] != "retained" ||
		live.Labels["kubernetes.io/lookup"] != "retained" {
		return fmt.Errorf("expected internal metadata for lookup of %s", live.Name)
	}
	config := "config.ub"
	if stage == "updated" {
		config = "update/config.ub"
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "admissionregistration-v1"),
		"output", "-c", config, "--format", "json")
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read lookup outputs: %s: %w", data, err)
	}
	var result struct {
		Outputs struct {
			Lookup struct {
				Hit, Miss webhookLookupResult
			} `json:"mutating-webhook-lookup"`
		}
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode lookup outputs: %w", err)
	}

	wantHit := webhookLookupResult{
		Webhook: make([]webhookLookupValues, 0, len(live.Webhooks)),
	}
	wantHit.Metadata.Name = live.Name
	wantHit.Metadata.UID = string(live.UID)
	wantHit.Metadata.Generation = live.Generation
	wantHit.Metadata.ResourceVersion = live.ResourceVersion
	wantHit.Metadata.Annotations = live.Annotations
	wantHit.Metadata.Labels = live.Labels
	for _, hook := range live.Webhooks {
		wantHit.Webhook = append(wantHit.Webhook, webhookLookupValues{
			Name: hook.Name, AdmissionReviewVersions: hook.AdmissionReviewVersions,
			FailurePolicy: string(*hook.FailurePolicy), MatchPolicy: string(*hook.MatchPolicy),
			ReinvocationPolicy: string(*hook.ReinvocationPolicy), SideEffects: string(*hook.SideEffects),
			TimeoutSeconds: *hook.TimeoutSeconds,
		})
	}
	if got := result.Outputs.Lookup.Hit; !reflect.DeepEqual(wantHit, got) {
		return fmt.Errorf("webhook lookup of %s differs: got %+v, want %+v", live.Name, got, wantHit)
	}

	wantMiss := webhookLookupResult{Webhook: []webhookLookupValues{}}
	wantMiss.Metadata.Name = "unobin-it-mutating-webhook-absent"
	wantMiss.Metadata.Annotations = map[string]string{"EXAMPLE.COM/Lookup": "any text"}
	wantMiss.Metadata.Labels = map[string]string{"example.com/value": ""}
	if got := result.Outputs.Lookup.Miss; !reflect.DeepEqual(wantMiss, got) {
		return fmt.Errorf("missing webhook lookup differs: got %+v, want %+v", got, wantMiss)
	}
	return nil
}
