package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	phase := os.Getenv("VERIFY_PHASE")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid VERIFY_PHASE")
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
	list, err := api.AutoscalingV2().HorizontalPodAutoscalers("default").List(ctx,
		metav1.ListOptions{
			LabelSelector: "test.unobin.cloudboss.io/scenario=autoscaling-v2-horizontal-pod-autoscaler"})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("%d HPAs remain after deletion", len(list.Items))
		}
		return nil
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("found %d HPAs, want 2", len(list.Items))
	}
	max, min, cpu, window, value := int32(4), int32(1), int32(65), int32(0), int32(4)
	target := "unobin-target"
	if phase == "updated" {
		max, min, cpu, window, value = 6, 2, 75, 30, 2
		target = "unobin-updated-target"
	}
	for _, hpa := range list.Items {
		instance := hpa.Labels["instance"]
		if instance != "explicit" && instance != "omitted" {
			return fmt.Errorf("unknown HPA instance %q", instance)
		}
		if hpa.UID == "" || hpa.Namespace != "default" || hpa.Annotations["example.com/stage"] != phase {
			return fmt.Errorf("HPA metadata differs")
		}
		s := hpa.Spec
		if s.MaxReplicas != max || s.MinReplicas == nil || *s.MinReplicas != min ||
			s.ScaleTargetRef.Kind != "Deployment" || s.ScaleTargetRef.APIVersion != "apps/v1" ||
			s.ScaleTargetRef.Name != target {
			return fmt.Errorf("HPA replica or target values differ")
		}
		wantCPU := cpu
		if instance == "omitted" {
			wantCPU = 80
		}
		if len(s.Metrics) != 1 || s.Metrics[0].Type != "Resource" || s.Metrics[0].Resource == nil ||
			s.Metrics[0].Resource.Name != "cpu" || s.Metrics[0].Resource.Target.Type != "Utilization" ||
			s.Metrics[0].Resource.Target.AverageUtilization == nil ||
			*s.Metrics[0].Resource.Target.AverageUtilization != wantCPU {
			return fmt.Errorf("HPA metric values differ")
		}
		if instance == "explicit" {
			if s.Behavior == nil || s.Behavior.ScaleUp == nil ||
				s.Behavior.ScaleUp.SelectPolicy == nil || *s.Behavior.ScaleUp.SelectPolicy != "Max" ||
				s.Behavior.ScaleUp.StabilizationWindowSeconds == nil ||
				*s.Behavior.ScaleUp.StabilizationWindowSeconds != window || len(
				s.Behavior.ScaleUp.Policies) != 1 || s.Behavior.ScaleUp.Policies[0].Type != "Pods" ||
				s.Behavior.ScaleUp.Policies[0].PeriodSeconds != 15 ||
				s.Behavior.ScaleUp.Policies[0].Value != value {
				return fmt.Errorf("HPA behavior values differ")
			}
		}
		path := filepath.Join(dir, "hpa-"+instance+"-uid")
		if phase == "applied" {
			if err := os.WriteFile(path, []byte(hpa.UID), 0o600); err != nil {
				return err
			}
		} else {
			uid, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(uid) != string(hpa.UID) {
				return fmt.Errorf("HPA UID changed")
			}
		}
	}
	fmt.Printf("ok: HPAs match %s API values\n", phase)
	return nil
}
