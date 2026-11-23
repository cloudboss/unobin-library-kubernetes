package v1

import (
	"context"
	"fmt"
	"maps"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin/pkg/constraint"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// StorageClassDataSource looks up a cluster-scoped StorageClass by name.
type StorageClassDataSource struct {
	Metadata             StorageClassLookupMetadata
	Parameters           *map[string]*string
	ReclaimPolicy        *string
	VolumeBindingMode    *string
	AllowVolumeExpansion *bool
	MountOptions         *[]string
	AllowedTopologies    *[]StorageClassTopologyTerm
}

type StorageClassLookupMetadata struct {
	Name        *string
	Annotations *map[string]*string
	Labels      *map[string]*string
}

type StorageClassDataSourceOutput struct {
	Metadata             StorageClassLookupMetadataOutput
	Provisioner          *string
	Parameters           map[string]string
	ReclaimPolicy        *string
	VolumeBindingMode    *string
	AllowVolumeExpansion *bool
	MountOptions         []string
	AllowedTopologies    []StorageClassTopologyTermOutput
}

type StorageClassLookupMetadataOutput struct {
	Name            string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

func (r *StorageClassDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*StorageClassDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, storageClassTimeout)
	defer cancel()

	name := ptr.Deref(r.Metadata.Name, "")
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("get StorageClass %s: %w", name, err)
	}
	obj, err := api.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &StorageClassDataSourceOutput{
			Metadata: StorageClassLookupMetadataOutput{
				Name:        name,
				Annotations: storageClassLookupMap(r.Metadata.Annotations),
				Labels:      storageClassLookupMap(r.Metadata.Labels),
			},
			Parameters:           storageClassLookupMap(r.Parameters),
			ReclaimPolicy:        nonemptyStorageClassFallback(r.ReclaimPolicy),
			VolumeBindingMode:    nonemptyStorageClassFallback(r.VolumeBindingMode),
			AllowVolumeExpansion: r.AllowVolumeExpansion,
			MountOptions:         storageClassSet(ptr.Deref(r.MountOptions, nil)),
			AllowedTopologies:    storageClassTopologyOutput(storageClassTopologies(r.AllowedTopologies)),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get StorageClass %s: %w", name, err)
	}
	topologies := obj.AllowedTopologies
	if topologies == nil {
		topologies = storageClassTopologies(r.AllowedTopologies)
	}
	binding := nonemptyStorageClassFallback(r.VolumeBindingMode)
	if obj.VolumeBindingMode != nil {
		binding = new(string(*obj.VolumeBindingMode))
	}
	expansion := obj.AllowVolumeExpansion
	if expansion == nil {
		expansion = r.AllowVolumeExpansion
	}
	out := &StorageClassDataSourceOutput{
		Metadata: StorageClassLookupMetadataOutput{
			Name: obj.Name, UID: string(obj.UID), Generation: obj.Generation,
			ResourceVersion: obj.ResourceVersion,
			Annotations:     map[string]string{}, Labels: map[string]string{},
		},
		Provisioner: new(obj.Provisioner), Parameters: map[string]string{},
		ReclaimPolicy:     new(string(ptr.Deref(obj.ReclaimPolicy, ""))),
		VolumeBindingMode: binding, AllowVolumeExpansion: expansion,
		MountOptions:      storageClassSet(obj.MountOptions),
		AllowedTopologies: storageClassTopologyOutput(topologies),
	}
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Parameters, obj.Parameters)
	return out, nil
}

func storageClassLookupMap(input *map[string]*string) map[string]string {
	out := map[string]string{}
	for key, value := range ptr.Deref(input, nil) {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func nonemptyStorageClassFallback(value *string) *string {
	if ptr.Deref(value, "") == "" {
		return nil
	}
	return value
}

func (r StorageClassDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		).Message("metadata.name must be a DNS subdomain of at most 253 bytes"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					).Message("metadata.labels keys must be Kubernetes qualified names"),
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`,
					)).Message("metadata.labels values must be valid Kubernetes label values"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Key,
							`^([A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?`+
								`(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					).Message("metadata.annotations keys must be qualified names after lowercasing"),
				}
			},
		),
		constraint.Must(constraint.MaxItems(r.AllowedTopologies, 1)).
			Message("allowed-topologies must contain at most one term"),
	}
}
