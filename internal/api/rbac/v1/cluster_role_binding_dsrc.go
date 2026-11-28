package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// ClusterRoleBindingDataSource reads one live ClusterRoleBinding without changing it.
type ClusterRoleBindingDataSource struct {
	// Metadata selects the exact cluster-scoped name.
	Metadata ClusterRoleBindingLookupMetadata
}

type ClusterRoleBindingLookupMetadata struct {
	Name string
}

// ClusterRoleBindingDataSourceOutput contains live metadata and unmodified RBAC content.
// RoleRef and Subjects are null only when the requested ClusterRoleBinding does not exist.
type ClusterRoleBindingDataSourceOutput struct {
	Metadata ClusterRoleBindingLookupMetadataOutput
	RoleRef  *ClusterRoleBindingLookupRoleRef
	Subjects *[]ClusterRoleBindingLookupSubject
}

type ClusterRoleBindingLookupMetadataOutput struct {
	Name            string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

type ClusterRoleBindingLookupRoleRef struct {
	APIGroup string
	Kind     string
	Name     string
}

type ClusterRoleBindingLookupSubject struct {
	Kind      string
	APIGroup  string
	Name      string
	Namespace string
}

func (r ClusterRoleBindingDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.NotEmpty(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name, "^[^/%]*$"),
			constraint.Not(constraint.OneOf(r.Metadata.Name, ".", "..")),
		),
	}
}

func (r *ClusterRoleBindingDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ClusterRoleBindingDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	out, err := func() (*ClusterRoleBindingDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.RbacV1().ClusterRoleBindings().Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if apierrors.IsNotFound(err) {
			return &ClusterRoleBindingDataSourceOutput{Metadata: ClusterRoleBindingLookupMetadataOutput{
				Name:   r.Metadata.Name,
				Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return clusterRoleBindingLookupOutput(obj), nil
	}()
	if err != nil {
		return nil, fmt.Errorf("get ClusterRoleBinding %s: %w", r.Metadata.Name, err)
	}
	return out, nil
}

func clusterRoleBindingLookupOutput(
	obj *rbacv1.ClusterRoleBinding,
) *ClusterRoleBindingDataSourceOutput {
	subjects := make([]ClusterRoleBindingLookupSubject, len(obj.Subjects))
	for i, subject := range obj.Subjects {
		subjects[i] = ClusterRoleBindingLookupSubject{
			Kind: subject.Kind, APIGroup: subject.APIGroup,
			Name: subject.Name, Namespace: subject.Namespace,
		}
	}
	out := &ClusterRoleBindingDataSourceOutput{
		Metadata: ClusterRoleBindingLookupMetadataOutput{
			Name: obj.Name, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		RoleRef: &ClusterRoleBindingLookupRoleRef{
			APIGroup: obj.RoleRef.APIGroup, Kind: obj.RoleRef.Kind, Name: obj.RoleRef.Name,
		},
		Subjects: &subjects,
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	return out
}
