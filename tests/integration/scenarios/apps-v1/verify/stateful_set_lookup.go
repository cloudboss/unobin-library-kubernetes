package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
)

func checkStatefulSetLookupAPI(ctx context.Context) (result error) {
	api, err := statefulSetAPI(ctx)
	if err != nil {
		return err
	}
	r := statefulSetInput(ctx)
	r.WaitForRollout = new(false)
	r.Spec.Replicas = new("0")
	r.Spec.UpdateStrategy = &svc.StatefulSetUpdateStrategy{Type: new("OnDelete")}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, r.Delete(ctx, nil, statefulSetPrior{Inputs: *r, Outputs: created}))
	}()
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	out, err := (&svc.StatefulSetDataSource{Metadata: svc.DeploymentLookupMetadata{
		Name: created.Name,
	}}).Read(ctx, nil)
	if err != nil {
		return err
	}
	if out.Spec == nil || out.Spec.UpdateStrategy.Type != "OnDelete" ||
		out.Spec.Replicas == nil || *out.Spec.Replicas != 0 ||
		out.Status == nil || out.Metadata.UID != string(live.UID) ||
		!maps.Equal(out.Metadata.Annotations, live.Annotations) {
		return fmt.Errorf("lookup did not return the live zero-replica StatefulSet")
	}
	after, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(live.Spec, after.Spec) || live.UID != after.UID {
		return fmt.Errorf("lookup changed the StatefulSet")
	}
	missing, err := (&svc.StatefulSetDataSource{Metadata: svc.DeploymentLookupMetadata{
		Name: created.Name + "-missing",
	}}).Read(ctx, nil)
	if err != nil {
		return err
	}
	if missing.Spec != nil || missing.Status != nil ||
		missing.Metadata.Name != created.Name+"-missing" {
		return fmt.Errorf("missing lookup did not return null spec/status")
	}
	return nil
}

func verifyStatefulSetLookup() error {
	if os.Getenv("VERIFY_PHASE") == "destroyed" {
		return nil
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	configuration := "config.ub"
	if os.Getenv("VERIFY_PHASE") == "updated" {
		configuration = "update/config.ub"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(dir, "apps-v1"),
		"output", "-c", configuration, "--format", "json")
	command.Dir = dir
	body, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read lookup output: %w: %s", err, body)
	}
	var result struct {
		Outputs struct {
			Lookup struct {
				Metadata struct {
					Name      string            `json:"name"`
					Namespace string            `json:"namespace"`
					UID       string            `json:"uid"`
					Labels    map[string]string `json:"labels"`
				} `json:"metadata"`
				Spec struct {
					Replicas            *int64 `json:"replicas"`
					ServiceName         string `json:"service-name"`
					PodManagementPolicy string `json:"pod-management-policy"`
					UpdateStrategy      struct {
						Type string `json:"type"`
					} `json:"update-strategy"`
					VolumeClaimTemplate []struct {
						Metadata struct {
							Name string `json:"name"`
						} `json:"metadata"`
						Spec struct {
							AccessModes []string `json:"access-modes"`
							Resources   struct {
								Requests map[string]string `json:"requests"`
							} `json:"resources"`
						} `json:"spec"`
					} `json:"volume-claim-template"`
					Template struct {
						Spec struct {
							Container []struct{ Name, Image string } `json:"container"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
				Status map[string]any `json:"status"`
			} `json:"stateful-set-lookup"`
			Missing map[string]any `json:"stateful-set-missing"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	api, err := statefulSetAPI(ctx)
	if err != nil {
		return err
	}
	live, err := api.Get(ctx, statefulSetName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	out := result.Outputs.Lookup
	if out.Metadata.Name != live.Name || out.Metadata.Namespace != live.Namespace ||
		out.Metadata.UID != string(live.UID) || !maps.Equal(out.Metadata.Labels, live.Labels) ||
		out.Spec.Replicas == nil || *out.Spec.Replicas != int64(*live.Spec.Replicas) {
		return fmt.Errorf("factory lookup identity or replicas differ from the API")
	}
	containers := out.Spec.Template.Spec.Container
	if len(containers) != 1 || containers[0].Name != live.Spec.Template.Spec.Containers[0].Name ||
		containers[0].Image != live.Spec.Template.Spec.Containers[0].Image {
		return fmt.Errorf("factory lookup template differs from the API")
	}

	if out.Spec.ServiceName != live.Spec.ServiceName ||
		out.Spec.PodManagementPolicy != string(live.Spec.PodManagementPolicy) ||
		out.Spec.UpdateStrategy.Type != string(live.Spec.UpdateStrategy.Type) {
		return fmt.Errorf("factory lookup spec differs from the API")
	}
	claims := out.Spec.VolumeClaimTemplate
	if len(claims) != len(live.Spec.VolumeClaimTemplates) {
		return fmt.Errorf("factory lookup claim templates differ from the API")
	}
	for i, claim := range claims {
		native := live.Spec.VolumeClaimTemplates[i]
		modes := make([]string, len(native.Spec.AccessModes))
		for j, mode := range native.Spec.AccessModes {
			modes[j] = string(mode)
		}
		requests := map[string]string{}
		for name, quantity := range native.Spec.Resources.Requests {
			requests[string(name)] = quantity.String()
		}
		if claim.Metadata.Name != native.Name || !reflect.DeepEqual(claim.Spec.AccessModes, modes) ||
			!maps.Equal(claim.Spec.Resources.Requests, requests) {
			return fmt.Errorf("factory lookup claim template %d differs from the API", i)
		}
	}
	status := live.Status
	conditions := []any{}
	for _, condition := range status.Conditions {
		var transition any
		if !condition.LastTransitionTime.IsZero() {
			transition = condition.LastTransitionTime.UTC().Format(time.RFC3339)
		}
		conditions = append(conditions, map[string]any{
			"type": string(condition.Type), "status": string(condition.Status),
			"reason": condition.Reason, "message": condition.Message, "last-transition-time": transition,
		})
	}
	var collision any
	if status.CollisionCount != nil {
		collision = float64(*status.CollisionCount)
	}
	expectedStatus := map[string]any{
		"observed-generation": float64(status.ObservedGeneration), "replicas": float64(status.Replicas),
		"ready-replicas":     float64(status.ReadyReplicas),
		"current-replicas":   float64(status.CurrentReplicas),
		"updated-replicas":   float64(status.UpdatedReplicas),
		"available-replicas": float64(status.AvailableReplicas),
		"current-revision":   status.CurrentRevision, "update-revision": status.UpdateRevision,
		"collision-count": collision, "conditions": conditions,
	}
	if !reflect.DeepEqual(out.Status, expectedStatus) {
		return fmt.Errorf("factory lookup status differs from the API")
	}
	expectedMissing := map[string]any{
		"metadata": map[string]any{
			"name": "unobin-it-stateful-set-lookup-missing", "namespace": "default",
			"generate-name": "", "uid": "", "resource-version": "", "generation": float64(0),
			"labels": map[string]any{}, "annotations": map[string]any{},
		},
		"spec": nil, "status": nil,
	}
	if !reflect.DeepEqual(result.Outputs.Missing, expectedMissing) {
		return fmt.Errorf("factory missing lookup differs from the empty result")
	}
	return nil
}
