package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	switch phase {
	case "applied", "updated", "destroyed":
	default:
		return fmt.Errorf("unknown Pod verification step %q", phase)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pods := api.CoreV1().Pods("default")
	list, err := pods.List(ctx, metav1.ListOptions{
		LabelSelector: "test.unobin.cloudboss.io/scenario=core-v1-pod",
	})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("scenario Pods remain after deletion: %v", list.Items)
		}
		return nil
	}
	current, err := verifyPods(list.Items, phase)
	if err != nil {
		return err
	}
	path := filepath.Join(os.Getenv("VERIFY_BUILD_DIR"), "pod-identities.json")
	if phase == "applied" {
		body, err := json.Marshal(current)
		if err != nil {
			return err
		}
		return os.WriteFile(path, body, 0o600)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var first map[string]identity
	if err := json.Unmarshal(body, &first); err != nil {
		return err
	}
	if first["stable"] != current["stable"] {
		return fmt.Errorf("metadata or deadline update changed the stable Pod identity")
	}
	if first["replacement"].UID == current["replacement"].UID {
		return fmt.Errorf("image update reused the old Pod UID")
	}
	_, err = pods.Get(ctx, first["replacement"].Name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("old replacement Pod should be gone, GET returned %v", err)
	}
	return nil
}

func verifyPods(pods []corev1.Pod, phase string) (map[string]identity, error) {
	epoch, image, deadline := "first", "registry.k8s.io/pause:3.9", int64(3600)
	if phase == "updated" {
		epoch, image, deadline = "updated", "registry.k8s.io/pause:3.10", 1800
	}
	seen := map[string]identity{}
	for _, pod := range pods {
		role := pod.Labels["role"]
		if _, exists := seen[role]; exists {
			return nil, fmt.Errorf("duplicate Pod for role %q", role)
		}
		prefix := "unobin-it-pod-stable-"
		switch role {
		case "stable":
			if pod.Labels["epoch"] != epoch || pod.Spec.ActiveDeadlineSeconds == nil ||
				*pod.Spec.ActiveDeadlineSeconds != deadline {
				return nil, fmt.Errorf("stable Pod differs from the %s inputs", phase)
			}
			if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") != "1" &&
				pod.Status.Phase != corev1.PodRunning {
				return nil, fmt.Errorf("stable Pod phase is %s, want Running", pod.Status.Phase)
			}
		case "replacement":
			prefix = "unobin-it-pod-replace-"
			if pod.Status.Phase != corev1.PodPending || len(pod.Spec.Containers) != 1 ||
				pod.Spec.Containers[0].Name != "app" || pod.Spec.Containers[0].Image != image {
				return nil, fmt.Errorf("replacement Pod phase or containers differ from %s", phase)
			}
		default:
			return nil, fmt.Errorf("unexpected Pod role %q", role)
		}
		if !strings.HasPrefix(pod.Name, prefix) || pod.GenerateName != prefix || pod.UID == "" {
			return nil, fmt.Errorf("invalid generated identity for Pod %s", pod.Name)
		}
		seen[role] = identity{pod.Name, string(pod.UID)}
	}
	if seen["stable"].UID == "" || seen["replacement"].UID == "" {
		return nil, fmt.Errorf("expected the stable and replacement Pods, found %v", seen)
	}
	return seen, nil
}
