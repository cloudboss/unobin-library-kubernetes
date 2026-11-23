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
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/typed/scheduling/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	revisionKey = "test.unobin.cloudboss.io/revision"
)

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("unknown verification phase %q", phase)
	}
	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	scheduling, err := v1.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api := scheduling.PriorityClasses()
	for _, role := range []string{"mutable", "immutable"} {
		name := "unobin-it-priority-" + role
		class, err := api.Get(ctx, name, metav1.GetOptions{})
		if phase == "destroyed" {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("expected PriorityClass %s to be absent after destroy, got %v", name, err)
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := verifyAPI(phase, role, class); err != nil {
			return err
		}
		if err := verifyOutputs(ctx, dir, role, cfg.Host, class); err != nil {
			return err
		}
		if err := verifyUID(dir, phase, role, string(class.UID)); err != nil {
			return err
		}
	}
	fmt.Printf("ok: PriorityClasses match the %s configuration\n", phase)
	return nil
}

func verifyAPI(phase, role string, class *schedulingv1.PriorityClass) error {
	if class.Namespace != "" || class.UID == "" || class.GenerateName != "" {
		return fmt.Errorf("unexpected identity for PriorityClass %s", class.Name)
	}
	value, policy := int32(0), corev1.PreemptLowerPriority
	description, globalDefault := "", false
	var metadata map[string]string
	if role == "mutable" {
		value = -10
		revision := "updated"
		if phase == "applied" {
			revision, description = "initial", "initial description"
			globalDefault = os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1"
		}
		metadata = map[string]string{revisionKey: revision}
	} else if phase == "updated" {
		value, policy = -5, corev1.PreemptNever
	}
	if class.Value != value || ptr.Deref(class.PreemptionPolicy, "") != policy ||
		class.Description != description || class.GlobalDefault != globalDefault ||
		!maps.Equal(class.Annotations, metadata) || !maps.Equal(class.Labels, metadata) {
		return fmt.Errorf("unexpected %s PriorityClass fields for %s: %+v", role, phase, class)
	}
	return nil
}

func verifyOutputs(
	ctx context.Context, dir, role, server string, class *schedulingv1.PriorityClass,
) error {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "scheduling-v1"),
		"state", "show", "resource."+role, "--format", "json", "-c", "config.ub")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("read PriorityClass outputs: %w: %s", err, exit.Stderr)
		}
		return err
	}
	var result struct {
		Entry struct {
			Outputs map[string]any `json:"outputs"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	toObject := func(values map[string]string) map[string]any {
		out := make(map[string]any, len(values))
		for key, value := range values {
			out[key] = value
		}
		return out
	}
	want := map[string]any{
		"server": server, "api-version": "scheduling.k8s.io/v1", "kind": "PriorityClass",
		"metadata": map[string]any{
			"name": class.Name, "generate-name": "", "uid": string(class.UID),
			"annotations": toObject(class.Annotations), "labels": toObject(class.Labels),
		},
		"value": float64(class.Value), "description": class.Description,
		"global-default": class.GlobalDefault, "preemption-policy": string(*class.PreemptionPolicy),
	}
	if !reflect.DeepEqual(want, result.Entry.Outputs) {
		return fmt.Errorf("unexpected %s PriorityClass outputs: %v", role, result.Entry.Outputs)
	}
	return nil
}

func verifyUID(dir, phase, role, uid string) error {
	file := filepath.Join(dir, role+"-uid")
	if phase == "applied" {
		return os.WriteFile(file, []byte(uid), 0o600)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if (string(before) == uid) != (role == "mutable") {
		return fmt.Errorf("unexpected UID transition for %s PriorityClass", role)
	}
	return nil
}
