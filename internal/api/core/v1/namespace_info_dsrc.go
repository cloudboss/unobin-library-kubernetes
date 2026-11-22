package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// NamespaceDataSource reads one cluster-scoped Namespace without changing it.
// A missing Namespace returns lookup metadata and null spec.
type NamespaceDataSource struct{ Metadata NamespaceLookupMetadata }

// NamespaceLookupMetadata selects only by name. Configured maps are returned on
// absence, with null entries omitted; successful reads return all remote keys.
type NamespaceLookupMetadata struct {
	Name        string
	Annotations *map[string]*string
	Labels      *map[string]*string
}

// NamespaceDataSourceOutput contains live metadata and ordered finalizers.
// Existing Namespaces have a non-null spec, including when finalizers is empty.
type NamespaceDataSourceOutput struct {
	Metadata NamespaceLookupMetadataOutput
	Spec     *NamespaceLookupSpec
}

type NamespaceLookupMetadataOutput struct {
	Name            string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

type NamespaceLookupSpec struct{ Finalizers []string }

var _ runtime.TypedDataSource[
	*NamespaceDataSourceOutput, *config.Configuration,
] = (*NamespaceDataSource)(nil)

func (r *NamespaceDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*NamespaceDataSourceOutput, error) {
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("get Namespace %s: %w", r.Metadata.Name, err)
	}
	obj, err := api.CoreV1().Namespaces().Get(ctx, r.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &NamespaceDataSourceOutput{Metadata: NamespaceLookupMetadataOutput{
			Name: r.Metadata.Name, Annotations: namespaceLookupMap(r.Metadata.Annotations),
			Labels: namespaceLookupMap(r.Metadata.Labels),
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Namespace %s: %w", r.Metadata.Name, err)
	}
	annotations, labels := obj.Annotations, obj.Labels
	if annotations == nil {
		annotations = map[string]string{}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	out := &NamespaceDataSourceOutput{
		Metadata: NamespaceLookupMetadataOutput{
			Name: obj.Name, Annotations: annotations, Labels: labels,
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion, UID: string(obj.UID),
		},
		Spec: &NamespaceLookupSpec{Finalizers: []string{}},
	}
	for _, finalizer := range obj.Spec.Finalizers {
		out.Spec.Finalizers = append(out.Spec.Finalizers, string(finalizer))
	}
	return out, nil
}

func namespaceLookupMap(input *map[string]*string) map[string]string {
	out := map[string]string{}
	if input != nil {
		for key, value := range *input {
			if value != nil {
				out[key] = *value
			}
		}
	}
	return out
}

func (r NamespaceDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?`+
								`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
	}
}
