package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	core "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := verify(ctx); err != nil {
		log.Fatal(err)
	}
}

func verify(ctx context.Context) (result error) {
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	apiOnly := os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1"
	node, err := drainNode(ctx, api, apiOnly)
	if err != nil {
		return err
	}
	originalSchedulability := node.Spec.Unschedulable
	namespaces := []string{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, ns := range namespaces {
			result = errors.Join(result, api.AppsV1().ReplicaSets(ns).DeleteCollection(cleanup,
				metav1.DeleteOptions{}, metav1.ListOptions{}),
				api.AppsV1().DaemonSets(ns).DeleteCollection(cleanup,
					metav1.DeleteOptions{}, metav1.ListOptions{}),
				api.PolicyV1().PodDisruptionBudgets(ns).DeleteCollection(cleanup,
					metav1.DeleteOptions{}, metav1.ListOptions{}),
				api.CoreV1().Pods(ns).DeleteCollection(cleanup,
					metav1.DeleteOptions{GracePeriodSeconds: new(int64(0))}, metav1.ListOptions{}),
				api.CoreV1().Namespaces().Delete(cleanup, ns, metav1.DeleteOptions{}))
		}
		if apiOnly {
			result = errors.Join(result, api.CoreV1().Nodes().Delete(cleanup, node.Name,
				metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &node.UID}}))
		} else {
			live, err := api.CoreV1().Nodes().Get(cleanup, node.Name, metav1.GetOptions{})
			if err == nil && live.UID == node.UID {
				live.Spec.Unschedulable = originalSchedulability
				_, err = api.CoreV1().Nodes().Update(cleanup, live, metav1.UpdateOptions{})
			}
			result = errors.Join(result, err)
		}
	}()
	for range 2 {
		ns, err := api.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "a-unobin-drain-"}}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		namespaces = append(namespaces, ns.Name)
	}
	slices.Sort(namespaces)
	ns := namespaces[0]
	unmanaged, err := api.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "protected"}, Spec: podSpec(node.Name),
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	action := &core.DrainNodeAction{Name: node.Name, ExpectedUID: string(node.UID), Timeout: new("5s")}
	out, err := action.Run(ctx, nil)
	if err == nil || out != nil || !strings.Contains(err.Error(), "no controlling owner") {
		return fmt.Errorf("protected Pod was not refused: %+v %v", out, err)
	}
	if err := checkCordoned(ctx, api, node); err != nil {
		return err
	}
	if err := api.CoreV1().Pods(ns).Delete(ctx, unmanaged.Name,
		metav1.DeleteOptions{GracePeriodSeconds: new(int64(0))}); err != nil {
		return err
	}
	if err := waitGone(ctx, api, unmanaged); err != nil {
		return err
	}
	originals := []*corev1.Pod{}
	for _, namespace := range namespaces {
		pod, err := managedPod(ctx, api, namespace, node.Name, apiOnly)
		if err != nil {
			return err
		}
		originals = append(originals, pod)
	}
	skipped, err := retainedPods(ctx, api, ns, node.Name, apiOnly)
	if err != nil {
		return err
	}
	minimum := intstr.FromInt32(1)
	pdb, err := api.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "protect"}, Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minimum, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app": "drain-eligible"}}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	if apiOnly {
		// API-only coverage explicitly simulates the absent PDB controller's status.
		pdb.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: pdb.Generation,
			CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1, DisruptionsAllowed: 0}
		pdb, err = api.PolicyV1().PodDisruptionBudgets(ns).UpdateStatus(ctx, pdb, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	} else {
		err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				live, err := api.PolicyV1().PodDisruptionBudgets(ns).Get(ctx, pdb.Name, metav1.GetOptions{})
				return err == nil && live.Status.ObservedGeneration == live.Generation &&
					live.Status.DisruptionsAllowed == 0, err
			})
		if err != nil {
			return err
		}
	}
	action.Timeout = new("250ms")
	out, err = action.Run(ctx, nil)
	if out != nil || !errors.Is(err, context.DeadlineExceeded) ||
		(!apierrors.IsTooManyRequests(err) && !strings.Contains(err.Error(), "HTTP 429")) {
		return fmt.Errorf("PDB refusal did not exhaust one deadline: %+v %v", out, err)
	}
	if err := checkCordoned(ctx, api, node); err != nil {
		return err
	}
	canceled, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	action.Timeout = new("5s")
	out, err = action.Run(canceled, nil)
	timer.Stop()
	cancel()
	if out != nil || !errors.Is(err, context.Canceled) {
		return fmt.Errorf("drain refusal was not canceled: %+v %v", out, err)
	}
	pdb, err = api.PolicyV1().PodDisruptionBudgets(ns).Get(ctx, pdb.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	minimum = intstr.FromInt32(0)
	pdb.Spec.MinAvailable = &minimum
	pdb, err = api.PolicyV1().PodDisruptionBudgets(ns).Update(ctx, pdb, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	if apiOnly {
		// Explicitly simulate the controller granting one disruption after the policy change.
		pdb.Status = policyv1.PodDisruptionBudgetStatus{ObservedGeneration: pdb.Generation,
			CurrentHealthy: 1, DesiredHealthy: 0, ExpectedPods: 1, DisruptionsAllowed: 1}
		_, err = api.PolicyV1().PodDisruptionBudgets(ns).UpdateStatus(ctx, pdb, metav1.UpdateOptions{})
	} else {
		err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				live, err := api.PolicyV1().PodDisruptionBudgets(ns).Get(ctx, pdb.Name, metav1.GetOptions{})
				return err == nil && live.Status.ObservedGeneration == live.Generation &&
					live.Status.DisruptionsAllowed > 0, err
			})
	}
	if err != nil {
		return err
	}
	if err := compiledDrain(ctx, api, node, originals, skipped, apiOnly); err != nil {
		return err
	}
	if apiOnly {
		fmt.Println("ok: API protocol; Pod/PDB status and final termination explicitly simulated")
	} else {
		fmt.Println("ok: isolated kind Node; real controllers and kubelet completed original Pod UIDs")
	}
	return nil
}

