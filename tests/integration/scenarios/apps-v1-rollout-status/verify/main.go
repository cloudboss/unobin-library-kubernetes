package main

import (
	"context"
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
	"github.com/cloudboss/unobin/pkg/state/local"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const namespace = "unobin-it-rollout-status"
const scenario = "apps-v1-rollout-status"

var targets = map[string]string{
	"deployment": "Deployment", "daemon": "DaemonSet", "stateful": "StatefulSet",
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
	if phase != "applied" && phase != "updated" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	if err := prepareWorkloads(ctx, api, phase); err != nil {
		return err
	}
	apiOnly := os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1"
	if apiOnly {
		if err := prepareStatus(ctx, api); err != nil {
			return err
		}
	}
	source, configuration := "config-observe.ub", "observe/config.ub"
	if phase == "updated" {
		source, configuration = "config-observe-update.ub", "observe-update/config.ub"
	}
	if err := copyConfig(build, source, configuration); err != nil {
		return err
	}
	if err := runFactory(ctx, build, "pin", "-c", configuration); err != nil {
		return err
	}
	plan, err := planFactory(ctx, build, configuration, "observe.ubp")
	if err != nil {
		return err
	}
	if err := checkDecisions(plan, runtime.DecisionRerun); err != nil {
		return err
	}
	if err := runFactory(ctx, build, "apply", "observe.ubp"); err != nil {
		return err
	}
	if !apiOnly {
		if err := checkPods(ctx, api, phase); err != nil {
			return err
		}
	}
	plan, err = planFactory(ctx, build, configuration, "unchanged.ubp")
	if err != nil {
		return err
	}
	if err := checkDecisions(plan, runtime.DecisionSkip); err != nil {
		return err
	}
	if err := checkOutputs(ctx, api, build, plan); err != nil {
		return err
	}
	if err := runFactory(ctx, build, "apply", "unchanged.ubp"); err != nil {
		return err
	}
	if apiOnly && phase == "updated" {
		if err := supersede(ctx, api, build); err != nil {
			return err
		}
	}
	if apiOnly {
		fmt.Println("ok: compiled rollout status and supersession with manually set API status; " +
			"controller completion untested")
	} else {
		fmt.Println("ok: compiled rollout status for external workloads and real ready Pods")
	}
	return nil
}

func podTemplate(name, phase string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name, "epoch": phase}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", Image: "registry.k8s.io/pause:3.9"},
		}},
	}
}

