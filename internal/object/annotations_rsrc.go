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

const defaultAnnotationsManager = "Terraform"

type AnnotationsResource struct {
	APIVersion          string
	Kind                string
	Metadata            ObjectLookupMetadata
	Annotations         *map[string]string
	TemplateAnnotations *map[string]string
	FieldManager        *string
	ForceConflicts      *bool
}

type AnnotationsResourceOutput struct {
	Server              string
	APIVersion          string
	Kind                string
	Name                string
	Namespace           string
	UID                 string
	Annotations         map[string]string
	TemplateAnnotations map[string]string
}

type annotationsPrior = runtime.Prior[
	AnnotationsResource, *AnnotationsResourceOutput, *config.Configuration,
]

func (r AnnotationsResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.AtLeastOneOf(r.Annotations, r.TemplateAnnotations),
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

func (r AnnotationsResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.FieldManager, "Terraform"),
		defaults.NullableValue(r.ForceConflicts, false),
	}
}

func (r *AnnotationsResource) ResourceDefinition() runtime.ResourceDefinition[
	AnnotationsResource, *AnnotationsResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		AnnotationsResource, *AnnotationsResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ AnnotationsResource, o *AnnotationsResourceOutput) (string, error) {
			if o == nil || o.Server == "" || o.UID == "" {
				return "", fmt.Errorf("missing Kubernetes object server or UID")
			}
			return o.Server + "/" + o.UID, nil
		},
		Replace: runtime.Replacement[
			AnnotationsResource, *AnnotationsResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[AnnotationsResource]{
				runtime.InputField(func(r *AnnotationsResource) *string { return &r.APIVersion }),
				runtime.InputField(func(r *AnnotationsResource) *string { return &r.Kind }),
				runtime.InputField(func(r *AnnotationsResource) *string { return &r.Metadata.Name }),
				runtime.InputField(func(r *AnnotationsResource) **string {
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

func (r *AnnotationsResource) Create(
	ctx context.Context, c *config.Configuration,
) (*AnnotationsResourceOutput, error) {
	return r.apply(ctx, c, nil)
}

func (r *AnnotationsResource) Read(
	ctx context.Context, c *config.Configuration, p annotationsPrior,
) (*AnnotationsResourceOutput, error) {
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
		return nil, fmt.Errorf("read Kubernetes %s %s annotations: %w", target.Kind, target.Name, err)
	}
	return p.Inputs.observeAnnotations(target, obj, p.Outputs.Annotations,
		p.Outputs.TemplateAnnotations)
}

func (r *AnnotationsResource) Update(
	ctx context.Context, c *config.Configuration, p annotationsPrior,
) (*AnnotationsResourceOutput, error) {
	target := p.Outputs.target()
	if target.UID == "" {
		return nil, fmt.Errorf("missing Kubernetes object identity in prior state")
	}
	return r.apply(ctx, c, &target)
}

func (r *AnnotationsResource) Delete(
	ctx context.Context, c *config.Configuration, p annotationsPrior,
) error {
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
		return fmt.Errorf("get Kubernetes %s %s for annotation cleanup: %w",
			target.Kind, target.Name, err)
	}
	if err := CheckUID(target, obj); err != nil {
		return err
	}
	effective := p.Observed
	if effective == nil {
		effective = p.Outputs
	}
	var top, template map[string]string
	if len(effective.Annotations) > 0 {
		top = map[string]string{}
	}
	if len(effective.TemplateAnnotations) > 0 {
		template = map[string]string{}
	}
	err = p.Inputs.patchAnnotations(ctx, connection.Resource, target, top, template)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clean up Kubernetes %s %s annotations: %w",
			target.Kind, target.Name, err)
	}
	return nil
}

func (r *AnnotationsResource) apply(
	ctx context.Context, c *config.Configuration, previous *Target,
) (*AnnotationsResourceOutput, error) {
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
		return nil, fmt.Errorf("select Kubernetes %s %s: %w", gv, r.Kind, err)
	}
	target := Target{
		Server: connection.Server, APIVersion: r.APIVersion, Kind: r.Kind,
		Name: r.Metadata.Name, Namespace: connection.Namespace,
	}
	if previous != nil {
		target.UID = previous.UID
		if target != *previous {
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
	var top, template map[string]string
	if r.Annotations != nil && len(*r.Annotations) > 0 {
		top = *r.Annotations
	}
	if r.TemplateAnnotations != nil && len(*r.TemplateAnnotations) > 0 {
		template = *r.TemplateAnnotations
	}
	if err := r.patchAnnotations(ctx, connection.Resource, target, top, template); err != nil {
		return nil, fmt.Errorf("apply Kubernetes %s %s annotations: %w", target.Kind, target.Name, err)
	}
	obj, err = connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("observe Kubernetes %s %s annotations: %w",
			target.Kind, target.Name, err)
	}
	return r.observeAnnotations(target, obj, top, template)
}

