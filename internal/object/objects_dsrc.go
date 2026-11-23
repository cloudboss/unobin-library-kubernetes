package object

import (
	"context"
	"fmt"
	"math"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/structdecode"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type ObjectsDataSource struct {
	APIVersion    string
	Kind          string
	Namespace     *string
	LabelSelector *string
	FieldSelector *string
	Limit         *float64

	integerLimit *int64
}

type ObjectsDataSourceOutput struct {
	Objects *[]map[string]any `ub:",sensitive"`
}

func (d ObjectsDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.IsMatch(d.APIVersion, `^[^/]*/?[^/]*$`)),
	}
}

func (d *ObjectsDataSource) UnmarshalMapstructure(input any) error {
	values, ok := input.(map[string]any)
	if !ok {
		return fmt.Errorf("decode Kubernetes objects inputs: expected a map, got %T", input)
	}

	// Preserve evaluated integers before decoding the public number field as float64.
	d.integerLimit = nil
	if limit, ok := values["limit"].(int64); ok {
		d.integerLimit = &limit
	}
	type inputs ObjectsDataSource
	return structdecode.Decode((*inputs)(d), values)
}

func (d *ObjectsDataSource) listOptions() metav1.ListOptions {
	options := metav1.ListOptions{}
	if d.LabelSelector != nil {
		options.LabelSelector = *d.LabelSelector
	}
	if d.FieldSelector != nil {
		options.FieldSelector = *d.FieldSelector
	}
	if d.integerLimit != nil {
		options.Limit = *d.integerLimit
	} else if d.Limit != nil {
		switch limit := *d.Limit; {
		case limit >= float64(math.MaxInt64):
			options.Limit = math.MaxInt64
		case limit <= float64(math.MinInt64):
			options.Limit = math.MinInt64
		default:
			options.Limit = int64(limit)
		}
	}
	return options
}

func (d *ObjectsDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ObjectsDataSourceOutput, error) {
	resource, err := lookupResource(ctx, c, d.APIVersion, d.Kind, d.Namespace)
	if err != nil {
		return nil, err
	}
	page, err := resource.List(ctx, d.listOptions())
	if apierrors.IsNotFound(err) {
		return &ObjectsDataSourceOutput{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list Kubernetes %s: %w", d.Kind, err)
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, item.Object)
	}
	return &ObjectsDataSourceOutput{Objects: &items}, nil
}
