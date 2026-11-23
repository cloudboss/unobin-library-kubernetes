package object

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const defaultLabelsManager = "Terraform"

type LabelsResource struct {
	APIVersion     string
	Kind           string
	Metadata       ObjectLookupMetadata
	Labels         map[string]string
	FieldManager   *string
	ForceConflicts *bool
}

type LabelsResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Labels     map[string]string
}

type labelsPrior = runtime.Prior[LabelsResource, *LabelsResourceOutput, *config.Configuration]

func (r LabelsResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.IsMatch(r.APIVersion, `^[^/]*/?[^/]+$`)),
		constraint.Must(constraint.NotEmpty(r.Kind)),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name, `^[^/%]+$`),
			constraint.NotEquals(r.Metadata.Name, "."),
			constraint.NotEquals(r.Metadata.Name, ".."),
		),
		constraint.Must(constraint.IsMatch(r.FieldManager, `[^\p{Z}\x09-\x0d\x{0085}]`)),
	}
}

func (r LabelsResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.FieldManager, "Terraform"),
		defaults.NullableValue(r.ForceConflicts, false),
	}
}

func (r *LabelsResource) ResourceDefinition() runtime.ResourceDefinition[
	LabelsResource, *LabelsResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		LabelsResource, *LabelsResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ LabelsResource, o *LabelsResourceOutput) (string, error) {
			if o == nil || o.Server == "" || o.UID == "" {
				return "", fmt.Errorf("missing Kubernetes object server or UID")
			}
			return o.Server + "/" + o.UID, nil
		},
		Replace: runtime.Replacement[LabelsResource, *LabelsResourceOutput, *config.Configuration]{
			Fields: []runtime.AnyInputField[LabelsResource]{
				runtime.InputField(func(r *LabelsResource) *string { return &r.APIVersion }),
				runtime.InputField(func(r *LabelsResource) *string { return &r.Kind }),
				runtime.InputField(func(r *LabelsResource) *string { return &r.Metadata.Name }),
				runtime.InputField(func(r *LabelsResource) **string {
					return &r.Metadata.Namespace
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func (r *LabelsResource) Create(
	ctx context.Context, c *config.Configuration,
) (*LabelsResourceOutput, error) {
	return r.apply(ctx, c, nil)
}

func (r *LabelsResource) Read(
	ctx context.Context, c *config.Configuration, p labelsPrior,
) (*LabelsResourceOutput, error) {
	target := p.Outputs.target()
	connection, err := targetClient(ctx, c, target)
	if err != nil {
		return nil, err
	}
	obj, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes labels on %s %s: %w", target.Kind, target.Name, err)
	}
	return p.Inputs.observeLabels(target, obj, p.Outputs.Labels)
}

func (r *LabelsResource) Update(
	ctx context.Context, c *config.Configuration, p labelsPrior,
) (*LabelsResourceOutput, error) {
	if p.Outputs == nil || p.Outputs.UID == "" {
		return nil, fmt.Errorf("missing Kubernetes object identity in prior state")
	}
	return r.apply(ctx, c, p.Outputs)
}

func (r *LabelsResource) Delete(ctx context.Context, c *config.Configuration, p labelsPrior) error {
	target := p.Outputs.target()
	connection, err := targetClient(ctx, c, target)
	if err != nil {
		return err
	}
	obj, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Kubernetes %s %s for label cleanup: %w", target.Kind, target.Name, err)
	}
	if err := CheckUID(target, obj); err != nil {
		return err
	}
	err = p.Inputs.patchLabels(ctx, connection.Resource, target, map[string]string{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clean up Kubernetes labels on %s %s: %w", target.Kind, target.Name, err)
	}
	return nil
}

func (r *LabelsResource) apply(
	ctx context.Context, c *config.Configuration, previous *LabelsResourceOutput,
) (*LabelsResourceOutput, error) {
	gv, err := schema.ParseGroupVersion(r.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes api-version %q: %w", r.APIVersion, err)
	}
	lookup := config.Configuration{}
	if c != nil {
		lookup = *c
	}
	lookup.Namespace = new("default")
	if r.Metadata.Namespace != nil && *r.Metadata.Namespace != "" {
		lookup.Namespace = r.Metadata.Namespace
	}
	connection, err := connect(ctx, &lookup, gv.WithKind(r.Kind), "")
	if err != nil {
		return nil, err
	}
	target := Target{
		Server: connection.Server, APIVersion: r.APIVersion, Kind: r.Kind,
		Name: r.Metadata.Name, Namespace: connection.Namespace,
	}
	if previous != nil {
		target.UID = previous.UID
		if target != previous.target() {
			return nil, fmt.Errorf("object identity changed; replacement is required")
		}
	}
	obj, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get existing Kubernetes %s %s: %w", target.Kind, target.Name, err)
	}
	if previous == nil {
		target.UID = string(obj.GetUID())
	}
	if err := CheckUID(target, obj); err != nil {
		return nil, err
	}
	if err := r.patchLabels(ctx, connection.Resource, target, r.Labels); err != nil {
		return nil, fmt.Errorf("apply Kubernetes labels to %s %s: %w", target.Kind, target.Name, err)
	}
	obj, err = connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("observe Kubernetes labels on %s %s: %w", target.Kind, target.Name, err)
	}
	return r.observeLabels(target, obj, r.Labels)
}

func (r *LabelsResource) patchLabels(
	ctx context.Context, api dynamic.ResourceInterface, target Target, labels map[string]string,
) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(target.APIVersion)
	obj.SetKind(target.Kind)
	obj.SetName(target.Name)
	obj.SetNamespace(target.Namespace)
	obj.SetUID(types.UID(target.UID))
	obj.SetLabels(labels)
	_, err := api.Apply(ctx, target.Name, obj, metav1.ApplyOptions{
		FieldManager: r.labelsManager(), Force: r.ForceConflicts != nil && *r.ForceConflicts,
	})
	if _, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict); conflict {
		return fmt.Errorf("label ownership conflict; set force-conflicts to take ownership: %w", err)
	}
	return err
}

func (r *LabelsResource) labelsManager() string {
	if r.FieldManager != nil {
		return *r.FieldManager
	}
	return defaultLabelsManager
}

func (o *LabelsResourceOutput) target() Target {
	if o == nil {
		return Target{}
	}
	return Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}

func (r *LabelsResource) observeLabels(
	target Target, obj *unstructured.Unstructured, selectedLabels map[string]string,
) (*LabelsResourceOutput, error) {
	if err := CheckUID(target, obj); err != nil {
		return nil, err
	}
	owned, err := managedLabelFields(obj.GetManagedFields(), r.labelsManager())
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for name, value := range obj.GetLabels() {
		_, selected := selectedLabels[name]
		_, managed := owned["f:"+name]
		if selected || managed {
			labels[name] = value
		}
	}
	return &LabelsResourceOutput{
		Server: target.Server, APIVersion: target.APIVersion, Kind: target.Kind,
		Name: target.Name, Namespace: target.Namespace, UID: target.UID, Labels: labels,
	}, nil
}

func managedLabelFields(entries []metav1.ManagedFieldsEntry, manager string) (
	map[string]any, error,
) {
	var owned map[string]any
	for _, entry := range entries {
		if entry.Manager != manager || entry.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			return nil, fmt.Errorf("decode Kubernetes label ownership for manager %q: %w",
				manager, err)
		}
		if labels, found, _ := unstructured.NestedMap(fields, "f:metadata", "f:labels"); found {
			owned = labels
		}
	}
	return owned, nil
}
