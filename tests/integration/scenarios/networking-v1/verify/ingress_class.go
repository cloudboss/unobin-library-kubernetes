package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	scenarioLabel        = "test.unobin.cloudboss.io/scenario"
	scenarioIngressClass = "networking-v1-ingress-class"
)

func verifyIngressClass() error {
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
	classes := api.NetworkingV1().IngressClasses()
	namePath := filepath.Join(buildDir, "ingress-class-name")
	uidPath := filepath.Join(buildDir, "ingress-class-uid")
	if stage == "destroyed" {
		name, err := os.ReadFile(namePath)
		if err != nil {
			return fmt.Errorf("read managed IngressClass name: %w", err)
		}
		_, err = classes.Get(ctx, string(name), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read IngressClass after destroy: %w", err)
		}
		return fmt.Errorf("IngressClass %s still exists after destroy", name)
	}
	list, err := classes.List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{scenarioLabel: scenarioIngressClass}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario IngressClasses: %w", err)
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("found %d IngressClasses, want 1", len(list.Items))
	}
	class := list.Items[0]
	prefix := "unobin-it-class-"
	if !strings.HasPrefix(class.Name, prefix) || class.Name == prefix ||
		class.GenerateName != prefix || class.Namespace != "" || class.UID == "" {
		return fmt.Errorf("IngressClass identity differs from the scenario")
	}
	paramName, revision := "initial", "first"
	if stage == "updated" {
		paramName, revision = "updated", "second"
	}
	want := networkingv1.IngressClassSpec{
		Controller: "example.com/ingress-controller",
		Parameters: &networkingv1.IngressClassParametersReference{
			APIGroup: new("example.com"), Kind: "IngressParameters", Name: paramName,
			Scope: new(networkingv1.IngressClassParametersReferenceScopeCluster),
		},
	}
	if !reflect.DeepEqual(class.Spec, want) {
		return fmt.Errorf("IngressClass spec differs from the %s configuration", stage)
	}
	if !maps.Equal(class.Labels, map[string]string{scenarioLabel: scenarioIngressClass, "revision": revision}) ||
		!maps.Equal(class.Annotations, map[string]string{"example.com/revision": revision}) {
		return fmt.Errorf("IngressClass metadata differs from the %s configuration", stage)
	}
	if stage == "applied" {
		if err := os.WriteFile(namePath, []byte(class.Name), 0o600); err != nil {
			return fmt.Errorf("save IngressClass name: %w", err)
		}
		if err := os.WriteFile(uidPath, []byte(class.UID), 0o600); err != nil {
			return fmt.Errorf("save IngressClass UID: %w", err)
		}
	} else {
		uid, err := os.ReadFile(uidPath)
		if err != nil {
			return fmt.Errorf("read IngressClass UID: %w", err)
		}
		if string(uid) != string(class.UID) {
			return fmt.Errorf("IngressClass UID changed after update")
		}
	}
	fmt.Printf("ok: IngressClass %s matches the %s configuration\n", class.Name, stage)
	return nil
}
