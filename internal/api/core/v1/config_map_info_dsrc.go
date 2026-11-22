package v1

import (
	"context"
	"encoding/base64"
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

// ConfigMapDataSource reads one ConfigMap without changing it. Missing objects
// return lookup metadata, empty data maps, and the configured immutable value.
type ConfigMapDataSource struct {
	// Metadata selects only by namespace and name. Namespace defaults to default;
	// name is optional. Annotation and label maps do not select objects.
	Metadata ConfigMapLookupMetadata
	// Immutable does not affect the lookup; it is returned only for missing objects.
	Immutable *bool
}

type ConfigMapLookupMetadata struct {
	Name        *string
	Namespace   *string
	Annotations *map[string]string
	Labels      *map[string]string
}

// ConfigMapDataSourceOutput contains live, unfiltered values. Changes between
// plan and apply require a new plan. Data and BinaryData are not sensitive.
type ConfigMapDataSourceOutput struct {
	Metadata ConfigMapLookupMetadataOutput
	Data     map[string]string
	// BinaryData contains standard padded-base64 encodings of remote bytes.
	BinaryData map[string]string
	Immutable  bool
}

type ConfigMapLookupMetadataOutput struct {
	Name            string
	Namespace       string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

var _ runtime.TypedDataSource[
	*ConfigMapDataSourceOutput, *config.Configuration,
] = (*ConfigMapDataSource)(nil)

func (r ConfigMapDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *ConfigMapDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ConfigMapDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, err := client.New(c)
	if err != nil {
		return nil, err
	}
	namespace := serviceDefault(r.Metadata.Namespace, "default")
	name := serviceValue(r.Metadata.Name)
	obj, err := api.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &ConfigMapDataSourceOutput{
			Metadata: ConfigMapLookupMetadataOutput{
				Name: name, Namespace: namespace,
				Annotations: maps.Clone(serviceMap(r.Metadata.Annotations)),
				Labels:      maps.Clone(serviceMap(r.Metadata.Labels)),
			},
			Data: map[string]string{}, BinaryData: map[string]string{},
			Immutable: serviceValue(r.Immutable),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ConfigMap %s/%s: %w", namespace, name, err)
	}
	binary := make(map[string]string, len(obj.BinaryData))
	for key, value := range obj.BinaryData {
		binary[key] = base64.StdEncoding.EncodeToString(value)
	}
	return &ConfigMapDataSourceOutput{
		Metadata: ConfigMapLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion,
			Annotations: serviceMap(&obj.Annotations), Labels: serviceMap(&obj.Labels),
		},
		Data: serviceMap(&obj.Data), BinaryData: binary, Immutable: serviceValue(obj.Immutable),
	}, nil
}

func (r ConfigMapDataSource) Constraints() []constraint.Constraint {
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
