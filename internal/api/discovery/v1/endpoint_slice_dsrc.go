package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// EndpointSliceDataSource reads one named discovery.k8s.io/v1 EndpointSlice.
type EndpointSliceDataSource struct {
	Metadata EndpointSliceLookupMetadata
}

type EndpointSliceLookupMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]*string
	Labels       *map[string]*string
}

type EndpointSliceDataSourceOutput struct {
	Metadata    EndpointSliceLookupMetadataOutput
	AddressType *string
	Endpoints   []EndpointSliceEndpointOutput
	Ports       []EndpointSlicePortOutput
}

type EndpointSliceLookupMetadataOutput struct {
	Name            string
	GenerateName    string
	Namespace       string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

var _ runtime.TypedDataSource[
	*EndpointSliceDataSourceOutput, *config.Configuration,
] = (*EndpointSliceDataSource)(nil)

func (r EndpointSliceDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *EndpointSliceDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*EndpointSliceDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	api, err := client.New(c)
	if err != nil {
		return nil, err
	}
	namespace := defaultNamespace(r.Metadata.Namespace)
	name := value(r.Metadata.Name)
	slice, err := api.DiscoveryV1().EndpointSlices(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &EndpointSliceDataSourceOutput{
			Metadata: EndpointSliceLookupMetadataOutput{
				Name: name, Namespace: namespace, GenerateName: value(r.Metadata.GenerateName),
				Annotations: endpointSliceLookupMap(r.Metadata.Annotations),
				Labels:      endpointSliceLookupMap(r.Metadata.Labels),
			},
			Endpoints: []EndpointSliceEndpointOutput{},
			Ports:     []EndpointSlicePortOutput{},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get EndpointSlice %s/%s: %w", namespace, name, err)
	}
	return &EndpointSliceDataSourceOutput{
		Metadata: EndpointSliceLookupMetadataOutput{
			Name: slice.Name, Namespace: slice.Namespace, GenerateName: slice.GenerateName,
			Generation: slice.Generation, ResourceVersion: slice.ResourceVersion,
			UID:         string(slice.UID),
			Annotations: maps.Collect(maps.All(slice.Annotations)),
			Labels:      maps.Collect(maps.All(slice.Labels)),
		},
		AddressType: new(string(slice.AddressType)),
		Endpoints:   readEndpoints(slice.Endpoints),
		Ports:       readEndpointSlicePorts(slice.Ports),
	}, nil
}

func endpointSliceLookupMap(input *map[string]*string) map[string]string {
	out := map[string]string{}
	for key, item := range value(input) {
		if item != nil {
			out[key] = *item
		}
	}
	return out
}

func (r EndpointSliceDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.Must(
			constraint.IsMatch(
				r.Metadata.GenerateName,
				`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|([a-z0-9][-a-z0-9]{0,61})?[\x00-\x7F]-)$`,
			),
		),
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
					),
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Value,
							`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
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
