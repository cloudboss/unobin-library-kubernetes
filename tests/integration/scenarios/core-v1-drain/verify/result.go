package main

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"

	core "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

func sortResults(out *core.DrainNodeActionOutput) {
	slices.SortFunc(out.DrainedPods, func(a, b core.DrainPodIdentity) int {
		return strings.Compare(a.Namespace+"/"+a.Name+"/"+a.UID, b.Namespace+"/"+b.Name+"/"+b.UID)
	})
	slices.SortFunc(out.SkippedPods, func(a, b core.DrainSkippedPod) int {
		return strings.Compare(a.Namespace+"/"+a.Name+"/"+a.UID, b.Namespace+"/"+b.Name+"/"+b.UID)
	})
}

func checkSkip(factory *factoryFixture) error {
	body, err := os.ReadFile(filepath.Join(factory.dir, "skip.ubp"))
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	for _, step := range plan.Steps {
		if step.Address == "action.drain" {
			if step.Decision != runtime.DecisionSkip {
				return fmt.Errorf("unchanged drain did not Skip")
			}
			return nil
		}
	}
	return fmt.Errorf("missing drain action in saved Skip plan")
}

func expectedSnapshot(ctx context.Context, api *kubernetes.Clientset, node *corev1.Node) (
	*core.DrainNodeActionOutput, error,
) {
	pods, err := api.CoreV1().Pods(metav1.NamespaceAll).List(ctx,
		metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		return nil, err
	}
	out := &core.DrainNodeActionOutput{
		DrainedPods: []core.DrainPodIdentity{}, SkippedPods: []core.DrainSkippedPod{},
	}
	for _, pod := range pods.Items {
		reason := ""
		if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
			reason = "mirror"
		} else if owner := metav1.GetControllerOf(&pod); owner != nil && owner.Kind == "DaemonSet" &&
			pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			reason = "daemon-set"
		}
		if reason != "" {
			out.SkippedPods = append(out.SkippedPods, core.DrainSkippedPod{
				Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID), Reason: reason,
			})
		} else {
			out.DrainedPods = append(out.DrainedPods, core.DrainPodIdentity{
				Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID),
			})
		}
	}
	sortResults(out)
	return out, nil
}
