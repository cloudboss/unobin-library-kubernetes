package v1

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// SecretDataSource reads one named Secret and partitions its payload by key.
type SecretDataSource struct {
	Metadata SecretLookupMetadata
	// BinaryData selects live keys by presence of a non-null value.
	BinaryData *map[string]*string `ub:",sensitive"`
}

type SecretLookupMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]*string
	Labels       *map[string]*string
}

type SecretDataSourceOutput struct {
	Metadata   SecretLookupMetadataOutput
	Data       map[string]string `ub:",sensitive"`
	BinaryData map[string]string `ub:",sensitive"`
	Type       string
	Immutable  bool
}

type SecretLookupMetadataOutput struct {
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
	*SecretDataSourceOutput, *config.Configuration,
] = (*SecretDataSource)(nil)

func (r SecretDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *SecretDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*SecretDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	api, err := client.New(c)
	if err != nil {
		return nil, err
	}
	namespace := serviceDefault(r.Metadata.Namespace, "default")
	name := serviceValue(r.Metadata.Name)
	obj, err := api.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &SecretDataSourceOutput{
			Metadata: SecretLookupMetadataOutput{
				Name: name, Namespace: namespace, GenerateName: serviceValue(r.Metadata.GenerateName),
				Annotations: namespaceLookupMap(r.Metadata.Annotations),
				Labels:      namespaceLookupMap(r.Metadata.Labels),
			},
			Data: map[string]string{}, BinaryData: namespaceLookupMap(r.BinaryData),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
	}
	return projectSecretLookup(obj, namespaceLookupMap(r.BinaryData)), nil
}

func projectSecretLookup(
	obj *corev1.Secret, binary map[string]string,
) *SecretDataSourceOutput {
	for key := range binary {
		binary[key] = base64.StdEncoding.EncodeToString(obj.Data[key])
	}
	data := make(map[string]string, len(obj.Data))
	for key, value := range obj.Data {
		if _, selected := binary[key]; !selected {
			// Valid UTF-8 keeps saved observations stable across JSON encoding.
			// Rune iteration replaces each malformed byte separately.
			data[key] = strings.Map(func(r rune) rune { return r }, string(value))
		}
	}
	return &SecretDataSourceOutput{
		Metadata: SecretLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion, UID: string(obj.UID),
			Annotations: serviceMap(&obj.Annotations), Labels: serviceMap(&obj.Labels),
		},
		Data: data, BinaryData: binary, Type: string(obj.Type), Immutable: serviceValue(obj.Immutable),
	}
}

func (r SecretDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|([a-z0-9][-a-z0-9]{0,61})?[\x00-\x7F]-)$`)),
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
