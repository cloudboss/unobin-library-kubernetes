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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type identity struct {
	Name string
	UID  string
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	phase, build := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := filepath.Join(build, "service-account-identities.json")
	initial := map[string]identity{}
	if phase != "applied" {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &initial); err != nil {
			return err
		}
	}
	if phase == "destroyed" {
		for _, saved := range initial {
			if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 10*time.Second, true,
				func(ctx context.Context) (bool, error) {
					_, err := api.CoreV1().ServiceAccounts("default").Get(ctx, saved.Name, metav1.GetOptions{})
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return false, err
				}); err != nil {
				return fmt.Errorf("ServiceAccount %s remains: %w", saved.Name, err)
			}
		}
		fmt.Println("ok: scenario ServiceAccounts are gone")
		return nil
	}
	list, err := api.CoreV1().ServiceAccounts("default").List(ctx, metav1.ListOptions{
		LabelSelector: "test.unobin.cloudboss.io/scenario=core-v1-service-account",
	})
	if err != nil {
		return err
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("found %d ServiceAccounts, want 2", len(list.Items))
	}
	value := "first"
	if phase == "updated" {
		value = "updated"
	}
	identities := map[string]identity{}
	for _, account := range list.Items {
		role := account.Labels["role"]
		if role != "explicit" && role != "defaulted" {
			return fmt.Errorf("unexpected role %q", role)
		}
		if _, exists := identities[role]; exists {
			return fmt.Errorf("duplicate role %q", role)
		}
		if !strings.HasPrefix(account.Name, "unobin-it-service-account-") || account.UID == "" {
			return fmt.Errorf("invalid ServiceAccount identity")
		}
		if account.Labels["value"] != value || account.Annotations["example.com/value"] != value {
			return fmt.Errorf("ServiceAccount %s metadata differs", account.Name)
		}
		wantAutomount := role == "defaulted" || phase == "updated"
		if account.AutomountServiceAccountToken == nil ||
			*account.AutomountServiceAccountToken != wantAutomount {
			return fmt.Errorf("ServiceAccount %s automount differs", account.Name)
		}
		if !reflect.DeepEqual(account.ImagePullSecrets, []corev1.LocalObjectReference{{Name: value}}) ||
			!reflect.DeepEqual(account.Secrets, []corev1.ObjectReference{{Name: value}}) {
			return fmt.Errorf("ServiceAccount %s references differ", account.Name)
		}
		identities[role] = identity{Name: account.Name, UID: string(account.UID)}
	}
	if phase == "applied" {
		body, err := json.Marshal(identities)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
	} else if !reflect.DeepEqual(identities, initial) {
		return fmt.Errorf("ServiceAccount identities changed after update")
	}
	fmt.Printf("ok: ServiceAccounts match the %s configuration\n", phase)
	return nil
}
