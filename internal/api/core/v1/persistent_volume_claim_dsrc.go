package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// PersistentVolumeClaimDataSource reads a namespaced claim by its exact name.
type PersistentVolumeClaimDataSource struct {
	Metadata PersistentVolumeClaimLookupMetadata
}

type PersistentVolumeClaimLookupMetadata struct {
	Name        *string
	Namespace   *string
	Annotations *map[string]string
	Labels      *map[string]string
}

// PersistentVolumeClaimDataSourceOutput contains live claim data.
// A missing claim returns lookup metadata and a null spec.
type PersistentVolumeClaimDataSourceOutput struct {
	Metadata PersistentVolumeClaimLookupMetadataOutput
	Spec     *PersistentVolumeClaimSpecOutput
}

type PersistentVolumeClaimLookupMetadataOutput struct {
	Name            string
	Namespace       string
	GenerateName    string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

var _ runtime.TypedDataSource[
	*PersistentVolumeClaimDataSourceOutput, *config.Configuration,
] = (*PersistentVolumeClaimDataSource)(nil)

func (r *PersistentVolumeClaimDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*PersistentVolumeClaimDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace := ptr.Deref(r.Metadata.Namespace, "default")
	name := ptr.Deref(r.Metadata.Name, "")
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("get PersistentVolumeClaim %s/%s: %w", namespace, name, err)
	}
	obj, err := api.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &PersistentVolumeClaimDataSourceOutput{
			Metadata: PersistentVolumeClaimLookupMetadataOutput{
				Name: name, Namespace: namespace,
				Annotations: maps.Clone(serviceMap(r.Metadata.Annotations)),
				Labels:      maps.Clone(serviceMap(r.Metadata.Labels)),
			},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get PersistentVolumeClaim %s/%s: %w", namespace, name, err)
	}
	spec := pvcSpecOutput(obj.Spec)
	return &PersistentVolumeClaimDataSourceOutput{
		Metadata: PersistentVolumeClaimLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion, UID: string(obj.UID),
			Annotations: serviceMap(&obj.Annotations), Labels: serviceMap(&obj.Labels),
		},
		Spec: &spec,
	}, nil
}

func (r PersistentVolumeClaimDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
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
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
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
