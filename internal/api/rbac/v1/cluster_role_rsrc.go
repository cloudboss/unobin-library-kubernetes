package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedrbacv1 "k8s.io/client-go/kubernetes/typed/rbac/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// ClusterRoleResource manages a cluster-scoped RBAC ClusterRole with JSON Patch.
// Writes refresh remote values without waiting for aggregation controllers.
// Delete returns after the API accepts the request. Each operation has a
// 20-minute deadline, subject to an earlier caller deadline or cancellation.
type ClusterRoleResource struct {
	capturePrior *clusterRolePrior `ub:"-"`
	// Metadata selects immutable name or generated-name prefix and managed maps.
	Metadata ClusterRoleMetadata
	// Rule preserves order and duplicates. Omission or an empty list leaves
	// existing rules unmanaged and exposes remote rules, including aggregation.
	Rule *[]ClusterRoleRule
	// AggregationRule selects ClusterRoles whose permissions a controller combines.
	// Removing it replaces the remote aggregation with an empty object.
	AggregationRule *ClusterRoleAggregation
	// IgnoreAnnotations filters untracked annotation keys during reads.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked label keys during reads.
	IgnoreLabels *[]string
}

// ClusterRoleMetadata contains cluster-scoped identity and managed metadata.
// Inputs require exactly one nonempty name or generated-name prefix.
// Reads and deletes use saved concrete identity for existing state.
type ClusterRoleMetadata struct {
	Name         *string
	GenerateName *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

// ClusterRoleRule preserves policy member order and duplicate values.
// Verbs is required but can be empty. Explicit api-groups must be nonempty.
// Kubernetes validates permission combinations and custom strings.
type ClusterRoleRule struct {
	Verbs           []string
	APIGroups       *[]string
	Resources       *[]string
	ResourceNames   *[]string
	NonResourceUrls *[]string
}

// ClusterRoleAggregation contains ordered alternative label selectors.
type ClusterRoleAggregation struct {
	ClusterRoleSelectors *[]ClusterRoleSelector
}

// ClusterRoleSelector contains ordered requirements that must all match.
// An empty selector matches all ClusterRoles.
type ClusterRoleSelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]ClusterRoleExpression
}

// ClusterRoleExpression uses set semantics for values. Kubernetes validates
// operators and their combinations with keys and values.
type ClusterRoleExpression struct {
	Key      *string
	Operator *string
	Values   *[]string
}

// ClusterRoleResourceOutput contains saved identity and stable remote values.
// Volatile server metadata and namespace are excluded.
type ClusterRoleResourceOutput struct {
	Server          string
	APIVersion      string
	Kind            string
	Name            string
	UID             string
	Metadata        ClusterRoleMetadataOutput
	Rule            []ClusterRoleRuleOutput
	AggregationRule *ClusterRoleAggregationOutput
}

// ClusterRoleMetadataOutput contains concrete identity and filtered metadata.
type ClusterRoleMetadataOutput struct {
	Name         string
	GenerateName string
	Annotations  map[string]string
	Labels       map[string]string
}

// ClusterRoleRuleOutput preserves all rule and member ordering.
type ClusterRoleRuleOutput struct {
	Verbs           []string
	APIGroups       []string
	Resources       []string
	ResourceNames   []string
	NonResourceUrls []string
}

// ClusterRoleAggregationOutput contains normalized remote selectors.
type ClusterRoleAggregationOutput struct {
	ClusterRoleSelectors []ClusterRoleSelectorOutput
}

// ClusterRoleSelectorOutput contains normalized label selector requirements.
type ClusterRoleSelectorOutput struct {
	MatchLabels      map[string]string
	MatchExpressions []ClusterRoleExpressionOutput
}

// ClusterRoleExpressionOutput contains sorted unique values.
type ClusterRoleExpressionOutput struct {
	Key      string
	Operator string
	Values   []string
}

type clusterRolePrior = runtime.Prior[
	ClusterRoleResource, *ClusterRoleResourceOutput, *config.Configuration,
]

func (r *ClusterRoleResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ClusterRoleResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, server, err := clusterRoleClient(c, nil)
	if err != nil {
		return nil, err
	}
	metadata := normalizeClusterRoleMetadata(r.Metadata)
	created, err := api.Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: metadata.Name, GenerateName: metadata.GenerateName,
			Annotations: metadata.Annotations, Labels: metadata.Labels},
		Rules:           nativeClusterRoleRules(normalizeClusterRoleRules(r.Rule)),
		AggregationRule: nativeClusterRoleAggregation(normalizeClusterRoleAggregation(r.AggregationRule)),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create ClusterRole: %w", err)
	}
	saved := clusterRoleIdentity(created, server)
	saved.Metadata = metadata
	saved.AggregationRule = normalizeClusterRoleAggregation(r.AggregationRule)
	return r.refresh(ctx, c, saved)
}

func (r *ClusterRoleResource) Read(
	ctx context.Context, c *config.Configuration, p clusterRolePrior,
) (*ClusterRoleResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		return p.Outputs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	return r.refresh(ctx, c, p.Outputs)
}