func (r *AnnotationsResource) patchAnnotations(
	ctx context.Context, api dynamic.ResourceInterface, target Target,
	top, template map[string]string,
) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(target.APIVersion)
	obj.SetKind(target.Kind)
	obj.SetName(target.Name)
	obj.SetNamespace(target.Namespace)
	obj.SetUID(types.UID(target.UID))
	if top != nil {
		obj.SetAnnotations(top)
	}
	if template != nil {
		path := templateAnnotationPath(target.Kind)
		if err := unstructured.SetNestedStringMap(obj.Object, template, path...); err != nil {
			return fmt.Errorf("prepare Kubernetes template annotations: %w", err)
		}
	}
	_, err := api.Apply(ctx, target.Name, obj, metav1.ApplyOptions{
		FieldManager: r.annotationsManager(), Force: r.ForceConflicts != nil && *r.ForceConflicts,
	})
	if _, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict); conflict {
		return fmt.Errorf("annotation ownership conflict; set force-conflicts to take ownership: %w",
			err)
	}
	return err
}

func (r *AnnotationsResource) annotationsManager() string {
	if r.FieldManager != nil {
		return *r.FieldManager
	}
	return defaultAnnotationsManager
}

func (o *AnnotationsResourceOutput) target() Target {
	if o == nil {
		return Target{}
	}
	return Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}

func templateAnnotationPath(kind string) []string {
	if kind == "CronJob" {
		return []string{"spec", "jobTemplate", "spec", "template", "metadata", "annotations"}
	}
	return []string{"spec", "template", "metadata", "annotations"}
}

func (r *AnnotationsResource) observeAnnotations(
	target Target, obj *unstructured.Unstructured, selectedTop, selectedTemplate map[string]string,
) (*AnnotationsResourceOutput, error) {
	if err := CheckUID(target, obj); err != nil {
		return nil, err
	}
	top, template, err := managedAnnotationFields(
		obj.GetManagedFields(), r.annotationsManager(), target.Kind,
	)
	if err != nil {
		return nil, err
	}
	values, _, err := unstructured.NestedStringMap(obj.Object, templateAnnotationPath(target.Kind)...)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes template annotations: %w", err)
	}
	return &AnnotationsResourceOutput{
		Server: target.Server, APIVersion: target.APIVersion, Kind: target.Kind,
		Name: target.Name, Namespace: target.Namespace, UID: target.UID,
		Annotations:         projectAnnotations(obj.GetAnnotations(), selectedTop, top),
		TemplateAnnotations: projectAnnotations(values, selectedTemplate, template),
	}, nil
}

func projectAnnotations(
	live, selected map[string]string, owned map[string]any,
) map[string]string {
	values := map[string]string{}
	for key, value := range live {
		_, kept := selected[key]
		_, managed := owned["f:"+key]
		if kept || managed {
			values[key] = value
		}
	}
	return values
}

func managedAnnotationFields(entries []metav1.ManagedFieldsEntry, manager, kind string) (
	top, template map[string]any, err error,
) {
	for _, entry := range entries {
		if entry.Manager != manager || entry.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			return nil, nil, fmt.Errorf("decode Kubernetes annotation ownership for manager %q: %w",
				manager, err)
		}
		if candidate, found, _ := unstructured.NestedMap(
			fields, "f:metadata", "f:annotations",
		); found {
			top = candidate
		}
		spec, _, _ := unstructured.NestedMap(fields, "f:spec")
		if kind == "CronJob" {
			if job, found, _ := unstructured.NestedMap(spec, "f:jobTemplate"); found {
				spec, _, _ = unstructured.NestedMap(job, "f:spec")
			}
		}
		if candidate, found, _ := unstructured.NestedMap(
			spec, "f:template", "f:metadata", "f:annotations",
		); found {
			template = candidate
		}
	}
	return top, template, nil
}
