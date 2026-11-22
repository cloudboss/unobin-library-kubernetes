package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedv2 "k8s.io/client-go/kubernetes/typed/autoscaling/v2"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func (r *HorizontalPodAutoscalerResource) Create(ctx context.Context,
	c *config.Configuration) (*HorizontalPodAutoscalerResourceOutput, error) {
	ctx, cancel := hpaOperationContext(ctx)
	defer cancel()
	m := hpaMetadata(r.Object.Metadata)
	spec, err := expandHPASpec(r.Object.Spec)
	if err != nil {
		return nil, err
	}
	api, server, err := hpaClient(c, nil, m.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: m.Name, GenerateName: m.GenerateName,
			Namespace: m.Namespace, Annotations: m.Annotations, Labels: m.Labels}, Spec: spec},
		metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create HPA: %w", err)
	}
	m.Name, m.Namespace, m.UID = created.Name, created.Namespace, string(created.UID)
	saved := &HorizontalPodAutoscalerResourceOutput{Server: server, APIVersion: "autoscaling/v2",
		Kind: "HorizontalPodAutoscaler", Object: HPAObjectOutput{Metadata: m}}
	return r.refresh(ctx, c, saved, m)
}

func (r *HorizontalPodAutoscalerResource) Read(ctx context.Context, c *config.Configuration,
	p hpaPrior) (*HorizontalPodAutoscalerResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		if p.Outputs != nil {
			return p.Outputs, nil
		}
		return &HorizontalPodAutoscalerResourceOutput{}, nil
	}
	ctx, cancel := hpaOperationContext(ctx)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing HPA identity in prior state")
	}
	return r.refresh(ctx, c, p.Outputs, p.Outputs.Object.Metadata)
}

func (r *HorizontalPodAutoscalerResource) Update(ctx context.Context, c *config.Configuration,
	p hpaPrior) (*HorizontalPodAutoscalerResourceOutput, error) {
	ctx, cancel := hpaOperationContext(ctx)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing HPA identity in prior state")
	}
	if hpaIdentityChanged(r.Object.Metadata, p) {
		return nil, fmt.Errorf("HPA identity changed; replacement is required")
	}
	previous := p.Outputs.Object
	if p.Observed != nil {
		previous = p.Observed.Object
	}
	desired := hpaMetadata(r.Object.Metadata)
	operations := []hpaPatchOperation{{Op: "test", Path: "/metadata/uid",
		Value: p.Outputs.Object.Metadata.UID}}
	operations = append(operations, hpaMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, desired.Annotations)...)
	operations = append(operations, hpaMapPatch("/metadata/labels", previous.Metadata.Labels,
		desired.Labels)...)
	spec := r.Object.Spec
	add := func(path string, value any) {
		operations = append(operations, hpaPatchOperation{Op: "replace", Path: "/spec/" + path,
			Value: value})
	}
	if spec.MaxReplicas != previous.Spec.MaxReplicas {
		add("maxReplicas", spec.MaxReplicas)
	}
	if hpaMin(spec.MinReplicas) != hpaValue(previous.Spec.MinReplicas) {
		add("minReplicas", hpaMin(spec.MinReplicas))
	}
	ref := autoscalingv2.CrossVersionObjectReference{APIVersion: hpaValue(
		spec.ScaleTargetRef.APIVersion), Kind: spec.ScaleTargetRef.Kind, Name: spec.ScaleTargetRef.Name}
	oldRef := autoscalingv2.CrossVersionObjectReference{APIVersion: hpaValue(
		previous.Spec.ScaleTargetRef.APIVersion), Kind: previous.Spec.ScaleTargetRef.Kind,
		Name: previous.Spec.ScaleTargetRef.Name}
	if ref != oldRef {
		add("scaleTargetRef", ref)
	}
	if spec.Metric != nil {
		metrics, err := expandHPAMetrics(*spec.Metric)
		if err != nil {
			return nil, err
		}
		baseline, err := restoreHPAMetrics(hpaValue(previous.Spec.Metric))
		if err != nil {
			return nil, err
		}
		if !equalHPAJSON(metrics, baseline) {
			add("metrics", metrics)
		}
	}
	if spec.Behavior != nil {
		effective := *spec.Behavior
		if previous.Spec.Behavior != nil {
			if effective.ScaleUp == nil {
				effective.ScaleUp = previous.Spec.Behavior.ScaleUp
			}
			if effective.ScaleDown == nil {
				effective.ScaleDown = previous.Spec.Behavior.ScaleDown
			}
		}
		desiredBehavior := expandHPABehavior(&effective)
		// Remote nil pointer fields must remain absent when a computed direction is retained.
		if previous.Spec.Behavior != nil {
			if spec.Behavior.ScaleUp == nil {
				desiredBehavior.ScaleUp = restoreHPARules(previous.Spec.Behavior.ScaleUp)
			}
			if spec.Behavior.ScaleDown == nil {
				desiredBehavior.ScaleDown = restoreHPARules(previous.Spec.Behavior.ScaleDown)
			}
		}
		baseline := restoreHPABehavior(previous.Spec.Behavior)
		if !equalHPAJSON(desiredBehavior, baseline) {
			add("behavior", desiredBehavior)
		}
	}
	api, _, err := hpaClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode HPA patch: %w", err)
	}
	_, err = api.Patch(ctx, p.Outputs.Object.Metadata.Name, types.JSONPatchType, patch,
		metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch HPA: %w", err)
	}
	return r.refresh(ctx, c, p.Outputs, desired)
}

