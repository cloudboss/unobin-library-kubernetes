package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type identity struct {
	Name string
	UID  string
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	phase := os.Getenv("VERIFY_PHASE")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid phase %q", phase)
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	file := filepath.Join(dir, "quota-identities.json")
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	quotas := api.CoreV1().ResourceQuotas("default")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := quotas.List(ctx, metav1.ListOptions{LabelSelector: labels.Set{
		"test.unobin.cloudboss.io/scenario": "core-v1-resource-quota",
	}.String()})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("managed ResourceQuotas remain: %v", list.Items)
		}
		return nil
	}

	hard, priorityClass, scope := "3", "unobin-test-first", corev1.ResourceQuotaScopeNotTerminating
	if phase == "updated" {
		hard, priorityClass, scope = "5", "unobin-test-updated", corev1.ResourceQuotaScopeTerminating
	}
	explicitSpec := corev1.ResourceQuotaSpec{
		Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse(hard)},
		ScopeSelector: &corev1.ScopeSelector{
			MatchExpressions: []corev1.ScopedResourceSelectorRequirement{{
				ScopeName: corev1.ResourceQuotaScopePriorityClass,
				Operator:  corev1.ScopeSelectorOpIn,
				Values:    []string{priorityClass},
			}},
		},
	}
	generatedSpec := corev1.ResourceQuotaSpec{
		Hard:   corev1.ResourceList{corev1.ResourcePods: resource.MustParse("7")},
		Scopes: []corev1.ResourceQuotaScope{scope},
	}
	identities := map[string]identity{}
	for _, live := range list.Items {
		var spec corev1.ResourceQuotaSpec
		role := live.Labels["identity"]
		switch role {
		case "explicit":
			if live.Name != "unobin-it-quota-explicit" || live.GenerateName != "" {
				return fmt.Errorf("unexpected explicit quota identity %q", live.Name)
			}
			spec = explicitSpec
		case "generated":
			if !strings.HasPrefix(live.Name, "unobin-it-quota-") ||
				live.GenerateName != "unobin-it-quota-" {
				return fmt.Errorf("unexpected generated quota identity %q", live.Name)
			}
			spec = generatedSpec
		default:
			return fmt.Errorf("unexpected quota %q with role %q", live.Name, role)
		}
		if live.UID == "" || live.Namespace != "default" || identities[role] != (identity{}) {
			return fmt.Errorf("unexpected identity for quota %q", live.Name)
		}
		if !reflect.DeepEqual(live.Spec.Scopes, spec.Scopes) ||
			!reflect.DeepEqual(live.Spec.ScopeSelector, spec.ScopeSelector) ||
			!hasHardLimit(live.Spec.Hard, spec.Hard.Pods()) ||
			!hasHardLimit(live.Status.Hard, spec.Hard.Pods()) {
			return fmt.Errorf("quota %q spec or status hard differs: %+v %+v",
				live.Name, live.Spec, live.Status)
		}
		identities[role] = identity{Name: live.Name, UID: string(live.UID)}
	}
	if identities["explicit"] == (identity{}) || identities["generated"] == (identity{}) {
		return fmt.Errorf("expected explicit and generated quotas, got %v", identities)
	}
	if phase == "applied" {
		raw, err := json.Marshal(identities)
		if err != nil {
			return err
		}
		return os.WriteFile(file, raw, 0600)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var previous map[string]identity
	if err := json.Unmarshal(raw, &previous); err != nil {
		return err
	}
	if identities["explicit"] != previous["explicit"] ||
		identities["generated"].UID == previous["generated"].UID {
		return fmt.Errorf("expected explicit quota update and generated quota replacement: %v",
			identities)
	}
	_, err = quotas.Get(ctx, previous["generated"].Name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected old generated quota to be absent, got %v", err)
	}
	return nil
}

func hasHardLimit(hard corev1.ResourceList, wanted *resource.Quantity) bool {
	actual, ok := hard[corev1.ResourcePods]
	return len(hard) == 1 && ok && actual.Cmp(*wanted) == 0
}
