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

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	stage := os.Getenv("VERIFY_PHASE")
	if stage != "applied" && stage != "updated" && stage != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
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
	list, err := api.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{
			"test.unobin.cloudboss.io/scenario": "rbac-v1-cluster-role-binding",
		}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario ClusterRoleBindings: %w", err)
	}
	if stage == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found %d ClusterRoleBindings after destroy", len(list.Items))
		}
		fmt.Println("ok: scenario ClusterRoleBindings are gone")
		return nil
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("found %d ClusterRoleBindings, want 1", len(list.Items))
	}
	binding := list.Items[0]
	if !strings.HasPrefix(binding.Name, "unobin-it-binding-") || binding.UID == "" ||
		binding.GenerateName != "unobin-it-binding-" || binding.Namespace != "" {
		return fmt.Errorf("ClusterRoleBinding identity differs from the scenario")
	}
	reference := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"}
	if binding.RoleRef != reference {
		return fmt.Errorf("ClusterRoleBinding reference differs from the scenario")
	}
	user, account := "alice", "reader"
	if stage == "updated" {
		user, account = "bob", "reader-updated"
	}
	want := []rbacv1.Subject{
		{Kind: "User", Name: user, APIGroup: rbacv1.GroupName},
		{Kind: "Group", Name: "readers", APIGroup: rbacv1.GroupName},
		{Kind: "ServiceAccount", Name: account, Namespace: "default"},
		{Kind: "ServiceAccount", Name: account, Namespace: "default"},
	}
	if !slices.Equal(want, binding.Subjects) {
		return fmt.Errorf("ClusterRoleBinding subjects are %v, want %v", binding.Subjects, want)
	}
	uidPath := filepath.Join(buildDir, "cluster-role-binding-uid")
	if stage == "applied" {
		if err := os.WriteFile(uidPath, []byte(binding.UID), 0o600); err != nil {
			return fmt.Errorf("save ClusterRoleBinding UID: %w", err)
		}
	} else {
		uid, err := os.ReadFile(uidPath)
		if err != nil {
			return fmt.Errorf("read ClusterRoleBinding UID: %w", err)
		}
		if len(uid) == 0 || string(uid) != string(binding.UID) {
			return fmt.Errorf("ClusterRoleBinding UID changed after update")
		}
	}
	fmt.Printf("ok: ClusterRoleBinding %s matches the %s configuration\n", binding.Name, stage)
	return nil
}
