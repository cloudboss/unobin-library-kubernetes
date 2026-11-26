package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const namespace = "unobin-it-restart"
const marker = "kubectl.kubernetes.io/restartedAt"

var workloads = map[string]string{
	"restart-deployment": "Deployment", "restart-daemonset": "DaemonSet",
	"restart-statefulset": "StatefulSet", "restart-partition": "StatefulSet",
	"restart-ondelete": "StatefulSet",
}

type identity struct {
	Controllers map[string]string            `json:"controllers"`
	Generations map[string]int64             `json:"generations"`
	Pods        map[string]map[string]string `json:"pods"`
}

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	build, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		return wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				_, err := api.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				return false, err
			})
	}
	apiOnly := os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1"
	stamp := "2026-11-26T12:00:00Z"
	if phase == "updated" {
		stamp = "2026-11-27T12:00:00Z"
	}
	before, err := snapshot(ctx, api, stamp, apiOnly)
	if err != nil {
		return err
	}
	savedPath := filepath.Join(build, "restart-identities.json")
	if phase == "updated" {
		body, err := os.ReadFile(savedPath)
		if err != nil {
			return err
		}
		var previous identity
		if err := json.Unmarshal(body, &previous); err != nil {
			return err
		}
		if !reflect.DeepEqual(previous.Controllers, before.Controllers) {
			return fmt.Errorf("restart replaced a controller: before %v, after %v",
				previous.Controllers, before.Controllers)
		}
		if !apiOnly {
			if err := checkRestarted(previous, before); err != nil {
				return err
			}
		}
	}
	config := "config.ub"
	if phase == "updated" {
		config = "update/config.ub"
	}
	plan, err := planFactory(ctx, build, config, "unchanged-"+phase+".ubp")
	if err != nil {
		return err
	}
	for _, step := range plan.Steps {
		if step.Kind == runtime.NodeResource && step.Decision != runtime.DecisionNoOp {
			return fmt.Errorf("post-restart %s decision is %s", step.Address, step.Decision)
		}
		if step.Kind == runtime.NodeAction {
			if step.Decision != runtime.DecisionSkip {
				return fmt.Errorf("unchanged %s did not skip", step.Address)
			}
			if step.Address == "action.partition" {
				if step.PriorOutputs["partition"] != int64(1) ||
					step.PriorOutputs["selected-replicas"] != int64(1) {
					return fmt.Errorf("partition outputs differ: %v", step.PriorOutputs)
				}
			}
			if step.Address == "action.staged" && step.PriorOutputs["waited"] != false {
				return fmt.Errorf("OnDelete action claimed to wait")
			}
		}
	}
	if err := runFactory(ctx, build, "apply", "unchanged-"+phase+".ubp"); err != nil {
		return err
	}
	unchanged, err := snapshot(ctx, api, stamp, apiOnly)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, unchanged) {
		return fmt.Errorf("unchanged apply changed workload identity")
	}
	coexist := "config-coexist"
	if apiOnly {
		coexist = "config-api-coexist"
	}
	if phase == "updated" {
		coexist += "-update"
	}
	directory := "coexist-" + phase
	if err := os.MkdirAll(filepath.Join(build, directory), 0o700); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join("..", coexist+".ub"))
	if err != nil {
		return err
	}
	localConfig := filepath.Join(directory, "config.ub")
	if err := os.WriteFile(filepath.Join(build, localConfig), body, 0o600); err != nil {
		return err
	}
	if err := runFactory(ctx, build, "pin", "-c", localConfig); err != nil {
		return err
	}
	plan, err = planFactory(ctx, build, localConfig, "coexist-"+phase+".ubp")
	if err != nil {
		return err
	}
	changed := map[string]bool{}
	for _, step := range plan.Steps {
		if step.Kind != runtime.NodeResource {
			continue
		}
		if strings.HasPrefix(step.Address, "resource.") && step.Address != "resource.scope" &&
			step.Address != "resource.headless" {
			if step.Decision != runtime.DecisionUpdate {
				return fmt.Errorf("metadata update %s decision is %s", step.Address, step.Decision)
			}
			changed[step.Address] = true
		} else if step.Decision != runtime.DecisionNoOp {
			return fmt.Errorf("coexistence changed supporting resource %s", step.Address)
		}
	}
	expected := map[string]bool{"resource.deployment": true, "resource.daemon": true,
		"resource.stateful": true, "resource.partition": true, "resource.staged": true}
	if !maps.Equal(expected, changed) {
		return fmt.Errorf("coexistence updates differ: %v", changed)
	}
	if err := runFactory(ctx, build, "apply", "coexist-"+phase+".ubp"); err != nil {
		return err
	}
	after, err := snapshot(ctx, api, stamp, apiOnly)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("metadata update reversed marker or restarted Pods")
	}
	if phase == "applied" {
		body, err := json.Marshal(before)
		if err != nil {
			return err
		}
		if err := os.WriteFile(savedPath, body, 0o600); err != nil {
			return err
		}
	}
	if apiOnly {
		fmt.Println("ok: API marker acceptance and typed-resource coexistence; Pod rollout untested")
	} else {
		fmt.Println("ok: real rollout, stable controllers, Pod identities, " +
			"partition, OnDelete, and coexistence")
	}
	return nil
}