func (r *ClusterRoleResource) refresh(
	ctx context.Context, c *config.Configuration, saved *ClusterRoleResourceOutput,
) (*ClusterRoleResourceOutput, error) {
	api, _, err := clusterRoleClient(c, saved)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, fmt.Errorf("missing ClusterRole identity in prior state")
	}
	var observed *rbacv1.ClusterRole
	for range 2 {
		observed, err = api.Get(ctx, saved.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("read ClusterRole: %w", runtime.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read ClusterRole: %w", err)
		}
		if err := object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, observed); err != nil {
			return nil, err
		}
	}
	annotations, err := projectClusterRoleMetadata(observed.Annotations,
		saved.Metadata.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectClusterRoleMetadata(observed.Labels, saved.Metadata.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	out := clusterRoleIdentity(observed, saved.Server)
	out.Metadata = ClusterRoleMetadataOutput{Name: observed.Name, GenerateName: observed.GenerateName,
		Annotations: annotations, Labels: labels}
	out.Rule = projectClusterRoleRules(observed.Rules)
	out.AggregationRule = saved.AggregationRule
	if observed.AggregationRule != nil {
		out.AggregationRule = projectClusterRoleAggregation(observed.AggregationRule)
	}
	return out, nil
}

func (r *ClusterRoleResource) Update(
	ctx context.Context, c *config.Configuration, p clusterRolePrior,
) (*ClusterRoleResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing ClusterRole identity in prior state")
	}
	metadata := normalizeClusterRoleMetadata(r.Metadata)
	if r.Metadata.Name != nil && metadata.Name != p.Outputs.Name ||
		roleValue(p.Inputs.Metadata.GenerateName) != metadata.GenerateName {
		return nil, fmt.Errorf("ClusterRole identity changed; replacement is required")
	}
	api, _, err := clusterRoleClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	operations := []rolePatchOperation{{Op: "test", Path: "/metadata/uid", Value: p.Outputs.UID}}
	operations = append(operations, roleMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, metadata.Annotations)...)
	operations = append(operations, roleMapPatch("/metadata/labels",
		previous.Metadata.Labels, metadata.Labels)...)
	rules := normalizeClusterRoleRules(r.Rule)
	if len(rules) > 0 && !reflect.DeepEqual(previous.Rule, rules) {
		operations = append(operations, clusterRoleRulePatch(previous.Rule, rules)...)
	}
	aggregation := normalizeClusterRoleAggregation(r.AggregationRule)
	if !reflect.DeepEqual(previous.AggregationRule, aggregation) {
		native := nativeClusterRoleAggregation(aggregation)
		if native == nil {
			native = &rbacv1.AggregationRule{}
		}
		operations = append(operations, rolePatchOperation{Op: "replace", Path: "/aggregationRule",
			Value: native})
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode ClusterRole patch: %w", err)
	}
	updated, err := api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch ClusterRole: %w", err)
	}
	if err := object.CheckUID(object.Target{Name: p.Outputs.Name, UID: p.Outputs.UID},
		updated); err != nil {
		return nil, err
	}
	seed := *p.Outputs
	seed.Metadata = metadata
	seed.AggregationRule = aggregation
	return r.refresh(ctx, c, &seed)
}

func (r *ClusterRoleResource) Delete(
	ctx context.Context, c *config.Configuration, p clusterRolePrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing ClusterRole identity in prior state")
	}
	api, _, err := clusterRoleClient(c, p.Outputs)
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.UID)
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if directClusterRoleNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete ClusterRole: %w", err)
	}
	return nil
}

func directClusterRoleNotFound(err error) bool {
	status, ok := err.(*apierrors.StatusError)
	return ok && apierrors.IsNotFound(status)
}

func clusterRoleClient(c *config.Configuration, saved *ClusterRoleResourceOutput,
) (typedrbacv1.ClusterRoleInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "rbac.authorization.k8s.io/v1" ||
			saved.Kind != "ClusterRole" || saved.Name == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing ClusterRole identity in prior state")
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedrbacv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create ClusterRole client: %w", err)
	}
	return api.ClusterRoles(), resolved.Host, nil
}

func clusterRoleIdentity(observed *rbacv1.ClusterRole, server string) *ClusterRoleResourceOutput {
	return &ClusterRoleResourceOutput{Server: server, APIVersion: "rbac.authorization.k8s.io/v1",
		Kind: "ClusterRole", Name: observed.Name, UID: string(observed.UID)}
}

func (r ClusterRoleResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.When(constraint.Present(r.Metadata.Name)).
			Require(constraint.NotEmpty(r.Metadata.Name)).
			Message("metadata.name must not be empty"),
		constraint.When(constraint.Present(r.Metadata.GenerateName)).
			Require(constraint.NotEmpty(r.Metadata.GenerateName)).
			Message("metadata.generate-name must not be empty"),
		constraint.Must(
			constraint.Matches(r.Metadata.Name, `^[^/%]*$`),
			constraint.Not(constraint.OneOf(r.Metadata.Name, ".", "..")),
		).Message("metadata.name must be an RBAC path segment"),
		constraint.Must(
			constraint.Matches(r.Metadata.GenerateName, `^[^/%]*$`),
			constraint.Not(constraint.OneOf(r.Metadata.GenerateName, ".", "..")),
		).Message("metadata.generate-name must be an RBAC path segment"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.Matches(entry.Key, `^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
							`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
							`[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`),
						constraint.Matches(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.labels keys must be Kubernetes qualified names"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.Matches(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`,
					)).Message("metadata.labels values must be valid Kubernetes label values"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.Matches(entry.Key, `^([A-Za-z0-9İK]([-A-Za-z0-9İK]*`+
							`[A-Za-z0-9İK])?(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*`+
							`[A-Za-z0-9İK])?)*/)?[A-Za-z0-9İK]`+
							`([-A-Za-z0-9İK_.]{0,61}[A-Za-z0-9İK])?$`),
						constraint.Matches(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.annotations keys must be qualified names after lowercasing"),
				}
			},
		),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsRegex(pattern)).
					Message("ignore-annotations entries must be valid regular expressions"),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsRegex(pattern)).
					Message("ignore-labels entries must be valid regular expressions"),
			}
		}),
		constraint.ForEach(r.Rule, func(rule ClusterRoleRule) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.When(constraint.Present(rule.APIGroups)).
					Require(constraint.MinItems(rule.APIGroups, 1)),
			}
		}),
	}
}
