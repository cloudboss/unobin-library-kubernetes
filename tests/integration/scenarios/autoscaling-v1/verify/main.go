package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type hpaIdentity struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type hpaOutput struct {
	Metadata hpaIdentity `json:"metadata"`
	Spec     struct {
		CPU *int32 `json:"target-cpu-utilization-percentage"`
	} `json:"spec"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	phase := os.Getenv("VERIFY_PHASE")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hpas := api.AutoscalingV1().HorizontalPodAutoscalers("default")
	path := filepath.Join(dir, "hpa-identities.json")
	identities := map[string]hpaIdentity{}
	if phase != "applied" {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &identities); err != nil {
			return err
		}
	}
	if phase == "destroyed" {
		for _, role := range []string{"configure", "clear"} {
			name := identities[role].Name
			if name == "" {
				return fmt.Errorf("missing saved HPA name for %s", role)
			}
			if _, err := hpas.Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				return fmt.Errorf("expected deleted HPA %s, got %v", name, err)
			}
		}
		fmt.Println("ok: both generated HPAs were deleted")
		return nil
	}
	config, max := "config.ub", int32(4)
	var configuredCPU *int32
	if phase == "updated" {
		config, max, configuredCPU = "update/config.ub", 6, new(int32(75))
	}
	outputs, err := readOutputs(ctx, dir, config)
	if err != nil {
		return err
	}
	for _, expected := range []struct {
		role string
		cpu  *int32
	}{
		{role: "configure", cpu: configuredCPU},
		{role: "clear", cpu: new(int32(65))},
	} {
		output := outputs[expected.role]
		id := output.Metadata
		prefix := "unobin-hpa-v1-" + expected.role + "-"
		if !strings.HasPrefix(id.Name, prefix) || id.Name == prefix || id.UID == "" {
			return fmt.Errorf("unexpected generated identity for %s: %+v", expected.role, id)
		}
		if phase == "updated" && identities[expected.role] != id {
			return fmt.Errorf("HPA %s identity changed from %+v to %+v",
				expected.role, identities[expected.role], id)
		}
		hpa, err := hpas.Get(ctx, id.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := verifyHPA(hpa, output, phase, max, expected.cpu); err != nil {
			return err
		}
		identities[expected.role] = id
	}
	if phase == "applied" {
		body, err := json.Marshal(identities)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("ok: HPA API values match factory outputs after %s\n", phase)
	return nil
}

func readOutputs(ctx context.Context, dir, config string) (map[string]hpaOutput, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "autoscaling-v1"), "output", "-c", config,
		"--format", "json")
	cmd.Dir = dir
	body, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read HPA factory outputs: %w", err)
	}
	var result struct {
		Outputs map[string]hpaOutput `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode HPA factory outputs: %w", err)
	}
	return result.Outputs, nil
}

func verifyHPA(
	hpa *autoscalingv1.HorizontalPodAutoscaler, output hpaOutput, stage string, max int32, cpu *int32,
) error {
	if string(hpa.UID) != output.Metadata.UID || hpa.Namespace != "default" ||
		hpa.Annotations["example.com/stage"] != stage ||
		!maps.Equal(hpa.Labels, map[string]string{"example.com/stage": stage}) {
		return fmt.Errorf("unexpected HPA metadata for %s", hpa.Name)
	}
	spec := hpa.Spec
	ref := autoscalingv1.CrossVersionObjectReference{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "absent-target",
	}
	if spec.MaxReplicas != max || !ptr.Equal(spec.MinReplicas, new(int32(1))) ||
		spec.ScaleTargetRef != ref {
		return fmt.Errorf("unexpected HPA replica bounds or target reference for %s", hpa.Name)
	}
	if !ptr.Equal(spec.TargetCPUUtilizationPercentage, output.Spec.CPU) {
		return fmt.Errorf("HPA %s API and factory output CPU targets differ", hpa.Name)
	}
	if cpu != nil && !ptr.Equal(spec.TargetCPUUtilizationPercentage, cpu) {
		return fmt.Errorf("HPA %s CPU target differs from %d", hpa.Name, *cpu)
	}
	return nil
}
