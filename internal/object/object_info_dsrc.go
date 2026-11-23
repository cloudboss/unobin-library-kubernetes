package object

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type ObjectDataSource struct {
	APIVersion string
	Kind       string
	Metadata   ObjectLookupMetadata
}

type ObjectLookupMetadata struct {
	Name      string
	Namespace *string
}

type ObjectDataSourceOutput struct {
	Object *map[string]any `ub:",sensitive"`
}

func (d ObjectDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.IsMatch(d.APIVersion, `^[^/]*/?[^/]*$`)),
		constraint.Must(
			constraint.IsMatch(d.Metadata.Name, `^[^/%]+$`),
			constraint.NotEquals(d.Metadata.Name, "."),
			constraint.NotEquals(d.Metadata.Name, ".."),
		),
	}
}

func (d *ObjectDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ObjectDataSourceOutput, error) {
	gv, err := schema.ParseGroupVersion(d.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes api-version %q: %w", d.APIVersion, err)
	}
	lookup := config.Configuration{}
	if c != nil {
		lookup = *c
	}
	lookup.Namespace = new("default")
	if d.Metadata.Namespace != nil && *d.Metadata.Namespace != "" {
		lookup.Namespace = d.Metadata.Namespace
	}
	api, err := client.NewDynamicContext(ctx, &lookup)
	if err != nil {
		return nil, fmt.Errorf("initialize Kubernetes object lookup: %w", err)
	}
	gvk := gv.WithKind(d.Kind)
	resource, _, err := api.Resolve(gvk, "")
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("discover Kubernetes kind %s: %w", gvk, ctx.Err())
		}
		return nil, err
	}
	obj, err := resource.Get(ctx, d.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &ObjectDataSourceOutput{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Kubernetes %s %s: %w", d.Kind, d.Metadata.Name, err)
	}
	return &ObjectDataSourceOutput{Object: &obj.Object}, nil
}
