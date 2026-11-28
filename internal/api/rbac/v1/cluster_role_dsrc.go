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

// ClusterRoleDataSource reads one cluster-scoped ClusterRole by name.
type ClusterRoleDataSource struct {
	Metadata ClusterRoleLookupMetadata
}

// ClusterRoleLookupMetadata selects the exact ClusterRole name.
type ClusterRoleLookupMetadata struct {
	Name string
}

// ClusterRoleDataSourceOutput contains live metadata and nullable observed content.
type ClusterRoleDataSourceOutput struct {
	Metadata        ClusterRoleLookupMetadataOutput
	Rules           *[]RoleLookupRule
	AggregationRule *ClusterRoleLookupAggregation
}

// ClusterRoleLookupMetadataOutput contains bounded cluster-scoped live metadata.
type ClusterRoleLookupMetadataOutput struct {
	Name            string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

// ClusterRoleLookupAggregation preserves the observed selector sequence.
type ClusterRoleLookupAggregation struct {
	ClusterRoleSelectors []ClusterRoleLookupSelector
}

// ClusterRoleLookupSelector contains observed labels and ordered expressions.
type ClusterRoleLookupSelector struct {
	MatchLabels      map[string]string
	MatchExpressions []ClusterRoleLookupExpression
}

// ClusterRoleLookupExpression preserves observed key, operator and value order.
type ClusterRoleLookupExpression struct {
	Key      string
	Operator string
	Values   []string
}

func (r ClusterRoleDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.NotEmpty(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name, "^[^/%]*$"),
			constraint.Not(constraint.OneOf(r.Metadata.Name, ".", "..")),
		),
	}
}

func (r *ClusterRoleDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ClusterRoleDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	out, err := func() (*ClusterRoleDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.RbacV1().ClusterRoles().Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if apierrors.IsNotFound(err) {
			return &ClusterRoleDataSourceOutput{Metadata: ClusterRoleLookupMetadataOutput{
				Name: r.Metadata.Name, Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return clusterRoleLookupOutput(obj), nil
	}()
	if err != nil {
		return nil, fmt.Errorf("get ClusterRole %s: %w", r.Metadata.Name, err)
	}
	return out, nil
}

func clusterRoleLookupOutput(obj *rbacv1.ClusterRole) *ClusterRoleDataSourceOutput {
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
	out := &ClusterRoleDataSourceOutput{
		Metadata: ClusterRoleLookupMetadataOutput{
			Name: obj.Name, GenerateName: obj.GenerateName, UID: string(obj.UID),
			ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		}, Rules: &rules,
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	if obj.AggregationRule != nil {
		selectors := make([]ClusterRoleLookupSelector, len(obj.AggregationRule.ClusterRoleSelectors))
		for i, selector := range obj.AggregationRule.ClusterRoleSelectors {
			expressions := make([]ClusterRoleLookupExpression, len(selector.MatchExpressions))
			for j, expression := range selector.MatchExpressions {
				expressions[j] = ClusterRoleLookupExpression{
					Key: expression.Key, Operator: string(expression.Operator),
					Values: append([]string{}, expression.Values...),
				}
			}
			labels := map[string]string{}
			maps.Copy(labels, selector.MatchLabels)
			selectors[i] = ClusterRoleLookupSelector{MatchLabels: labels, MatchExpressions: expressions}
		}
		out.AggregationRule = &ClusterRoleLookupAggregation{ClusterRoleSelectors: selectors}
	}
	return out
}