func podSpec(node string) corev1.PodSpec {
	return corev1.PodSpec{NodeName: node, AutomountServiceAccountToken: new(false),
		TerminationGracePeriodSeconds: new(int64(1)),
		Containers: []corev1.Container{
			{Name: "app", Image: "registry.k8s.io/pause:3.9"},
		},
	}
}

func drainNode(ctx context.Context, api *kubernetes.Clientset, apiOnly bool) (*corev1.Node, error) {
	if apiOnly {
		return api.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			GenerateName: "a-unobin-drain-"}}, metav1.CreateOptions{})
	}
	nodes, err := api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if _, control := node.Labels["node-role.kubernetes.io/control-plane"]; !control {
			return node, nil
		}
	}
	if len(nodes.Items) > 0 {
		return &nodes.Items[0], nil
	}
	return nil, fmt.Errorf("dedicated drain kind scenario requires a disposable Node")
}

func checkCordoned(ctx context.Context, api *kubernetes.Clientset, node *corev1.Node) error {
	live, err := api.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.UID != node.UID || !live.Spec.Unschedulable {
		return fmt.Errorf("drain failed to retain the expected Node cordon")
	}
	return nil
}

func managedPod(ctx context.Context, api *kubernetes.Clientset, ns, node string, apiOnly bool) (
	*corev1.Pod, error,
) {
	labels := map[string]string{"app": "drain-eligible"}
	rs, err := api.AppsV1().ReplicaSets(ns).Create(ctx, &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "managed"},
		Spec: appsv1.ReplicaSetSpec{Replicas: new(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: podSpec(node)}}}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if !apiOnly {
		var ready *corev1.Pod
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				pods, err := api.CoreV1().Pods(ns).List(ctx,
					metav1.ListOptions{LabelSelector: "app=drain-eligible"})
				if err != nil {
					return false, err
				}
				for i := range pods.Items {
					for _, condition := range pods.Items[i].Status.Conditions {
						if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
							ready = &pods.Items[i]
							return true, nil
						}
					}
				}
				return false, nil
			})
		return ready, err
	}
	pod, err := api.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "eligible", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1",
			Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: new(true)}}}, Spec: podSpec(node)},
		metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	// No kubelet exists in this tier; readiness is an explicit verifier simulation.
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{
		Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
	return api.CoreV1().Pods(ns).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
}

func retainedPods(ctx context.Context, api *kubernetes.Clientset, ns, node string, apiOnly bool) (
	[]core.DrainSkippedPod, error,
) {
	labels := map[string]string{"app": "drain-daemon"}
	spec := podSpec(node)
	spec.NodeName = ""
	spec.NodeSelector = map[string]string{"kubernetes.io/hostname": node}
	spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	ds, err := api.AppsV1().DaemonSets(ns).Create(ctx, &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "retained"}, Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: spec}}}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	var daemon *corev1.Pod
	if apiOnly {
		daemon, err = api.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "daemon", OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "DaemonSet",
				Name: ds.Name, UID: ds.UID, Controller: new(true)}}}, Spec: podSpec(node),
		}, metav1.CreateOptions{})
	} else {
		err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				pods, err := api.CoreV1().Pods(ns).List(ctx,
					metav1.ListOptions{LabelSelector: "app=drain-daemon"})
				if err != nil {
					return false, err
				}
				for i := range pods.Items {
					if pods.Items[i].Spec.NodeName == node {
						daemon = &pods.Items[i]
						return true, nil
					}
				}
				return false, nil
			})
	}
	if err != nil {
		return nil, err
	}
	retained := []core.DrainSkippedPod{{Namespace: ns, Name: daemon.Name, UID: string(daemon.UID),
		Reason: "daemon-set"}}
	if !apiOnly {
		pods, err := api.CoreV1().Pods(metav1.NamespaceAll).List(ctx,
			metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
		if err != nil {
			return nil, err
		}
		for _, pod := range pods.Items {
			if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
				retained = append(retained, core.DrainSkippedPod{Namespace: pod.Namespace,
					Name: pod.Name, UID: string(pod.UID), Reason: "mirror"})
			}
		}
		return retained, nil
	}
	mirror, err := api.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "mirror", Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "verifier"}},
		Spec: podSpec(node)}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return append(retained, core.DrainSkippedPod{Namespace: ns, Name: mirror.Name,
		UID: string(mirror.UID), Reason: "mirror"}), nil
}

