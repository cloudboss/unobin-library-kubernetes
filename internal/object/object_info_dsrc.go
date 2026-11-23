package object

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
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
	resource, err := lookupResource(ctx, c, d.APIVersion, d.Kind, d.Metadata.Namespace)
	if err != nil {
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