func restoreHPABehavior(b *HPABehavior) *autoscalingv2.HorizontalPodAutoscalerBehavior {
	if b == nil {
		return nil
	}
	return &autoscalingv2.HorizontalPodAutoscalerBehavior{ScaleUp: restoreHPARules(b.ScaleUp),
		ScaleDown: restoreHPARules(b.ScaleDown)}
}

func restoreHPARules(rules *[]HPARule) *autoscalingv2.HPAScalingRules {
	if rules == nil || len(*rules) == 0 {
		return nil
	}
	out := expandHPARules(rules)
	if (*rules)[0].SelectPolicy == nil {
		out.SelectPolicy = nil
	}
	if (*rules)[0].StabilizationWindowSeconds == nil {
		out.StabilizationWindowSeconds = nil
	}
	return out
}

func (r *HorizontalPodAutoscalerResource) Delete(ctx context.Context, c *config.Configuration,
	p hpaPrior) error {
	ctx, cancel := hpaOperationContext(ctx)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing HPA identity in prior state")
	}
	api, _, err := hpaClient(c, p.Outputs, "")
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.Object.Metadata.UID)
	err = api.Delete(ctx, p.Outputs.Object.Metadata.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete HPA: %w", err)
	}
	return nil
}

func hpaClient(c *config.Configuration, saved *HorizontalPodAutoscalerResourceOutput,
	namespace string) (typedv2.HorizontalPodAutoscalerInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		m := saved.Object.Metadata
		if saved.Server == "" || saved.APIVersion != "autoscaling/v2" ||
			saved.Kind != "HorizontalPodAutoscaler" || m.Name == "" || m.Namespace == "" || m.UID == "" {
			return nil, "", fmt.Errorf("missing HPA identity in prior state")
		}
		current.Server, namespace = &saved.Server, m.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedv2.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create HPA client: %w", err)
	}
	return api.HorizontalPodAutoscalers(namespace), resolved.Host, nil
}

func (r *HorizontalPodAutoscalerResource) refresh(ctx context.Context, c *config.Configuration,
	saved *HorizontalPodAutoscalerResourceOutput, tracked HPAMetadataOutput) (
	*HorizontalPodAutoscalerResourceOutput, error) {
	api, _, err := hpaClient(c, saved, "")
	if err != nil {
		return nil, err
	}
	m := saved.Object.Metadata
	observed, err := api.Get(ctx, m.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read HPA: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read HPA: %w", err)
	}
	if err := object.CheckUID(object.Target{Name: m.Name, UID: m.UID}, observed); err != nil {
		return nil, err
	}
	annotations, err := projectHPAMetadata(observed.Annotations, tracked.Annotations,
		r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectHPAMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	spec, err := projectHPASpec(observed.Spec)
	if err != nil {
		return nil, err
	}
	return &HorizontalPodAutoscalerResourceOutput{Server: saved.Server,
		APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler", Object: HPAObjectOutput{
			Metadata: HPAMetadataOutput{Name: observed.Name, GenerateName: observed.GenerateName,
				Namespace: observed.Namespace, UID: string(observed.UID), Annotations: annotations,
				Labels: labels}, Spec: spec}}, nil
}

func equalHPAJSON(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

func hpaOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 20*time.Minute)
}