func prepareWorkloads(ctx context.Context, api kubernetes.Interface, phase string) error {
	apps := api.AppsV1()
	if phase == "updated" {
		deployment, err := apps.Deployments(namespace).Get(ctx, "status-deployment", metav1.GetOptions{})
		if err != nil {
			return err
		}
		deployment.Spec.Template.Labels["epoch"] = phase
		if _, err := apps.Deployments(namespace).Update(ctx, deployment,
			metav1.UpdateOptions{}); err != nil {
			return err
		}
		daemon, err := apps.DaemonSets(namespace).Get(ctx, "status-daemonset", metav1.GetOptions{})
		if err != nil {
			return err
		}
		daemon.Spec.Template.Labels["epoch"] = phase
		if _, err := apps.DaemonSets(namespace).Update(ctx, daemon, metav1.UpdateOptions{}); err != nil {
			return err
		}
		stateful, err := apps.StatefulSets(namespace).Get(ctx, "status-statefulset", metav1.GetOptions{})
		if err != nil {
			return err
		}
		stateful.Spec.Template.Labels["epoch"] = phase
		_, err = apps.StatefulSets(namespace).Update(ctx, stateful, metav1.UpdateOptions{})
		return err
	}
	_, err := api.CoreV1().Services(namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: namespace},
		Spec:       corev1.ServiceSpec{ClusterIP: "None", Ports: []corev1.ServicePort{{Port: 80}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: namespace}
	}
	selector := func(name string) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}
	}
	_, err = apps.Deployments(namespace).Create(ctx, &appsv1.Deployment{
		ObjectMeta: meta("status-deployment"), Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)), Selector: selector("status-deployment"),
			Template: podTemplate("status-deployment", phase),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	_, err = apps.DaemonSets(namespace).Create(ctx, &appsv1.DaemonSet{
		ObjectMeta: meta("status-daemonset"), Spec: appsv1.DaemonSetSpec{
			Selector: selector("status-daemonset"), Template: podTemplate("status-daemonset", phase),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	_, err = apps.StatefulSets(namespace).Create(ctx, &appsv1.StatefulSet{
		ObjectMeta: meta("status-statefulset"), Spec: appsv1.StatefulSetSpec{
			Replicas: new(int32(1)), ServiceName: "headless", Selector: selector("status-statefulset"),
			Template: podTemplate("status-statefulset", phase),
		},
	}, metav1.CreateOptions{})
	return err
}

func prepareStatus(ctx context.Context, api kubernetes.Interface) error {
	apps := api.AppsV1()
	deployment, err := apps.Deployments(namespace).Get(ctx, "status-deployment", metav1.GetOptions{})
	if err != nil {
		return err
	}
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation,
		Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if _, err := apps.Deployments(namespace).UpdateStatus(ctx, deployment,
		metav1.UpdateOptions{}); err != nil {
		return err
	}
	daemon, err := apps.DaemonSets(namespace).Get(ctx, "status-daemonset", metav1.GetOptions{})
	if err != nil {
		return err
	}
	daemon.Status = appsv1.DaemonSetStatus{ObservedGeneration: daemon.Generation,
		DesiredNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberReady: 1, NumberAvailable: 1}
	if _, err := apps.DaemonSets(namespace).UpdateStatus(ctx, daemon,
		metav1.UpdateOptions{}); err != nil {
		return err
	}
	stateful, err := apps.StatefulSets(namespace).Get(ctx, "status-statefulset", metav1.GetOptions{})
	if err != nil {
		return err
	}
	stateful.Status = appsv1.StatefulSetStatus{ObservedGeneration: stateful.Generation,
		Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
		CurrentRevision: "ready", UpdateRevision: "ready"}
	_, err = apps.StatefulSets(namespace).UpdateStatus(ctx, stateful, metav1.UpdateOptions{})
	return err
}

func copyConfig(build, source, name string) error {
	body, err := os.ReadFile(filepath.Join("..", source))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(build, name)), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(build, name), body, 0o600)
}

func runFactory(ctx context.Context, build string, args ...string) error {
	command := exec.CommandContext(ctx, filepath.Join(build, scenario), args...)
	command.Dir = build
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("factory %v: %w: %s", args, err, output)
	}
	return nil
}

