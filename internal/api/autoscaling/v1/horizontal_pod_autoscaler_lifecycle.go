package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedv1 "k8s.io/client-go/kubernetes/typed/autoscaling/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const (
	hpaAPIVersion       = "autoscaling/v1"
	hpaKind             = "HorizontalPodAutoscaler"
	hpaOperationTimeout = 20 * time.Minute
)

func (r *HorizontalPodAutoscalerResource) Create(
	ctx context.Context, c *config.Configuration,
) (*HorizontalPodAutoscalerResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, hpaOperationTimeout)
	defer cancel()
	m := r.Object.Metadata
	api, server, err := hpaClient(c, hpaNamespace(m.Namespace))
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &autoscalingv1.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name: hpaValue(m.Name), GenerateName: hpaValue(m.GenerateName),
			Namespace: hpaNamespace(m.Namespace), Annotations: hpaValue(m.Annotations),
			Labels: hpaValue(m.Labels),
		},
		Spec: expandHPASpec(r.Object.Spec),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create HPA: %w", err)
	}
	target := object.Target{
		Server: server, APIVersion: hpaAPIVersion, Kind: hpaKind,
		Name: created.Name, Namespace: created.Namespace, UID: string(created.UID),
	}
	return r.refresh(ctx, c, target)
}

func (r *HorizontalPodAutoscalerResource) Read(
	ctx context.Context, c *config.Configuration, p hpaPrior,
) (*HorizontalPodAutoscalerResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		return &HorizontalPodAutoscalerResourceOutput{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, hpaOperationTimeout)
	defer cancel()
	return p.Inputs.refresh(ctx, c, hpaTarget(p.Outputs))
}

func (r *HorizontalPodAutoscalerResource) Update(
	ctx context.Context, c *config.Configuration, p hpaPrior,
) (*HorizontalPodAutoscalerResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, hpaOperationTimeout)
	defer cancel()
	if hpaIdentityChanged(r.Object.Metadata, p) {
		return nil, fmt.Errorf("HPA identity changed; replacement is required")
	}
	target := hpaTarget(p.Outputs)
	api, err := hpaSavedClient(c, target)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs.Object
	if p.Observed != nil {
		previous = p.Observed.Object
	}
	if err := r.refreshPatchMetadata(ctx, api, target, p.Inputs, &previous.Metadata); err != nil {
		return nil, err
	}
	operations := r.patch(previous, target.UID)
	if len(operations) > 1 {
		body, err := json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode HPA patch: %w", err)
		}
		updated, err := api.Patch(ctx, target.Name, types.JSONPatchType, body, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch HPA: %w", err)
		}
		if err := object.CheckUID(target, updated); err != nil {
			return nil, err
		}
	}
	return r.refresh(ctx, c, target)
}

func (r *HorizontalPodAutoscalerResource) refreshPatchMetadata(
	ctx context.Context, api typedv1.HorizontalPodAutoscalerInterface, target object.Target,
	prior HorizontalPodAutoscalerResource, previous *HPAMetadataOutput,
) error {
	annotationsChanged := !slices.Equal(hpaValue(prior.IgnoreAnnotations),
		hpaValue(r.IgnoreAnnotations))
	labelsChanged := !slices.Equal(hpaValue(prior.IgnoreLabels), hpaValue(r.IgnoreLabels))
	if !annotationsChanged && !labelsChanged {
		return nil
	}
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read HPA metadata for update: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return err
	}
	before, after := prior.Object.Metadata, r.Object.Metadata
	if annotationsChanged {
		previous.Annotations = projectHPAMetadata(live.Annotations, r.IgnoreAnnotations,
			hpaValue(before.Annotations), hpaValue(after.Annotations))
	}
	if labelsChanged {
		previous.Labels = projectHPAMetadata(live.Labels, r.IgnoreLabels,
			hpaValue(before.Labels), hpaValue(after.Labels))
	}
	return nil
}

func (r *HorizontalPodAutoscalerResource) patch(
	previous HPAObjectOutput, uid string,
) []hpaPatchOperation {
	operations := []hpaPatchOperation{{Op: "test", Path: "/metadata/uid", Value: uid}}
	desired := r.Object.Metadata
	operations = append(operations, hpaMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, hpaValue(desired.Annotations))...)
	operations = append(operations, hpaMapPatch("/metadata/labels",
		previous.Metadata.Labels, hpaValue(desired.Labels))...)
	add := func(path string, value any) {
		operations = append(operations, hpaPatchOperation{
			Op: "replace", Path: "/spec/" + path, Value: value,
		})
	}
	before, after := previous.Spec, r.Object.Spec
	if after.MaxReplicas != before.MaxReplicas {
		add("maxReplicas", after.MaxReplicas)
	}
	if min := hpaMin(after.MinReplicas); min != hpaValue(before.MinReplicas) {
		add("minReplicas", min)
	}
	if ref := expandHPARef(after.ScaleTargetRef); ref != expandHPARef(before.ScaleTargetRef) {
		add("scaleTargetRef", ref)
	}
	if cpu := after.TargetCPUUtilizationPercentage; cpu != nil &&
		*cpu != hpaValue(before.TargetCPUUtilizationPercentage) {
		add("targetCPUUtilizationPercentage", *cpu)
	}
	return operations
}

func (*HorizontalPodAutoscalerResource) Delete(
	ctx context.Context, c *config.Configuration, p hpaPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, hpaOperationTimeout)
	defer cancel()
	target := hpaTarget(p.Outputs)
	api, err := hpaSavedClient(c, target)
	if err != nil {
		return err
	}
	err = api.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: new(types.UID(target.UID))},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete HPA: %w", err)
	}
	return nil
}

func (r *HorizontalPodAutoscalerResource) refresh(
	ctx context.Context, c *config.Configuration, target object.Target,
) (*HorizontalPodAutoscalerResourceOutput, error) {
	api, err := hpaSavedClient(c, target)
	if err != nil {
		return nil, err
	}
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read HPA: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read HPA: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return nil, err
	}
	return projectHPA(target.Server, live, r), nil
}

func hpaTarget(saved *HorizontalPodAutoscalerResourceOutput) object.Target {
	if saved == nil {
		return object.Target{}
	}
	m := saved.Object.Metadata
	return object.Target{
		Server: saved.Server, APIVersion: saved.APIVersion, Kind: saved.Kind,
		Name: m.Name, Namespace: m.Namespace, UID: m.UID,
	}
}

func hpaSavedClient(c *config.Configuration, target object.Target) (
	typedv1.HorizontalPodAutoscalerInterface, error,
) {
	if target.Server == "" || target.APIVersion != hpaAPIVersion || target.Kind != hpaKind ||
		target.Name == "" || target.Namespace == "" || target.UID == "" {
		return nil, fmt.Errorf("missing HPA identity in prior state")
	}
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	current.Server = &target.Server
	api, _, err := hpaClient(&current, target.Namespace)
	return api, err
}

func hpaClient(c *config.Configuration, namespace string) (
	typedv1.HorizontalPodAutoscalerInterface, string, error,
) {
	resolved, err := client.Load(c)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create HPA client: %w", err)
	}
	return api.HorizontalPodAutoscalers(namespace), resolved.Host, nil
}
