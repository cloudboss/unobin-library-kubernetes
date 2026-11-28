package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := verifyAPI(); err != nil {
		log.Fatal(err)
	}
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := verifyEvictionFactory(ctx); err != nil {
		return err
	}
	list, err := api.PolicyV1().PodDisruptionBudgets("default").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{
			"test.unobin.cloudboss.io/scenario": "policy-v1-pod-disruption-budget",
		}.String(),
	})
	if err != nil {
		return err
	}
	phase := os.Getenv("VERIFY_PHASE")
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found %d budgets after destroy", len(list.Items))
		}
		fmt.Println("ok: budgets are gone")
		return nil
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("found %d budgets, want 2", len(list.Items))
	}
	for _, budget := range list.Items {
		role := budget.Labels["role"]
		if role != "metadata" && role != "spec" {
			return fmt.Errorf("unexpected role %q", role)
		}
		if !strings.HasPrefix(budget.Name, budget.GenerateName) || budget.Name == budget.GenerateName {
			return fmt.Errorf("budget %q does not have a generated name", budget.Name)
		}
		expected := "1"
		if role == "metadata" {
			revision := "before"
			if phase == "updated" {
				revision = "after"
			}
			if budget.Labels["revision"] != revision {
				return fmt.Errorf("metadata revision differs")
			}
			if budget.Spec.Selector == nil || len(budget.Spec.Selector.MatchLabels) != 0 {
				return fmt.Errorf("expected nonnil empty selector")
			}
		} else {
			if phase == "updated" {
				expected = "2"
			}
			if budget.Spec.Selector == nil || budget.Spec.Selector.MatchLabels["app"] != "pdb-test" {
				return fmt.Errorf("selector differs")
			}
		}
		if budget.Spec.MinAvailable == nil || budget.Spec.MinAvailable.String() != expected {
			return fmt.Errorf("minimum differs for %s", role)
		}
		path := filepath.Join(os.Getenv("VERIFY_BUILD_DIR"), role+"-uid")
		switch phase {
		case "applied":
			if err := os.WriteFile(path, []byte(budget.UID), 0600); err != nil {
				return err
			}
		case "updated":
			before, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			same := string(before) == string(budget.UID)
			if (role == "metadata") != same {
				return fmt.Errorf("unexpected UID change for %s", role)
			}
		default:
			return fmt.Errorf("unknown verification phase %q", phase)
		}
	}
	fmt.Println("ok: budget identity, metadata, and specification match")
	return nil
}
