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

type RoleDataSource struct {
	Metadata RoleLookupMetadata
}

type RoleLookupMetadata struct {
	Name      string
	Namespace *string
}

type RoleDataSourceOutput struct {
	Metadata RoleLookupMetadataOutput
	Rules    *[]RoleLookupRule
}

type RoleLookupMetadataOutput struct {
	Name            string
	Namespace       string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

type RoleLookupRule struct {
	APIGroups       []string
	Resources       []string
	Verbs           []string
	ResourceNames   []string
	NonResourceUrls []string
}

func (r RoleDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r RoleDataSource) Constraints() []constraint.Constraint {
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

func (r *RoleDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*RoleDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace := "default"
	if r.Metadata.Namespace != nil {
		namespace = *r.Metadata.Namespace
	}
	out, err := func() (*RoleDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.RbacV1().Roles(namespace).Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &RoleDataSourceOutput{Metadata: RoleLookupMetadataOutput{
				Name: r.Metadata.Name, Namespace: namespace,
				Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return roleLookupOutput(obj), nil
	}()
	if err != nil {
		return nil, fmt.Errorf("get Role %s/%s: %w", namespace, r.Metadata.Name, err)
	}
	return out, nil
}

func roleLookupOutput(obj *rbacv1.Role) *RoleDataSourceOutput {
	rules := make([]RoleLookupRule, len(obj.Rules))
	for i, rule := range obj.Rules {
		rules[i] = RoleLookupRule{
			APIGroups:       append([]string{}, rule.APIGroups...),
			Resources:       append([]string{}, rule.Resources...),
			Verbs:           append([]string{}, rule.Verbs...),
			ResourceNames:   append([]string{}, rule.ResourceNames...),
			NonResourceUrls: append([]string{}, rule.NonResourceURLs...),
		}
	}
	out := &RoleDataSourceOutput{
		Metadata: RoleLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		}, Rules: &rules,
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	return out
}
