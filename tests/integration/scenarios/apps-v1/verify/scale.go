package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type scaleEndpoint interface {
	GetScale(context.Context, string, metav1.GetOptions) (*autoscalingv1.Scale, error)
	UpdateScale(context.Context, string, *autoscalingv1.Scale,
		metav1.UpdateOptions) (*autoscalingv1.Scale, error)
}

func scaleEndpointFor(api typedappsv1.AppsV1Interface, kind string) scaleEndpoint {
	switch kind {
	case "Deployment":
		return api.Deployments("default")
	case "StatefulSet":
		return api.StatefulSets("default")
	default:
		return api.ReplicaSets("default")
	}
}

func checkScaleAPI(ctx context.Context) (result error) {
	if _, err := deploymentAPI(ctx); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	for _, kind := range []string{"Deployment", "StatefulSet", "ReplicaSet"} {
		if err := checkScaleTarget(ctx, api.AppsV1(), kind); err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
	}
	return nil
}

func checkScaleTarget(ctx context.Context, api typedappsv1.AppsV1Interface,
	kind string) (result error) {
	name := "unobin-scale-" + strings.ToLower(rand.Text()[:10])
	labels := map[string]string{"app": name}
	meta := metav1.ObjectMeta{Name: name, Namespace: "default"}
	selector := &metav1.LabelSelector{MatchLabels: labels}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "registry.k8s.io/pause:3.9",
		}}}}
	var remove func(context.Context) error
	switch kind {
	case "Deployment":
		_, err := api.Deployments("default").Create(ctx, &appsv1.Deployment{ObjectMeta: meta,
			Spec: appsv1.DeploymentSpec{Replicas: new(int32(1)), Paused: true,
				Selector: selector, Template: template}}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		remove = func(c context.Context) error {
			return api.Deployments("default").Delete(c, name, metav1.DeleteOptions{})
		}
	case "StatefulSet":
		_, err := api.StatefulSets("default").Create(ctx, &appsv1.StatefulSet{ObjectMeta: meta,
			Spec: appsv1.StatefulSetSpec{Replicas: new(int32(0)), ServiceName: name, Selector: selector,
				Template: template, UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: "OnDelete"}}},
			metav1.CreateOptions{})
		if err != nil {
			return err
		}
		remove = func(c context.Context) error {
			return api.StatefulSets("default").Delete(c, name, metav1.DeleteOptions{})
		}
	case "ReplicaSet":
		_, err := api.ReplicaSets("default").Create(ctx, &appsv1.ReplicaSet{ObjectMeta: meta,
			Spec: appsv1.ReplicaSetSpec{Replicas: new(int32(0)), Selector: selector, Template: template}},
			metav1.CreateOptions{})
		if err != nil {
			return err
		}
		remove = func(c context.Context) error {
			return api.ReplicaSets("default").Delete(c, name, metav1.DeleteOptions{})
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, remove(cleanup))
	}()
	resource := map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets",
		"ReplicaSet": "replicasets"}[kind]
	if err := api.RESTClient().Patch(types.MergePatchType).Namespace("default").
		Resource(resource).Name(name).SubResource("status").
		Body([]byte(`{"status":{"replicas":1}}`)).Do(ctx).Error(); err != nil {
		return err
	}
	endpoint := scaleEndpointFor(api, kind)
	before, err := endpoint.GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" && before.Status.Replicas != 1 {
		return fmt.Errorf("expected lagging observed replicas before scale")
	}
	a := &svc.ScaleAction{Kind: kind, Name: name, Namespace: new("default"),
		Replicas: before.Spec.Replicas + 1, ExpectedUID: new("wrong-uid"), Timeout: new("30s")}
	rejected, err := a.Run(ctx, nil)
	if err == nil || rejected != nil || !strings.Contains(err.Error(), "expected UID") {
		return fmt.Errorf("expected UID mismatch without output, got %+v: %v", rejected, err)
	}
	unchanged, err := endpoint.GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if before.UID != unchanged.UID || before.Spec.Replicas != unchanged.Spec.Replicas {
		return fmt.Errorf("UID mismatch changed target identity or desired replicas")
	}
	a.ExpectedUID = new(string(before.UID))
	a.Replicas = 0
	out, err := a.Run(ctx, nil)
	if err != nil {
		return err
	}
	accepted, err := endpoint.GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	resolved, err := client.Load(nil)
	if err != nil {
		return err
	}
	if out.Server != resolved.Host || out.APIVersion != "apps/v1" || out.Kind != kind ||
		out.Name != name || out.Namespace != "default" || out.UID != string(accepted.UID) ||
		out.ResourceVersion == "" || out.Replicas != 0 || accepted.Spec.Replicas != 0 {
		return fmt.Errorf("accepted scale output differs from API identity or desired replicas")
	}
	// Advance the parent version without creating Pods, then submit the stale Scale.
	patch := []byte(`{"metadata":{"labels":{"scale-check":"updated"}}}`)
	switch kind {
	case "Deployment":
		_, err = api.Deployments("default").Patch(ctx, name, types.MergePatchType,
			patch, metav1.PatchOptions{})
	case "StatefulSet":
		_, err = api.StatefulSets("default").Patch(ctx, name, types.MergePatchType,
			patch, metav1.PatchOptions{})
	case "ReplicaSet":
		_, err = api.ReplicaSets("default").Patch(ctx, name, types.MergePatchType,
			patch, metav1.PatchOptions{})
	}
	if err != nil {
		return err
	}
	accepted.Spec.Replicas = 1
	_, err = endpoint.UpdateScale(ctx, name, accepted, metav1.UpdateOptions{})
	if !apierrors.IsConflict(err) {
		return fmt.Errorf("stale Scale did not conflict: %w", err)
	}
	current, err := endpoint.GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.Spec.Replicas != 0 || current.UID != before.UID {
		return fmt.Errorf("conflict changed desired replicas or UID")
	}
	return nil
}

func verifyScale() error {
	if os.Getenv("VERIFY_PHASE") == "destroyed" {
		return nil
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	configuration := "config.ub"
	if os.Getenv("VERIFY_PHASE") == "updated" {
		configuration = "update/config.ub"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(dir, "apps-v1"), "output", "-c", configuration,
		"--format", "json")
	command.Dir = dir
	body, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read scale output: %w: %s", err, body)
	}
	var values struct {
		Outputs struct {
			Scale map[string]struct {
				Server          string `json:"server"`
				APIVersion      string `json:"api-version"`
				Kind            string `json:"kind"`
				Name            string `json:"name"`
				Namespace       string `json:"namespace"`
				UID             string `json:"uid"`
				ResourceVersion string `json:"resource-version"`
				Replicas        int32  `json:"replicas"`
			} `json:"scale"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &values); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	for key, kind := range map[string]string{"deployment": "Deployment",
		"stateful-set": "StatefulSet", "replica-set": "ReplicaSet"} {
		out, ok := values.Outputs.Scale[key]
		if !ok {
			return fmt.Errorf("missing scale output %s", key)
		}
		name := "unobin-it-scale-" + key
		live, err := scaleEndpointFor(api.AppsV1(), kind).GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if out.Kind != kind || out.APIVersion != "apps/v1" || out.Name != name ||
			out.Namespace != "default" || out.UID != string(live.UID) || out.ResourceVersion == "" ||
			out.Server == "" || out.Replicas != 0 || live.Spec.Replicas != 0 {
			return fmt.Errorf("compiled scale %s differs from API identity or replicas", key)
		}
	}
	return nil
}
