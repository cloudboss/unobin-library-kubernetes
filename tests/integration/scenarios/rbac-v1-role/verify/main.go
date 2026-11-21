package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	scenarioName  = "rbac-v1-role"
	scenarioLabel = "test.unobin.cloudboss.io/scenario"
	namePrefix    = "unobin-it-role-"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	phase := os.Getenv("VERIFY_PHASE")
	switch phase {
	case "applied", "updated", "destroyed":
	default:
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed, got %q", phase)
	}
	buildDir := os.Getenv("VERIFY_BUILD_DIR")
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := api.RbacV1().Roles(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{scenarioLabel: scenarioName}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario Roles: %w", err)
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found %d scenario Roles after destroy, want 0", len(list.Items))
		}
		fmt.Println("ok: scenario Roles are gone")
		return nil
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("found %d scenario Roles, want 1", len(list.Items))
	}
	role := list.Items[0]
	if !strings.HasPrefix(role.Name, namePrefix) || role.Name == namePrefix {
		return fmt.Errorf("role name is %q, want generated prefix %q", role.Name, namePrefix)
	}
	if role.GenerateName != namePrefix || role.Namespace != metav1.NamespaceDefault {
		return fmt.Errorf("role namespace or generate-name differs from the scenario")
	}
	if role.UID == "" {
		return fmt.Errorf("role has no UID")
	}
	if len(role.Rules) != 1 {
		return fmt.Errorf("role has %d rules, want 1", len(role.Rules))
	}
	rule := role.Rules[0]
	verbs, names := []string{"get", "list"}, []string{"one"}
	if phase == "updated" {
		verbs, names = []string{"get", "list", "watch"}, []string{}
	}
	if !slices.Equal(rule.APIGroups, []string{""}) ||
		!slices.Equal(rule.Resources, []string{"pods"}) ||
		!slices.Equal(rule.Verbs, verbs) || !slices.Equal(rule.ResourceNames, names) ||
		len(rule.NonResourceURLs) != 0 {
		return fmt.Errorf("role permissions are %v, want pods verbs %v and names %v",
			rule, verbs, names)
	}
	uidPath := filepath.Join(buildDir, "role-uid")
	if phase == "applied" {
		if err := os.WriteFile(uidPath, []byte(role.UID), 0o600); err != nil {
			return fmt.Errorf("save initial Role UID: %w", err)
		}
	} else {
		initialUID, err := os.ReadFile(uidPath)
		if err != nil {
			return fmt.Errorf("read initial Role UID: %w", err)
		}
		if len(initialUID) == 0 || string(role.UID) != string(initialUID) {
			return fmt.Errorf("role UID changed after the permissions update")
		}
	}
	fmt.Printf("ok: Role %s matches the %s configuration\n", role.Name, phase)
	return nil
}
