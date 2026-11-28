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

func checkDeploymentLookupAPI(ctx context.Context) (result error) {
	api, err := deploymentAPI(ctx)
	if err != nil {
		return err
	}
	r := deploymentInput(ctx)
	r.WaitForRollout = new(false)
	r.Spec.Paused = new(true)
	created, err := r.Create(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, r.Delete(ctx, nil, deploymentPrior{Inputs: *r, Outputs: created}))
	}()
	live, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	out, err := (&svc.DeploymentDataSource{Metadata: svc.DeploymentLookupMetadata{
		Name: created.Name,
	}}).Read(ctx, nil)
	if err != nil {
		return err
	}
	if out.Spec == nil || !out.Spec.Paused || out.Spec.Replicas == nil || *out.Spec.Replicas != 1 ||
		out.Status == nil || out.Metadata.UID != string(live.UID) ||
		!maps.Equal(out.Metadata.Annotations, live.Annotations) {
		return fmt.Errorf("lookup did not return the live paused Deployment")
	}
	after, err := api.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(live.Spec, after.Spec) || live.UID != after.UID {
		return fmt.Errorf("lookup changed the Deployment")
	}
	missing, err := (&svc.DeploymentDataSource{Metadata: svc.DeploymentLookupMetadata{
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

func verifyDeploymentLookup() error {
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
					Replicas *int64 `json:"replicas"`
					Template struct {
						Spec struct {
							Container []struct{ Name, Image string } `json:"container"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"spec"`
				Status struct {
					Replicas           int64 `json:"replicas"`
					ReadyReplicas      int64 `json:"ready-replicas"`
					ObservedGeneration int64 `json:"observed-generation"`
				} `json:"status"`
			} `json:"deployment-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	api, err := deploymentAPI(ctx)
	if err != nil {
		return err
	}
	live, err := api.Get(ctx, deploymentName, metav1.GetOptions{})
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
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1" &&
		(out.Status.Replicas != int64(live.Status.Replicas) ||
			out.Status.ReadyReplicas != int64(live.Status.ReadyReplicas) ||
			out.Status.ObservedGeneration != live.Status.ObservedGeneration) {
		return fmt.Errorf("factory lookup status differs from the API")
	}
	return nil
}
