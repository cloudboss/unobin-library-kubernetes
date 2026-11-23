package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type identityService struct {
	Name    string
	UID     string
	Address string
}

func verifyService() error {
	phase := os.Getenv("VERIFY_PHASE")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("verify phase must be applied, updated, or destroyed, got %q", phase)
	}
	buildDir := os.Getenv("VERIFY_BUILD_DIR")
	if buildDir == "" {
		return fmt.Errorf("verify build directory is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := api.CoreV1().Services("default").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{"test.unobin.cloudboss.io/scenario": "core-v1-service"}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario Services: %w", err)
	}
	path := filepath.Join(buildDir, "service-identities.json")
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found %d Services after destroy, want 0", len(list.Items))
		}
		fmt.Println("ok: scenario Services are gone")
		return nil
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("found %d Services, want 2", len(list.Items))
	}
	current := map[string]identityService{}
	for _, service := range list.Items {
		role := service.Labels["role"]
		if role != "stable" && role != "replacement" {
			return fmt.Errorf("unexpected Service role %q", role)
		}
		if _, exists := current[role]; exists {
			return fmt.Errorf("duplicate Service role %q", role)
		}
		prefix := "unobin-it-service-stable-"
		if role == "replacement" {
			prefix = "unobin-it-service-first-"
			if phase == "updated" {
				prefix = "unobin-it-service-updated-"
			}
		}
		if !strings.HasPrefix(service.Name, prefix) || service.GenerateName != prefix ||
			service.UID == "" {
			return fmt.Errorf("service %s has an invalid generated identity", service.Name)
		}
		if service.Spec.Type != corev1.ServiceTypeClusterIP ||
			net.ParseIP(service.Spec.ClusterIP) == nil ||
			len(service.Spec.ClusterIPs) == 0 || service.Spec.ClusterIPs[0] != service.Spec.ClusterIP ||
			len(service.Spec.IPFamilies) == 0 || service.Spec.IPFamilyPolicy == nil {
			return fmt.Errorf("service %s lacks computed allocation or policy", service.Name)
		}
		if len(service.Spec.Ports) != 1 {
			return fmt.Errorf("service %s must have one port", service.Name)
		}
		if role == "stable" {
			value, target := "first", int32(8080)
			if phase == "updated" {
				value, target = "updated", 8081
			}
			if service.Spec.Selector["app"] != value || service.Spec.Ports[0].Port != 80 ||
				service.Spec.Ports[0].TargetPort != intstr.FromInt32(target) {
				return fmt.Errorf("stable Service differs from the %s configuration", phase)
			}
		} else if service.Spec.Ports[0].Port != 81 ||
			service.Spec.Ports[0].TargetPort != intstr.FromInt32(81) {
			return fmt.Errorf("replacement Service did not retain remote target-port defaulting")
		}
		current[role] = identityService{
			Name: service.Name, UID: string(service.UID), Address: service.Spec.ClusterIP,
		}
	}
	if phase == "applied" {
		body, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("encode Service identities: %w", err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return fmt.Errorf("save Service identities: %w", err)
		}
	} else {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Service identities: %w", err)
		}
		var previous map[string]identityService
		if err := json.Unmarshal(body, &previous); err != nil {
			return fmt.Errorf("decode Service identities: %w", err)
		}
		if current["stable"] != previous["stable"] {
			return fmt.Errorf("mutable update changed Service identity or address")
		}
		if current["replacement"].UID == previous["replacement"].UID {
			return fmt.Errorf("immutable generate-name update did not replace the Service")
		}
		_, err = api.CoreV1().Services("default").Get(ctx,
			previous["replacement"].Name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("old Service still exists or lookup failed: %v", err)
		}
	}
	fmt.Printf("ok: Services match the %s configuration\n", phase)
	return nil
}