func runFactory(ctx context.Context, build string, args ...string) error {
	command := exec.CommandContext(ctx, filepath.Join(build, "apps-v1-restart"), args...)
	command.Dir = build
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("factory %v: %w", args, err)
	}
	return nil
}

func planFactory(ctx context.Context, build, configuration, name string) (*runtime.PlanFile,
	error) {
	if err := runFactory(ctx, build, "plan", "-c", configuration, "-o", name); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(build, name))
	if err != nil {
		return nil, err
	}
	return runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
}

func snapshot(ctx context.Context, api kubernetes.Interface, stamp string,
	apiOnly bool) (identity, error) {
	result := identity{Controllers: map[string]string{}, Generations: map[string]int64{},
		Pods: map[string]map[string]string{}}
	for name, kind := range workloads {
		var meta metav1.Object
		var annotations map[string]string
		expected := int32(1)
		switch kind {
		case "Deployment":
			obj, err := api.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return result, err
			}
			meta, annotations = obj, obj.Spec.Template.Annotations
		case "DaemonSet":
			obj, err := api.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return result, err
			}
			meta, annotations = obj, obj.Spec.Template.Annotations
			expected = obj.Status.DesiredNumberScheduled
		case "StatefulSet":
			obj, err := api.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return result, err
			}
			meta, annotations = obj, obj.Spec.Template.Annotations
			if obj.Spec.Replicas != nil {
				expected = *obj.Spec.Replicas
			}
			if name == "restart-ondelete" &&
				obj.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
				return result, fmt.Errorf("OnDelete strategy changed")
			}
		}
		if meta.GetUID() == "" || annotations[marker] != stamp {
			return result, fmt.Errorf("%s UID/annotation differs: %q %v", name, meta.GetUID(), annotations)
		}
		result.Controllers[name] = string(meta.GetUID())
		result.Generations[name] = meta.GetGeneration()
		if apiOnly {
			continue
		}
		var pods map[string]string
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				list, err := api.CoreV1().Pods(namespace).List(ctx,
					metav1.ListOptions{LabelSelector: "app=" + name})
				if err != nil {
					return false, err
				}
				pods = map[string]string{}
				for _, pod := range list.Items {
					if pod.DeletionTimestamp != nil {
						continue
					}
					ready := false
					for _, condition := range pod.Status.Conditions {
						if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
							ready = true
						}
					}
					if !ready || pod.Status.Phase != corev1.PodRunning {
						return false, nil
					}
					eligible := name != "restart-ondelete" && pod.Name != "restart-partition-0"
					if eligible && pod.Annotations[marker] != stamp {
						return false, nil
					}
					if pod.UID == "" {
						return false, fmt.Errorf("pod %s has no UID", pod.Name)
					}
					pods[pod.Name] = string(pod.UID)
				}
				return int32(len(pods)) == expected, nil
			})
		if err != nil {
			return result, fmt.Errorf("observe real Pods for %s: %w", name, err)
		}
		if expected <= 0 {
			return result, fmt.Errorf("%s has no eligible Pods to prove restart", name)
		}
		result.Pods[name] = pods
	}
	return result, nil
}

func checkRestarted(before, after identity) error {
	for name, current := range after.Pods {
		previous := before.Pods[name]
		if len(previous) == 0 || len(current) == 0 {
			return fmt.Errorf("missing baseline or current Pods for %s", name)
		}
		if name == "restart-ondelete" {
			if !maps.Equal(previous, current) {
				return fmt.Errorf("OnDelete staging replaced Pods")
			}
			continue
		}
		if name == "restart-partition" {
			if previous[name+"-0"] == "" || previous[name+"-0"] != current[name+"-0"] {
				return fmt.Errorf("partition restarted unselected ordinal zero")
			}
			if previous[name+"-1"] == "" || previous[name+"-1"] == current[name+"-1"] {
				return fmt.Errorf("partition did not restart selected ordinal one")
			}
			continue
		}
		for _, uid := range current {
			for _, old := range previous {
				if uid == old {
					return fmt.Errorf("%s retained eligible Pod UID %s", name, uid)
				}
			}
		}
	}
	return nil
}
