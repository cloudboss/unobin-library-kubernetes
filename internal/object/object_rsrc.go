package object

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
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
	connection, err := Connect(c, obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return nil, err
	}
	obj.SetNamespace(connection.Namespace)
	return r.apply(ctx, connection.Resource, obj, connection.Server)
}

func (r *ObjectResource) Read(
	ctx context.Context, c *config.Configuration, p prior,
) (*ObjectResourceOutput, error) {
	target := p.Outputs.target()
	connection, err := TargetClient(c, target)
	if err != nil {
		return nil, err
	}
	obj, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes object: %w", err)
	}
	if err := CheckUID(target, obj); err != nil {
		return nil, err
	}
	if obj.GetDeletionTimestamp() != nil {
		return nil, fmt.Errorf("object %s is being deleted", p.Outputs.Name)
	}
	values, err := FieldValues(obj, p.Outputs.Fields)
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
	connection, err := Connect(c, obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return nil, err
	}
	obj.SetNamespace(connection.Namespace)
	if p.Outputs == nil || p.Outputs.UID == "" || obj.GetName() != p.Outputs.Name ||
		obj.GetNamespace() != p.Outputs.Namespace || obj.GetKind() != p.Outputs.Kind ||
		obj.GetAPIVersion() != p.Outputs.APIVersion || connection.Server != p.Outputs.Server {
		return nil, fmt.Errorf("object identity changed; replacement is required")
	}
	obj.SetUID(types.UID(p.Outputs.UID))
	return r.apply(ctx, connection.Resource, obj, p.Outputs.Server)
}

func (r *ObjectResource) Delete(ctx context.Context, c *config.Configuration, p prior) error {
	timeout, err := p.Inputs.deleteTimeout()
	if err != nil {
		return err
	}
	return Delete(ctx, c, p.Outputs.target(), metav1.DeleteOptions{}, timeout)
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
	fields, err := OwnedFields(result, r.fieldManager())
	if err != nil {
		return nil, err
	}
	values, err := FieldValues(result, fields)
	if err != nil {
		return nil, err
	}
	return &ObjectResourceOutput{
		Server: server, APIVersion: result.GetAPIVersion(), Kind: result.GetKind(),
		Name: result.GetName(), Namespace: result.GetNamespace(), UID: string(result.GetUID()),
		Fields: fields, Values: values,
	}, nil
}

func (o *ObjectResourceOutput) target() Target {
	if o == nil {
		return Target{}
	}
	return Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}
