package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// IngressClassDataSource reads a cluster-scoped IngressClass by name.
type IngressClassDataSource struct {
	Metadata IngressClassLookupMetadata
}

type IngressClassLookupMetadata struct {
	Name string
}

type IngressClassDataSourceOutput struct {
	Metadata IngressClassLookupMetadataOutput
	// Spec is null only when the named IngressClass does not exist.
	Spec *IngressClassLookupSpec
}

type IngressClassLookupMetadataOutput struct {
	Name            string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

type IngressClassLookupSpec struct {
	Controller string
	Parameters *IngressClassLookupParameters
}

type IngressClassLookupParameters struct {
	APIGroup  *string
	Kind      string
	Name      string
	Scope     *string
	Namespace *string
}

func (r *IngressClassDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*IngressClassDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	out, err := func() (*IngressClassDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.NetworkingV1().IngressClasses().Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if apierrors.IsNotFound(err) {
			return &IngressClassDataSourceOutput{Metadata: IngressClassLookupMetadataOutput{
				Name: r.Metadata.Name, Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return ingressClassLookupOutput(obj), nil
	}()
	if err != nil {
		return nil, fmt.Errorf("get IngressClass %s: %w", r.Metadata.Name, err)
	}
	return out, nil
}

func (r IngressClassDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.Present(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.AtMost(constraint.Length(r.Metadata.Name), 253),
		),
	}
}

func ingressClassLookupOutput(obj *networkingv1.IngressClass) *IngressClassDataSourceOutput {
	out := &IngressClassDataSourceOutput{
		Metadata: IngressClassLookupMetadataOutput{
			Name: obj.Name, GenerateName: obj.GenerateName, UID: string(obj.UID),
			ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &IngressClassLookupSpec{Controller: obj.Spec.Controller},
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	if p := obj.Spec.Parameters; p != nil {
		p = p.DeepCopy()
		out.Spec.Parameters = &IngressClassLookupParameters{
			APIGroup: p.APIGroup, Kind: p.Kind, Name: p.Name, Scope: p.Scope, Namespace: p.Namespace,
		}
	}
	return out
}
