package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// IngressDataSource reads live routing, TLS, and status for a named Ingress.
type IngressDataSource struct {
	// Metadata selects by name and namespace. Omitted or null namespace selects default.
	// Annotations and labels are returned on a miss.
	Metadata IngressLookupMetadata
}

type IngressLookupMetadata struct {
	Name        *string
	Namespace   *string
	Annotations *map[string]*string
	Labels      *map[string]*string
}

type IngressDataSourceOutput struct {
	Metadata IngressLookupMetadataOutput
	// Spec returns the API routing and TLS configuration. It is null on a miss.
	Spec *IngressLookupSpec
	// Status returns the live load balancer endpoints. It is null on a miss.
	Status *IngressStatus
}

type IngressLookupMetadataOutput struct {
	Name            string
	Namespace       string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

type IngressLookupSpec struct {
	IngressClassName *string
	DefaultBackend   *IngressBackend
	Rule             []IngressLookupRule
	TLS              []IngressLookupTLS
}

type IngressLookupRule struct {
	Host string
	HTTP *IngressLookupHTTP
}

type IngressLookupHTTP struct {
	Path []IngressLookupPath
}

type IngressLookupPath struct {
	Path     string
	PathType *string
	Backend  IngressBackend
}

type IngressLookupTLS struct {
	Hosts      []string
	SecretName string
}

func (r IngressDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *IngressDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*IngressDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	name := value(r.Metadata.Name)
	namespace := "default"
	if r.Metadata.Namespace != nil {
		namespace = *r.Metadata.Namespace
	}
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("get Ingress %s/%s: %w", namespace, name, err)
	}
	obj, err := api.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &IngressDataSourceOutput{Metadata: IngressLookupMetadataOutput{
			Name: name, Namespace: namespace,
			Annotations: ingressLookupMap(r.Metadata.Annotations),
			Labels:      ingressLookupMap(r.Metadata.Labels),
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Ingress %s/%s: %w", namespace, name, err)
	}
	annotations, labels := obj.Annotations, obj.Labels
	if annotations == nil {
		annotations = map[string]string{}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return &IngressDataSourceOutput{
		Metadata: IngressLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion,
			Annotations: annotations, Labels: labels,
		},
		Spec:   ingressLookupSpec(obj.Spec),
		Status: new(readIngressStatus(obj.Status)),
	}, nil
}

func (r IngressDataSource) Constraints() []constraint.Constraint {
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
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
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
