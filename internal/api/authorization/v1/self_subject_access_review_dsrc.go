package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// SelfSubjectAccessReviewDataSource observes access for the configured identity.
// A negative decision and any evaluation error are returned as data. The query
// does not create a permission grant. Choose exactly one resource or non-resource
// attribute object; subject identity comes from shared client configuration.
type SelfSubjectAccessReviewDataSource struct {
	// ResourceAttributes selects a resource request. An omitted or empty namespace
	// queries all namespaces and does not inherit the configured namespace.
	ResourceAttributes    *AccessReviewResourceAttributes
	NonResourceAttributes *AccessReviewNonResourceAttributes
}

type AccessReviewResourceAttributes struct {
	// An omitted or empty namespace queries all namespaces; it does not inherit
	// the library or kubeconfig namespace.
	Namespace   *string
	Verb        string
	Group       *string
	Version     *string
	Resource    string
	Subresource *string
	Name        *string
}

type AccessReviewNonResourceAttributes struct {
	Verb string
	Path string
}

type SelfSubjectAccessReviewDataSourceOutput struct {
	Allowed         bool
	Denied          bool
	Reason          string
	EvaluationError string
}

var _ runtime.TypedDataSource[
	*SelfSubjectAccessReviewDataSourceOutput, *config.Configuration,
] = (*SelfSubjectAccessReviewDataSource)(nil)

func (r *SelfSubjectAccessReviewDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*SelfSubjectAccessReviewDataSourceOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("review Kubernetes self access: %w", err)
	}
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("review Kubernetes self access: %w", err)
	}
	spec := authorizationv1.SelfSubjectAccessReviewSpec{}
	if a := r.ResourceAttributes; a != nil {
		spec.ResourceAttributes = &authorizationv1.ResourceAttributes{
			Namespace: ptr.Deref(a.Namespace, ""), Verb: a.Verb, Group: ptr.Deref(a.Group, ""),
			Version: ptr.Deref(a.Version, ""), Resource: a.Resource,
			Subresource: ptr.Deref(a.Subresource, ""), Name: ptr.Deref(a.Name, ""),
		}
	}
	if a := r.NonResourceAttributes; a != nil {
		spec.NonResourceAttributes = &authorizationv1.NonResourceAttributes{
			Verb: a.Verb, Path: a.Path,
		}
	}
	review, err := api.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
		&authorizationv1.SelfSubjectAccessReview{Spec: spec}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("review Kubernetes self access: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("review Kubernetes self access: %w", err)
	}
	return &SelfSubjectAccessReviewDataSourceOutput{
		Allowed: review.Status.Allowed, Denied: review.Status.Denied,
		Reason: review.Status.Reason, EvaluationError: review.Status.EvaluationError,
	}, nil
}

func (r SelfSubjectAccessReviewDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.ResourceAttributes, r.NonResourceAttributes),
		constraint.When(constraint.Present(r.ResourceAttributes)).Require(
			constraint.NotEmpty(r.ResourceAttributes.Verb),
			constraint.NotEmpty(r.ResourceAttributes.Resource),
		).Message("resource-attributes verb and resource must be nonempty"),
		constraint.When(constraint.Present(r.NonResourceAttributes)).Require(
			constraint.NotEmpty(r.NonResourceAttributes.Verb),
			constraint.NotEmpty(r.NonResourceAttributes.Path),
			constraint.IsMatch(r.NonResourceAttributes.Path, `^/`),
		).Message("non-resource-attributes requires a nonempty verb and an absolute path"),
	}
}
