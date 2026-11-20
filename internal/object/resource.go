package object

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type ObjectResource struct {
	Manifest       string `ub:",sensitive"`
	FieldManager   *string
	ForceConflicts *bool
	DeleteTimeout  *string
}

type ObjectResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Fields     string         `ub:",sensitive"`
	Values     map[string]any `ub:",sensitive"`
}

type prior = runtime.Prior[ObjectResource, *ObjectResourceOutput, *config.Configuration]

func (r *ObjectResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ObjectResourceOutput, error) {
	obj, err := r.object()
	if err != nil {
		return nil, err
	}
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	api, err := resourceClient(c, obj)
	if err != nil {
		return nil, err
	}
	return r.apply(ctx, api, obj, resolved.Host)
}

func (r *ObjectResource) Read(
	ctx context.Context, c *config.Configuration, p prior,
) (*ObjectResourceOutput, error) {
	api, err := priorClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	obj, err := api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes object: %w", err)
	}
	if string(obj.GetUID()) != p.Outputs.UID {
		return nil, fmt.Errorf("object %s UID changed from %s to %s",
			p.Outputs.Name, p.Outputs.UID, obj.GetUID())
	}
	if obj.GetDeletionTimestamp() != nil {
		return nil, fmt.Errorf("object %s is being deleted", p.Outputs.Name)
	}
	values, err := fieldValues(obj, p.Outputs.Fields)
	if err != nil {
		return nil, err
	}
	output := *p.Outputs
	output.Values = values
	return &output, nil
}

func (r *ObjectResource) Update(
	ctx context.Context, c *config.Configuration, p prior,
) (*ObjectResourceOutput, error) {
	obj, err := r.object()
	if err != nil {
		return nil, err
	}
	api, err := resourceClient(c, obj)
	if err != nil {
		return nil, err
	}
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	if p.Outputs == nil || p.Outputs.UID == "" || obj.GetName() != p.Outputs.Name ||
		obj.GetNamespace() != p.Outputs.Namespace || obj.GetKind() != p.Outputs.Kind ||
		obj.GetAPIVersion() != p.Outputs.APIVersion || resolved.Host != p.Outputs.Server {
		return nil, fmt.Errorf("object identity changed; replacement is required")
	}
	obj.SetUID(types.UID(p.Outputs.UID))
	return r.apply(ctx, api, obj, p.Outputs.Server)
}

func (r *ObjectResource) Delete(ctx context.Context, c *config.Configuration, p prior) error {
	api, err := priorClient(c, p.Outputs)
	if err != nil {
		return err
	}
	timeout, err := p.Inputs.deleteTimeout()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	uid := types.UID(p.Outputs.UID)
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Kubernetes object: %w", err)
	}
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			obj, err := api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return obj.GetUID() != uid, nil
		})
	if err != nil {
		return fmt.Errorf("wait for Kubernetes object deletion: %w", err)
	}
	return nil
}

func (r *ObjectResource) apply(
	ctx context.Context, api dynamic.ResourceInterface, obj *unstructured.Unstructured, server string,
) (*ObjectResourceOutput, error) {
	force := r.ForceConflicts != nil && *r.ForceConflicts
	result, err := api.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{
		FieldManager: r.fieldManager(), Force: force,
	})
	if err != nil {
		return nil, fmt.Errorf("apply Kubernetes object: %w", err)
	}
	if result.GetUID() == "" {
		return nil, fmt.Errorf("object has no Kubernetes UID")
	}
	fields, err := ownedFields(result, r.fieldManager())
	if err != nil {
		return nil, err
	}
	values, err := fieldValues(result, fields)
	if err != nil {
		return nil, err
	}
	return &ObjectResourceOutput{
		Server: server, APIVersion: result.GetAPIVersion(), Kind: result.GetKind(),
		Name: result.GetName(), Namespace: result.GetNamespace(), UID: string(result.GetUID()),
		Fields: fields, Values: values,
	}, nil
}

func resourceClient(
	c *config.Configuration, obj *unstructured.Unstructured,
) (dynamic.ResourceInterface, error) {
	api, err := client.NewDynamic(c)
	if err != nil {
		return nil, err
	}
	resource, namespace, err := api.Resolve(obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return nil, err
	}
	obj.SetNamespace(namespace)
	return resource, nil
}

func priorClient(c *config.Configuration, output *ObjectResourceOutput) (
	dynamic.ResourceInterface, error,
) {
	if output == nil || output.UID == "" || output.Server == "" || output.Name == "" ||
		output.APIVersion == "" || output.Kind == "" {
		return nil, fmt.Errorf("missing Kubernetes object identity in prior state")
	}
	target := config.Configuration{}
	if c != nil {
		target = *c
	}
	target.Server = &output.Server
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.FromAPIVersionAndKind(output.APIVersion, output.Kind))
	obj.SetNamespace(output.Namespace)
	api, err := resourceClient(&target, obj)
	if err != nil {
		return nil, err
	}
	if obj.GetNamespace() != output.Namespace {
		return nil, fmt.Errorf("missing Kubernetes object namespace in prior state")
	}
	return api, nil
}
