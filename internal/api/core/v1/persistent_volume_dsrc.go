package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type PersistentVolumeDataSource struct {
	Metadata PersistentVolumeLookupMetadata
}

type PersistentVolumeLookupMetadata struct {
	Name        string
	Annotations *map[string]string
	Labels      *map[string]string
}

type PersistentVolumeDataSourceOutput struct {
	Metadata PersistentVolumeLookupMetadataOutput
	Spec     PersistentVolumeSpecOutput
}

type PersistentVolumeLookupMetadataOutput struct {
	Name            string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

func (r *PersistentVolumeDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*PersistentVolumeDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	name := r.Metadata.Name
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("get PersistentVolume %s: %w", name, err)
	}
	obj, err := api.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get PersistentVolume %s: %w", name, err)
	}

	spec := pvSpecOutput(obj.Spec)
	spec.VolumeMode = string(ptr.Deref(obj.Spec.VolumeMode, ""))
	if obj.Spec.HostPath != nil && obj.Spec.HostPath.Path == "" {
		spec.PersistentVolumeSource.HostPath.Path = new("")
	}
	if obj.Spec.Local != nil && obj.Spec.Local.Path == "" {
		spec.PersistentVolumeSource.Local.Path = new("")
	}
	return &PersistentVolumeDataSourceOutput{
		Metadata: PersistentVolumeLookupMetadataOutput{
			Name: obj.Name, UID: string(obj.UID),
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion,
			Annotations: serviceMap(&obj.Annotations), Labels: serviceMap(&obj.Labels),
		},
		Spec: spec,
	}, nil
}

func (r PersistentVolumeDataSource) Constraints() []constraint.Constraint {
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
