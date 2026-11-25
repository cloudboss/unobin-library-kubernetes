package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type identityLimitRange struct {
	Name string
	UID  string
}

func verifyLimitRange() error {
	stage := os.Getenv("VERIFY_PHASE")
	if stage != "applied" && stage != "updated" && stage != "destroyed" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", stage)
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	list, err := api.CoreV1().LimitRanges("default").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{
			"test.unobin.cloudboss.io/scenario": "core-v1-limit-range",
		}.String(),
	})
	if err != nil {
		return err
	}
	if stage == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("managed LimitRanges remain: %v", list.Items)
		}
		return nil
	}

	var generated, explicit *corev1.LimitRange
	for i := range list.Items {
		item := &list.Items[i]
		switch item.Labels["identity"] {
		case "generated":
			if generated != nil || !strings.HasPrefix(item.Name, "unobin-it-limit-") ||
				item.GenerateName != "unobin-it-limit-" {
				return fmt.Errorf("unexpected generated identity: %v", item.ObjectMeta)
			}
			generated = item
		case "explicit":
			if explicit != nil || item.Name != "unobin-it-limit-explicit" {
				return fmt.Errorf("unexpected explicit identity: %v", item.ObjectMeta)
			}
			explicit = item
		default:
			return fmt.Errorf("unexpected labels: %v", item.Labels)
		}
		if item.UID == "" || item.Namespace != "default" {
			return fmt.Errorf("incomplete LimitRange identity: %v", item.ObjectMeta)
		}
	}
	if generated == nil || explicit == nil {
		return fmt.Errorf("expected generated and explicit LimitRanges, got %v", list.Items)
	}
	wantGenerated, wantExplicit := initialSpecs()
	if stage == "updated" {
		wantGenerated = []corev1.LimitRangeItem{
			{Type: corev1.LimitTypePod, Max: resources(corev1.ResourceCPU, "2")},
		}
		wantExplicit = nil
	}
	if generated.Annotations["example.com/policy"] != string(wantGenerated[0].Type) ||
		!apiequality.Semantic.DeepEqual(generated.Spec.Limits, wantGenerated) ||
		!apiequality.Semantic.DeepEqual(explicit.Spec.Limits, wantExplicit) {
		return fmt.Errorf("unexpected LimitRanges: generated %v, explicit %v", generated, explicit)
	}
	ids := map[string]identityLimitRange{
		"generated": {Name: generated.Name, UID: string(generated.UID)},
		"explicit":  {Name: explicit.Name, UID: string(explicit.UID)},
	}
	file := filepath.Join(dir, "limit-range-identities.json")
	if stage == "applied" {
		if err := checkNoOp(ctx, dir); err != nil {
			return err
		}
		data, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		return os.WriteFile(file, data, 0600)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var prior map[string]identityLimitRange
	if err := json.Unmarshal(data, &prior); err != nil {
		return err
	}
	if ids["generated"] != prior["generated"] || ids["explicit"] != prior["explicit"] {
		return fmt.Errorf("LimitRange identities changed after update: %v -> %v", prior, ids)
	}
	return nil
}

func initialSpecs() (generated, explicit []corev1.LimitRangeItem) {
	return []corev1.LimitRangeItem{{
			Type: corev1.LimitTypeContainer, Default: resources(corev1.ResourceCPU, "500m"),
			DefaultRequest: resources(corev1.ResourceCPU, "500m"), Max: resources(corev1.ResourceCPU, "1"),
		}}, []corev1.LimitRangeItem{{
			Type: corev1.LimitTypePod, Max: resources(corev1.ResourceMemory, "1Gi"),
		}}
}

func resources(name corev1.ResourceName, quantity string) corev1.ResourceList {
	return corev1.ResourceList{name: resource.MustParse(quantity)}
}

func checkNoOp(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "./core-v1-admission",
		"plan", "-c", "config.ub", "-o", "verify-noop.ubp")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("second LimitRange plan: %w: %s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "verify-noop.ubp"))
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(data, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	expected := map[string]runtime.Decision{
		"resource.limit-range-generated": runtime.DecisionNoOp, "resource.limit-range-explicit": runtime.DecisionNoOp,
	}
	for _, step := range plan.Steps {
		if !strings.HasPrefix(step.Address, "resource.limit-range-") {
			continue
		}
		if decision, ok := expected[step.Address]; !ok || decision != step.Decision {
			return fmt.Errorf("unexpected second plan decision %s: %s", step.Address, step.Decision)
		}
		delete(expected, step.Address)
	}
	if len(expected) != 0 {
		return fmt.Errorf("missing decisions in second plan: %v", expected)
	}
	return nil
}
