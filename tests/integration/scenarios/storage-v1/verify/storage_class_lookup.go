package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	name   = "unobin-it-storage-class-info"
	absent = name + "-absent"
)

func verifyStorageClassLookup() error {
	dir, stage := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	classes := api.StorageV1().StorageClasses()
	_, err = classes.Get(ctx, absent, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected missing StorageClass %s, got %v", absent, err)
	}
	class, err := classes.Get(ctx, name, metav1.GetOptions{})
	if stage == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected deleted StorageClass %s, got %v", name, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if class.UID == "" || class.ResourceVersion == "" ||
		ptr.Deref(class.ReclaimPolicy, "") != corev1.PersistentVolumeReclaimDelete ||
		ptr.Deref(class.VolumeBindingMode, "") != storagev1.VolumeBindingImmediate {
		return fmt.Errorf("missing StorageClass identity or API defaults")
	}

	config, plan, fallback := "config.ub", "plan.ubp", "first"
	switch stage {
	case "applied":
	case "updated":
		config, plan, fallback = "update/config.ub", "plan-update.ubp", "updated"
	default:
		return fmt.Errorf("unexpected VERIFY_PHASE %q", stage)
	}
	if err := verifyPlan(filepath.Join(dir, plan), stage); err != nil {
		return err
	}
	outputs, err := output(ctx, dir, config)
	if err != nil {
		return err
	}
	if want := expectedOutputs(class, fallback); !reflect.DeepEqual(want, outputs) {
		return fmt.Errorf("lookup outputs differ\n got: %#v\nwant: %#v", outputs, want)
	}
	fmt.Printf("ok: full StorageClass lookup outputs after %s\n", stage)
	return nil
}

func output(ctx context.Context, dir, config string) (map[string]any, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "storage-v1"), "output", "-c", config,
		"--format", "json")
	cmd.Dir = dir
	body, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read factory outputs: %w", err)
	}
	var result struct {
		Outputs struct {
			StorageClassLookup map[string]any `json:"storage-class-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode factory outputs: %w", err)
	}
	return result.Outputs.StorageClassLookup, nil
}

func verifyPlan(path, stage string) error {
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
	steps := map[string]runtime.PlanStep{}
	for _, step := range plan.Steps {
		steps[step.Address] = step
	}
	resource, hit, miss := steps["resource.storage-class-lookup-class"], steps["data-source.storage-class-lookup-hit"], steps["data-source.storage-class-lookup-miss"]
	if hit.Decision != runtime.DecisionRead || miss.Decision != runtime.DecisionRead ||
		miss.ObservedOutputs["provisioner"] != nil ||
		miss.ObservedOutputs["reclaim-policy"] != "Fallback" {
		return fmt.Errorf("unexpected StorageClass lookup steps after %s", stage)
	}
	if stage == "applied" {
		if resource.Decision != runtime.DecisionCreate || hit.ObservedOutputs != nil ||
			!reflect.DeepEqual(hit.UnresolvedInputs, map[string][]string{
				"metadata": {"resource.storage-class-lookup-class.name"},
			}) {
			return fmt.Errorf("initial hit did not defer for the StorageClass creation")
		}
		return nil
	}
	if resource.Decision != runtime.DecisionNoOp || hit.ObservedOutputs["reclaim-policy"] != "Delete" {
		return fmt.Errorf("steady plan did not read the existing StorageClass")
	}
	return nil
}

func expectedOutputs(class *storagev1.StorageClass, fallback string) map[string]any {
	topologies := func(key string) []any {
		return []any{map[string]any{
			"match-label-expressions": []any{map[string]any{
				"key": key, "values": []any{"east", "west"},
			}},
		}}
	}
	return map[string]any{
		"hit": map[string]any{
			"metadata": map[string]any{
				"name": name, "uid": string(class.UID), "resource-version": class.ResourceVersion,
				"generation":  float64(class.Generation),
				"labels":      map[string]any{"test.unobin.cloudboss.io/scenario": "storage-class-info"},
				"annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "full"},
			},
			"provisioner": "kubernetes.io/no-provisioner", "parameters": map[string]any{"tier": "fast"},
			"reclaim-policy": "Delete", "volume-binding-mode": "Immediate",
			"allow-volume-expansion": false, "mount-options": []any{"noexec", "nosuid"},
			"allowed-topologies": topologies("topology.kubernetes.io/zone"),
		},
		"miss": map[string]any{
			"metadata": map[string]any{
				"name": absent, "uid": "", "resource-version": "", "generation": float64(0),
				"labels":      map[string]any{"example.com/Lookup": ""},
				"annotations": map[string]any{"EXAMPLE.COM/Lookup": fallback},
			},
			"provisioner": nil, "parameters": map[string]any{"tier": "fallback"},
			"reclaim-policy": "Fallback", "volume-binding-mode": nil,
			"allow-volume-expansion": false, "mount-options": []any{"noexec", "nosuid"},
			"allowed-topologies": topologies(""),
		},
	}
}
