package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyClusterRole() error {
	stage := os.Getenv("VERIFY_PHASE")
	if stage != "applied" && stage != "updated" && stage != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	build := os.Getenv("VERIFY_BUILD_DIR")
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	roles, err := api.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{"test.unobin.cloudboss.io/scenario": "rbac-v1-cluster-role"}.String()})
	if err != nil {
		return err
	}
	if stage == "destroyed" {
		if len(roles.Items) != 0 {
			return fmt.Errorf("ClusterRoles remain after destroy")
		}
		return nil
	}
	if len(roles.Items) != 2 {
		return fmt.Errorf("found %d ClusterRoles, want two", len(roles.Items))
	}
	var reader, aggregate *rbacv1.ClusterRole
	for i := range roles.Items {
		role := &roles.Items[i]
		if strings.HasPrefix(role.Name, "unobin-it-cluster-reader-") {
			reader = role
		}
		if strings.HasPrefix(role.Name, "unobin-it-cluster-aggregate-") {
			aggregate = role
		}
	}
	if reader == nil || aggregate == nil || reader.Namespace != "" || aggregate.Namespace != "" {
		return fmt.Errorf("cluster role identities differ from scenario")
	}
	verb, name := "list", "one"
	if stage == "updated" {
		verb, name = "watch", "two"
	}
	want := []rbacv1.PolicyRule{{Verbs: []string{"get", verb, "get"}, APIGroups: []string{""},
		Resources: []string{"pods"}, ResourceNames: []string{name}}}
	if !reflect.DeepEqual(want, reader.Rules) {
		return fmt.Errorf("reader rules differ: %v", reader.Rules)
	}
	if aggregate.AggregationRule == nil {
		return fmt.Errorf("aggregation rule is missing")
	}
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1" {
		err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				live, err := api.RbacV1().ClusterRoles().Get(ctx, aggregate.Name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				return reflect.DeepEqual(want, live.Rules), nil
			})
		if err != nil {
			return fmt.Errorf("wait for aggregation controller: %w", err)
		}
	}
	path := filepath.Join(build, "cluster-role-uids")
	ids := map[string]string{"reader": string(reader.UID), "aggregate": string(aggregate.UID)}
	if ids["reader"] == "" || ids["aggregate"] == "" {
		return fmt.Errorf("missing role UID")
	}
	if stage == "applied" {
		body, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		return os.WriteFile(path, body, 0o600)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var prior map[string]string
	if err := json.Unmarshal(body, &prior); err != nil {
		return err
	}
	if !reflect.DeepEqual(ids, prior) {
		return fmt.Errorf("role UID changed during update")
	}
	return nil
}