func waitGone(ctx context.Context, api *kubernetes.Clientset, pod *corev1.Pod) error {
	return wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			live, err := api.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return err == nil && live.UID != pod.UID, err
		})
}

func compiledDrain(ctx context.Context, api *kubernetes.Clientset, node *corev1.Node,
	originals []*corev1.Pod, skipped []core.DrainSkippedPod, apiOnly bool,
) (result error) {
	factory, err := compileFixture(ctx, "testdata/ub/drain-node", "drain-node")
	if err != nil {
		return err
	}
	defer factory.cleanup(&result)
	if err := factory.config("testdata/ub/drain-node/config.ub", "config.ub",
		strings.NewReplacer("DRAIN_NAME", node.Name, "DRAIN_UID", string(node.UID))); err != nil {
		return err
	}
	if err := factory.commands([]string{"plan", "-c", "config.ub", "-o", "run.ubp"}); err != nil {
		return err
	}
	simulate, cancel := context.WithCancel(ctx)
	completed := make(chan error, 1)
	if apiOnly {
		go func() {
			// The action only evicts. This verifier simulates the absent kubelet's final deletion.
			for _, pod := range originals {
				err := wait.PollUntilContextCancel(simulate, 25*time.Millisecond, true,
					func(ctx context.Context) (bool, error) {
						live, err := api.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
						if apierrors.IsNotFound(err) {
							return true, nil
						}
						if err != nil {
							return false, err
						}
						if live.UID != pod.UID {
							return true, nil
						}
						if live.DeletionTimestamp == nil {
							return false, nil
						}
						err = api.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
							GracePeriodSeconds: new(int64(0)), Preconditions: &metav1.Preconditions{UID: &pod.UID}})
						return err == nil, err
					})
				if err != nil {
					completed <- err
					return
				}
			}
			completed <- nil
		}()
	}
	expected, err := expectedSnapshot(ctx, api, node)
	if err != nil {
		cancel()
		if apiOnly {
			<-completed
		}
		return err
	}
	for _, fixture := range skipped {
		if !slices.Contains(expected.SkippedPods, fixture) {
			cancel()
			if apiOnly {
				<-completed
			}
			return fmt.Errorf("retained fixture absent from snapshot: %+v", fixture)
		}
	}
	factory.applied = true
	err = factory.commands([]string{"apply", "run.ubp"})
	cancel()
	if apiOnly {
		err = errors.Join(err, <-completed)
	}
	if err != nil {
		return err
	}
	body, err := factory.run("output", "--format", "json", "-c", "config.ub")
	if err != nil {
		return err
	}
	var envelope struct {
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	values, ok := envelope.Outputs["result"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing compiled drain output: %s", body)
	}
	var out core.DrainNodeActionOutput
	if err := runtime.Decode(&out, values); err != nil {
		return err
	}
	resolved, err := client.Load(nil)
	if err != nil {
		return err
	}
	want := core.DrainNodeActionOutput{Server: resolved.Host, APIVersion: "v1", Kind: "Node",
		Name: node.Name, UID: string(node.UID), Unschedulable: true, Drained: true,
		DrainedPods: expected.DrainedPods, SkippedPods: expected.SkippedPods}
	for _, pod := range originals {
		if err := waitGone(ctx, api, pod); err != nil {
			return err
		}
	}
	for _, pod := range expected.DrainedPods {
		live, err := api.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) && (err != nil || string(live.UID) == pod.UID) {
			return fmt.Errorf("snapshot original %s/%s UID %s remains: %v",
				pod.Namespace, pod.Name, pod.UID, err)
		}
	}
	if !reflect.DeepEqual(want, out) {
		return fmt.Errorf("drain output = %+v, want %+v", out, want)
	}
	for _, pod := range expected.SkippedPods {
		live, err := api.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(live.UID) != pod.UID || live.DeletionTimestamp != nil {
			return fmt.Errorf("drain changed retained %s/%s", pod.Namespace, pod.Name)
		}
	}
	if err := factory.commands([]string{"plan", "-c", "config.ub", "-o", "skip.ubp"}); err != nil {
		return err
	}
	if err := checkSkip(factory); err != nil {
		return err
	}
	if err := factory.commands([]string{"apply", "skip.ubp"}); err != nil {
		return err
	}
	after, err := factory.run("output", "--format", "json", "-c", "config.ub")
	if err != nil {
		return err
	}
	var retained struct {
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(after, &retained); err != nil {
		return err
	}
	if !reflect.DeepEqual(envelope.Outputs, retained.Outputs) {
		return fmt.Errorf("skip changed the last successful drain output")
	}
	if err := factory.commands(
		[]string{"plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp"},
		[]string{"apply", "destroy.ubp"}); err != nil {
		return err
	}
	factory.applied = false
	return checkCordoned(ctx, api, node)
}
