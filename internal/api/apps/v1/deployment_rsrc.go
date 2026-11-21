package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type DeploymentResource struct {
	Metadata          Metadata
	Spec              DeploymentSpec
	WaitForRollout    *bool
	CreateTimeout     *string
	UpdateTimeout     *string
	DeleteTimeout     *string
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type DeploymentResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   Metadata
	Spec       DeploymentSpec
}

type prior = runtime.Prior[DeploymentResource, *DeploymentResourceOutput, *config.Configuration]

func (r *DeploymentResource) Create(
	ctx context.Context, c *config.Configuration,
) (*DeploymentResourceOutput, error) {
	obj, err := r.expand(nil)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout, err := duration(r.CreateTimeout, "create-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	api, server, err := deploymentClient(c, nil, obj.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Deployment: %w", err)
	}
	target := object.Target{
		Server: server, APIVersion: "apps/v1", Kind: "Deployment",
		Name: created.GetName(), Namespace: created.GetNamespace(), UID: string(created.GetUID()),
	}
	if target.UID == "" || target.Name == "" {
		return nil, fmt.Errorf("created Deployment has no name or UID")
	}
	return r.finish(ctx, api, target)
}
func (r *DeploymentResource) Read(
	ctx context.Context, c *config.Configuration, p prior,
) (*DeploymentResourceOutput, error) {
	api, _, err := deploymentClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	obj, err := readDeployment(ctx, api, p.Outputs.target())
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p.Inputs.output(obj, p.Outputs.Server)
}
func (r *DeploymentResource) Update(
	ctx context.Context, c *config.Configuration, p prior,
) (*DeploymentResourceOutput, error) {
	if _, err := r.expand(nil); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Outputs == nil || outerIdentity(r.Metadata) != outerIdentity(p.Inputs.Metadata) {
		return nil, fmt.Errorf("deployment identity changed; replacement is required")
	}
	timeout, err := duration(r.UpdateTimeout, "update-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := p.Outputs.target()
	api, _, err := deploymentClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	live, err := readDeployment(ctx, api, target)
	if err != nil {
		return nil, err
	}
	observed, err := p.Inputs.output(live, target.Server)
	if err != nil {
		return nil, err
	}
	desired, err := r.expand(&observed.Spec)
	if err != nil {
		return nil, err
	}
	patch := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	patch = append(patch, mapPatch("/metadata/labels", observed.Metadata.Labels, r.Metadata.Labels)...)
	patch = append(patch,
		mapPatch("/metadata/annotations", observed.Metadata.Annotations, r.Metadata.Annotations)...)
	projection, err := normalizedSpec(desired.Spec)
	if err != nil {
		return nil, err
	}
	if !equivalentSpec(p.Inputs.Spec, r.Spec) || !equivalentObservedSpec(projection, observed.Spec) {
		patch = append(patch, patchOperation{Op: "replace", Path: "/spec", Value: desired.Spec})
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	if _, err := api.Patch(ctx, target.Name, types.JSONPatchType,
		body, metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch Deployment: %w", err)
	}
	return r.finish(ctx, api, target)
}
func (r *DeploymentResource) Delete(ctx context.Context, c *config.Configuration, p prior) error {
	if p.Outputs == nil || p.Outputs.APIVersion != "apps/v1" || p.Outputs.Kind != "Deployment" {
		return fmt.Errorf("missing Deployment identity in prior state")
	}
	timeout, err := duration(p.Inputs.DeleteTimeout, "delete-timeout")
	if err != nil {
		return err
	}
	return object.Delete(ctx, c, p.Outputs.target(), metav1.DeleteOptions{}, timeout)
}

func (o *DeploymentResourceOutput) target() object.Target {
	if o == nil {
		return object.Target{}
	}
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}

func readDeployment(
	ctx context.Context, api typedappsv1.DeploymentInterface, target object.Target,
) (*appsv1.Deployment, error) {
	value, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read Deployment: %w", err)
	}
	if err := object.CheckUID(target, value); err != nil {
		return nil, err
	}
	return value, nil
}

func (r *DeploymentResource) finish(
	ctx context.Context, api typedappsv1.DeploymentInterface, target object.Target,
) (*DeploymentResourceOutput, error) {
	if r.WaitForRollout == nil || *r.WaitForRollout {
		if err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				obj, err := readDeployment(ctx, api, target)
				if err != nil {
					return false, err
				}
				return rolloutReady(obj)
			}); err != nil {
			return nil, fmt.Errorf("wait for Deployment rollout: %w", err)
		}
	}
	obj, err := readDeployment(ctx, api, target)
	if err != nil {
		return nil, err
	}
	return r.output(obj, target.Server)
}

func deploymentClient(
	c *config.Configuration, saved *DeploymentResourceOutput, namespace string,
) (typedappsv1.DeploymentInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "apps/v1" || saved.Kind != "Deployment" ||
			saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing Deployment identity in prior state")
		}
		current.Server = &saved.Server
		namespace = saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	resolved.AcceptContentTypes = "application/json"
	api, err := typedappsv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Deployment client: %w", err)
	}
	return api.Deployments(namespace), resolved.Host, nil
}

func rolloutReady(obj *appsv1.Deployment) (bool, error) {
	if obj.Generation > obj.Status.ObservedGeneration {
		return false, nil
	}
	if obj.Generation < obj.Status.ObservedGeneration {
		return false, fmt.Errorf("observed Deployment generation exceeds generation")
	}
	for _, condition := range obj.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing {
			if condition.Reason == "ProgressDeadlineExceeded" {
				return false, fmt.Errorf("deployment exceeded its progress deadline")
			}
			break
		}
	}
	replicas := int32(1)
	if obj.Spec.Replicas != nil {
		replicas = *obj.Spec.Replicas
	}
	return obj.Status.UpdatedReplicas >= replicas &&
		obj.Status.Replicas <= obj.Status.UpdatedReplicas &&
		obj.Status.ReadyReplicas == obj.Status.Replicas &&
		obj.Status.AvailableReplicas >= obj.Status.UpdatedReplicas, nil
}

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func mapPatch(path string, before, after *map[string]string) []patchOperation {
	old, desired := map[string]string{}, map[string]string{}
	if before != nil {
		old = *before
	}
	if after != nil {
		desired = *after
	}
	if reflect.DeepEqual(old, desired) || len(old) == 0 && len(desired) == 0 {
		return nil
	}
	if len(old) == 0 {
		return []patchOperation{{Op: "add", Path: path, Value: desired}}
	}
	keys := make([]string, 0, len(old)+len(desired))
	for key := range old {
		keys = append(keys, key)
	}
	for key := range desired {
		if _, present := old[key]; !present {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	operations := []patchOperation{}
	for _, key := range keys {
		next, present := desired[key]
		previous, existed := old[key]
		if present && existed && next == previous {
			continue
		}
		op := "replace"
		if !present {
			op = "remove"
		} else if !existed {
			op = "add"
		}
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operation := patchOperation{Op: op, Path: path + "/" + pointer}
		if present {
			operation.Value = next
		}
		operations = append(operations, operation)
	}
	return operations
}