func planFactory(ctx context.Context, build, configuration, name string) (
	*runtime.PlanFile, error,
) {
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

func checkDecisions(plan *runtime.PlanFile, decision runtime.Decision) error {
	seen := map[string]bool{}
	for _, step := range plan.Steps {
		if step.Kind == runtime.NodeResource && step.Decision != runtime.DecisionNoOp {
			return fmt.Errorf("observer changed %s: %s", step.Address, step.Decision)
		}
		if step.Kind != runtime.NodeAction {
			continue
		}
		if step.Decision != decision {
			return fmt.Errorf("%s decision %s, want %s", step.Address, step.Decision, decision)
		}
		seen[strings.Split(strings.TrimPrefix(step.Address, "action."), "[")[0]] = true
	}
	if !maps.Equal(seen, map[string]bool{"deployment": true, "daemon": true, "stateful": true}) {
		return fmt.Errorf("action instances differ: %v", seen)
	}
	return nil
}

func checkOutputs(ctx context.Context, api kubernetes.Interface, build string,
	plan *runtime.PlanFile) error {
	store, err := local.NewStore(filepath.Join(build, ".unobin/state"), plan.Factory.Name,
		plan.Stack, encrypters.Noop{})
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	resolved, err := client.Load(nil)
	if err != nil {
		return err
	}
	expected := map[string]any{}
	for label, kind := range targets {
		var meta metav1.Object
		var observed int64
		switch kind {
		case "Deployment":
			obj, err := api.AppsV1().Deployments(namespace).Get(ctx, "status-deployment",
				metav1.GetOptions{})
			if err != nil {
				return err
			}
			meta, observed = obj, obj.Status.ObservedGeneration
		case "DaemonSet":
			obj, err := api.AppsV1().DaemonSets(namespace).Get(ctx, "status-daemonset",
				metav1.GetOptions{})
			if err != nil {
				return err
			}
			meta, observed = obj, obj.Status.ObservedGeneration
		case "StatefulSet":
			obj, err := api.AppsV1().StatefulSets(namespace).Get(ctx, "status-statefulset",
				metav1.GetOptions{})
			if err != nil {
				return err
			}
			meta, observed = obj, obj.Status.ObservedGeneration
		}
		if meta.GetUID() == "" || observed < meta.GetGeneration() {
			return fmt.Errorf("%s has no stable observed identity", label)
		}
		want := map[string]any{
			"server": resolved.Host, "api-version": "apps/v1", "kind": kind,
			"name": meta.GetName(), "namespace": namespace, "uid": string(meta.GetUID()),
			"generation": meta.GetGeneration(), "observed-generation": observed,
		}
		expected[label] = want
		for _, step := range plan.Steps {
			if !strings.HasPrefix(step.Address, "action."+label+"[") {
				continue
			}
			entry := snapshot.Find(step.Address)
			if entry == nil || !reflect.DeepEqual(entry.Outputs, want) ||
				!reflect.DeepEqual(step.PriorOutputs, want) {
				return fmt.Errorf("%s saved output differs from %v", step.Address, want)
			}
		}
	}
	if !reflect.DeepEqual(snapshot.Outputs, expected) {
		return fmt.Errorf("factory output differs: got %v, want %v", snapshot.Outputs, expected)
	}
	return nil
}

func checkPods(ctx context.Context, api kubernetes.Interface, phase string) error {
	for _, name := range []string{"status-deployment", "status-daemonset", "status-statefulset"} {
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				pods, err := api.CoreV1().Pods(namespace).List(ctx,
					metav1.ListOptions{LabelSelector: "app=" + name})
				if err != nil {
					return false, err
				}
				active := map[string]bool{}
				for _, pod := range pods.Items {
					if pod.DeletionTimestamp != nil {
						continue
					}
					ready := false
					for _, condition := range pod.Status.Conditions {
						if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
							ready = true
						}
					}
					if !ready || pod.Status.Phase != corev1.PodRunning || pod.UID == "" ||
						pod.Labels["epoch"] != phase {
						return false, nil
					}
					active[pod.Name] = true
				}
				return len(active) > 0, nil
			})
		if err != nil {
			return fmt.Errorf("real Pods for %s: %w", name, err)
		}
	}
	return nil
}

func supersede(ctx context.Context, api kubernetes.Interface, build string) error {
	apps := api.AppsV1().Deployments(namespace)
	deployment, err := apps.Get(ctx, "status-deployment", metav1.GetOptions{})
	if err != nil {
		return err
	}
	deployment.Spec.Template.Labels["epoch"] = "pending"
	if _, err := apps.Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
		return err
	}
	if err := copyConfig(build, "config-superseded.ub", "superseded/config.ub"); err != nil {
		return err
	}
	if err := runFactory(ctx, build, "pin", "-c", "superseded/config.ub"); err != nil {
		return err
	}
	if _, err := planFactory(ctx, build, "superseded/config.ub", "superseded.ubp"); err != nil {
		return err
	}
	mutate, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for revision := 0; ; revision++ {
			select {
			case <-mutate.Done():
				done <- nil
				return
			case <-ticker.C:
				deployment, err := apps.Get(mutate, "status-deployment", metav1.GetOptions{})
				if err != nil {
					if mutate.Err() != nil {
						done <- nil
					} else {
						done <- err
					}
					return
				}
				deployment.Spec.Template.Labels["epoch"] = fmt.Sprint(revision)
				if _, err := apps.Update(mutate, deployment, metav1.UpdateOptions{}); err != nil {
					if mutate.Err() != nil {
						done <- nil
					} else {
						done <- err
					}
					return
				}
			}
		}
	}()
	err = runFactory(ctx, build, "apply", "superseded.ubp")
	stop()
	if updateErr := <-done; updateErr != nil {
		return updateErr
	}
	if err == nil || !strings.Contains(err.Error(), "target generation changed") {
		return fmt.Errorf("superseding generation did not fail pinned observation: %v", err)
	}
	return nil
}
