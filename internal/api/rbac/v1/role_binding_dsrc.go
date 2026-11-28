package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// RoleBindingDataSource reads one live RoleBinding without changing it.
type RoleBindingDataSource struct {
	// Metadata selects the required name and optional namespace, which defaults to default.
	Metadata RoleBindingLookupMetadata
}

type RoleBindingLookupMetadata struct {
	Name      string
	Namespace *string
}

// RoleBindingDataSourceOutput contains live metadata and unmodified RBAC content.
// RoleRef and Subjects are null only when the requested RoleBinding does not exist.
type RoleBindingDataSourceOutput struct {
	Metadata RoleBindingLookupMetadataOutput
	RoleRef  *RoleBindingLookupRoleRef
	Subjects *[]RoleBindingLookupSubject
}

type RoleBindingLookupMetadataOutput struct {
	Name            string
	Namespace       string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

type RoleBindingLookupRoleRef struct {
	APIGroup string
	Kind     string
	Name     string
}

type RoleBindingLookupSubject struct {
	Kind      string
	APIGroup  string
	Name      string
	Namespace string
}

func (r RoleBindingDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r RoleBindingDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.NotEmpty(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name, "^[^/%]*$"),
			constraint.Not(constraint.OneOf(r.Metadata.Name, ".", "..")),
		),
		constraint.When(constraint.Present(r.Metadata.Namespace)).Require(
			constraint.IsMatch(r.Metadata.Namespace, "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"),
			constraint.AtMost(constraint.Length(r.Metadata.Namespace), 63),
		),
	}
}

func (r *RoleBindingDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*RoleBindingDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace := "default"
	if r.Metadata.Namespace != nil {
		namespace = *r.Metadata.Namespace
	}
	out, err := func() (*RoleBindingDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.RbacV1().RoleBindings(namespace).Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if apierrors.IsNotFound(err) {
			return &RoleBindingDataSourceOutput{Metadata: RoleBindingLookupMetadataOutput{
				Name: r.Metadata.Name, Namespace: namespace,
				Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return roleBindingLookupOutput(obj), nil
	}()
	if err != nil {
		return nil, fmt.Errorf("get RoleBinding %s/%s: %w", namespace, r.Metadata.Name, err)
	}
	return out, nil
}

func roleBindingLookupOutput(obj *rbacv1.RoleBinding) *RoleBindingDataSourceOutput {
	subjects := make([]RoleBindingLookupSubject, len(obj.Subjects))
	for i, subject := range obj.Subjects {
		subjects[i] = RoleBindingLookupSubject{
			Kind: subject.Kind, APIGroup: subject.APIGroup,
			Name: subject.Name, Namespace: subject.Namespace,
		}
	}
	out := &RoleBindingDataSourceOutput{
		Metadata: RoleBindingLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		RoleRef: &RoleBindingLookupRoleRef{
			APIGroup: obj.RoleRef.APIGroup, Kind: obj.RoleRef.Kind, Name: obj.RoleRef.Name,
		},
		Subjects: &subjects,
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	return out
}
