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

func checkDaemonSetLookupAPI(ctx context.Context) (result error) {
	api, err := daemonSetAPI(ctx)
	if err != nil {
		return err
	}
	r := daemonSetInput(ctx)
	r.WaitForRollout = new(false)
	r.Spec.Strategy = &svc.DaemonSetStrategy{Type: new("OnDelete")}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, r.Delete(ctx, nil, daemonSetPrior{Inputs: *r, Outputs: created}))
	}()
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	out, err := (&svc.DaemonSetDataSource{Metadata: svc.DeploymentLookupMetadata{
		Name: created.Name,
	}}).Read(ctx, nil)
	if err != nil {
		return err
	}
	if out.Spec == nil || out.Spec.UpdateStrategy.Type != "OnDelete" ||
		out.Status == nil || out.Metadata.UID != string(live.UID) ||
		!maps.Equal(out.Metadata.Annotations, live.Annotations) {
		return fmt.Errorf("lookup did not return the live OnDelete DaemonSet")
	}
	after, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(live.Spec, after.Spec) || live.UID != after.UID {
		return fmt.Errorf("lookup changed the DaemonSet")
	}
	missing, err := (&svc.DaemonSetDataSource{Metadata: svc.DeploymentLookupMetadata{
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

func verifyDaemonSetLookup() error {
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
					MinReadySeconds      int64  `json:"min-ready-seconds"`
					RevisionHistoryLimit *int64 `json:"revision-history-limit"`
					UpdateStrategy       struct {
						Type          string `json:"type"`
						RollingUpdate *struct {
							MaxSurge       *string `json:"max-surge"`
							MaxUnavailable *string `json:"max-unavailable"`
						} `json:"rolling-update"`
					} `json:"update-strategy"`
					Template struct {
						Spec struct {
							Container []struct{ Name, Image string } `json:"container"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
				Status map[string]any `json:"status"`
			} `json:"daemon-set-lookup"`
			Missing map[string]any `json:"daemon-set-missing"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	api, err := daemonSetAPI(ctx)
	if err != nil {
		return err
	}
	live, err := api.Get(ctx, daemonSetName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	out := result.Outputs.Lookup
	if out.Metadata.Name != live.Name || out.Metadata.Namespace != live.Namespace ||
		out.Metadata.UID != string(live.UID) || !maps.Equal(out.Metadata.Labels, live.Labels) {
		return fmt.Errorf("factory lookup identity differs from the API")
	}
	containers := out.Spec.Template.Spec.Container
	if len(containers) != 1 || containers[0].Name != live.Spec.Template.Spec.Containers[0].Name ||
		containers[0].Image != live.Spec.Template.Spec.Containers[0].Image {
		return fmt.Errorf("factory lookup template differs from the API")
	}

	if out.Spec.MinReadySeconds != int64(live.Spec.MinReadySeconds) ||
		out.Spec.UpdateStrategy.Type != string(live.Spec.UpdateStrategy.Type) {
		return fmt.Errorf("factory lookup spec differs from the API")
	}
	var history *int64
	if live.Spec.RevisionHistoryLimit != nil {
		history = new(int64(*live.Spec.RevisionHistoryLimit))
	}
	if !reflect.DeepEqual(history, out.Spec.RevisionHistoryLimit) {
		return fmt.Errorf("factory lookup history differs from the API")
	}
	rolling := out.Spec.UpdateStrategy.RollingUpdate
	native := live.Spec.UpdateStrategy.RollingUpdate
	if (rolling == nil) != (native == nil) {
		return fmt.Errorf("factory lookup strategy differs from the API")
	}
	if rolling != nil {
		var surge, unavailable *string
		if native.MaxSurge != nil {
			surge = new(native.MaxSurge.String())
		}
		if native.MaxUnavailable != nil {
			unavailable = new(native.MaxUnavailable.String())
		}
		if !reflect.DeepEqual(rolling.MaxSurge, surge) ||
			!reflect.DeepEqual(rolling.MaxUnavailable, unavailable) {
			return fmt.Errorf("factory lookup thresholds differ from the API")
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
		"observed-generation":      float64(status.ObservedGeneration),
		"current-number-scheduled": float64(status.CurrentNumberScheduled),
		"number-misscheduled":      float64(status.NumberMisscheduled),
		"desired-number-scheduled": float64(status.DesiredNumberScheduled),
		"number-ready":             float64(status.NumberReady),
		"updated-number-scheduled": float64(status.UpdatedNumberScheduled),
		"number-available":         float64(status.NumberAvailable),
		"number-unavailable":       float64(status.NumberUnavailable),
		"collision-count":          collision, "conditions": conditions,
	}
	if !reflect.DeepEqual(out.Status, expectedStatus) {
		return fmt.Errorf("factory lookup status differs from the API")
	}
	expectedMissing := map[string]any{
		"metadata": map[string]any{
			"name": "unobin-it-daemon-set-lookup-missing", "namespace": "default",
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
