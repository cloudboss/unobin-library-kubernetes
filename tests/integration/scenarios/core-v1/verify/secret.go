package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifySecret() error {
	phase, buildDir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := api.CoreV1().Secrets("default").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{"test.unobin.cloudboss.io/scenario": "core-v1-secret"}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario Secrets: %w", err)
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("scenario Secrets remain after destroy")
		}
		fmt.Println("ok: scenario Secrets are gone")
		return nil
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("expected two scenario Secrets")
	}
	roles := map[string]bool{}
	for _, secret := range list.Items {
		role := secret.Labels["role"]
		if (role != "mutable" && role != "locked") || roles[role] {
			return fmt.Errorf("scenario Secret roles differ")
		}
		roles[role] = true
		prefix := "unobin-it-secret-" + role + "-"
		if !strings.HasPrefix(secret.Name, prefix) || secret.Name == prefix ||
			secret.GenerateName != prefix || secret.Namespace != "default" || secret.UID == "" {
			return fmt.Errorf("scenario Secret identity differs")
		}
		immutable := secret.Immutable != nil && *secret.Immutable
		if immutable != (role == "locked" || phase == "updated") {
			return fmt.Errorf("scenario Secret immutability differs")
		}
		text, binary := "first", []byte{0, 255}
		if phase == "updated" {
			text, binary = "updated", []byte{2, 3}
		}
		if !maps.EqualFunc(secret.Data, map[string][]byte{
			"value": []byte(text), "bytes": binary, "empty": {},
		}, bytes.Equal) {
			return fmt.Errorf("scenario Secret payload differs")
		}
		uidPath := filepath.Join(buildDir, "secret-"+role+"-uid")
		if phase == "applied" {
			if err := os.WriteFile(uidPath, []byte(secret.UID), 0o600); err != nil {
				return err
			}
		} else {
			before, err := os.ReadFile(uidPath)
			if err != nil {
				return err
			}
			if len(before) == 0 || (string(before) == string(secret.UID)) != (role == "mutable") {
				return fmt.Errorf("scenario Secret update identity differs")
			}
		}
	}
	fmt.Println("ok: scenario Secrets match the configuration")
	return nil
}
