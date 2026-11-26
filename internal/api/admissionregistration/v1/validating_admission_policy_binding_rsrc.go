package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	typedv1 "k8s.io/client-go/kubernetes/typed/admissionregistration/v1"
	"k8s.io/client-go/util/retry"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
	"github.com/cloudboss/unobin/pkg/runtime"
)

// ValidatingAdmissionPolicyBindingResource connects an admission policy
// to matching requests and optional parameter objects. It is cluster-scoped and
// does not create its policy or parameter objects. Validation actions are Deny,
// Warn and Audit; Deny and Warn cannot be combined.
type ValidatingAdmissionPolicyBindingResource struct {
	Metadata PolicyMetadata
	Spec     BindingSpec
}

type BindingSpec struct {
	PolicyName string
	// ValidationActions selects Deny, Warn or Audit. Deny and Warn cannot be combined.
	ValidationActions []string
	MatchResources    *BindingMatchResources
	ParamRef          *BindingParamRef
}

type BindingMatchResources struct {
	MatchPolicy          *string
	NamespaceSelector    *BindingSelector
	ObjectSelector       *BindingSelector
	ResourceRules        *[]PolicyResourceRule
	ExcludeResourceRules *[]PolicyResourceRule
}

type BindingSelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]BindingSelectorExpression
}

type BindingSelectorExpression struct {
	Key      string
	Operator string
	Values   *[]string
}

type BindingParamRef struct {
	Name                    *string
	Selector                *BindingSelector
	Namespace               *string
	ParameterNotFoundAction string
}

type ValidatingAdmissionPolicyBindingResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   PolicyMetadataOutput
	Spec       BindingSpec
}

type bindingPrior = runtime.Prior[
	ValidatingAdmissionPolicyBindingResource, *ValidatingAdmissionPolicyBindingResourceOutput,
	*config.Configuration,
]

const (
	bindingKind       = "ValidatingAdmissionPolicyBinding"
	bindingAPIVersion = "admissionregistration.k8s.io/v1"
	bindingTimeout    = 20 * time.Minute
)

func (r *ValidatingAdmissionPolicyBindingResource) Create(
	ctx context.Context, cfg *config.Configuration,
) (*ValidatingAdmissionPolicyBindingResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, bindingTimeout)
	defer cancel()
	api, server, err := bindingClient(cfg, nil)
	if err != nil {
		return nil, err
	}
	live, err := api.Create(ctx, &admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: policyValue(r.Metadata.Name), GenerateName: policyValue(r.Metadata.GenerateName),
			Labels: policyValue(r.Metadata.Labels), Annotations: policyValue(r.Metadata.Annotations),
		},
		Spec: nativeBindingSpec(r.Spec),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", bindingKind, err)
	}
	return bindingOutput(live, server)
}

func (r *ValidatingAdmissionPolicyBindingResource) Read(
	ctx context.Context, cfg *config.Configuration, p bindingPrior,
) (*ValidatingAdmissionPolicyBindingResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, bindingTimeout)
	defer cancel()
	if err := checkBindingIdentity(p.Outputs); err != nil {
		return nil, err
	}
	api, _, err := bindingClient(cfg, p.Outputs)
	if err != nil {
		return nil, err
	}
	live, err := api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read %s: %w", bindingKind, runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", bindingKind, err)
	}
	if err := object.CheckUID(bindingTarget(p.Outputs), live); err != nil {
		return nil, err
	}
	return bindingOutput(live, p.Outputs.Server)
}

func (r *ValidatingAdmissionPolicyBindingResource) Update(
	ctx context.Context, cfg *config.Configuration, p bindingPrior,
) (*ValidatingAdmissionPolicyBindingResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, bindingTimeout)
	defer cancel()
	if err := checkBindingIdentity(p.Outputs); err != nil {
		return nil, err
	}
	if policyIdentityChanged(p.Inputs.Metadata, r.Metadata) ||
		r.Metadata.Name != nil && *r.Metadata.Name != p.Outputs.Name {
		return nil, fmt.Errorf("%s identity changed; replacement is required", bindingKind)
	}
	desired := nativeBindingSpec(r.Spec)
	api, _, err := bindingClient(cfg, p.Outputs)
	if err != nil {
		return nil, err
	}
	var updated *admissionv1.ValidatingAdmissionPolicyBinding
	backoff := retry.DefaultRetry
	attempts := 0
	err = wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		attempts++
		live, err := api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("read %s for update: %w", bindingKind, err)
		}
		if err := object.CheckUID(bindingTarget(p.Outputs), live); err != nil {
			return false, err
		}
		live.Spec = *desired.DeepCopy()
		live.Labels = maps.Clone(policyValue(r.Metadata.Labels))
		live.Annotations = maps.Clone(policyValue(r.Metadata.Annotations))
		updated, err = api.Update(ctx, live, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) && attempts < backoff.Steps {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("update %s: %w", bindingKind, err)
		}
		if err := object.CheckUID(bindingTarget(p.Outputs), updated); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return bindingOutput(updated, p.Outputs.Server)
}

func (r *ValidatingAdmissionPolicyBindingResource) Delete(
	ctx context.Context, cfg *config.Configuration, p bindingPrior,
) error {
	if err := checkBindingIdentity(p.Outputs); err != nil {
		return err
	}
	return object.Delete(ctx, cfg, bindingTarget(p.Outputs), metav1.DeleteOptions{}, bindingTimeout)
}

func bindingTarget(saved *ValidatingAdmissionPolicyBindingResourceOutput) object.Target {
	return object.Target{
		Server: saved.Server, APIVersion: saved.APIVersion, Kind: saved.Kind,
		Name: saved.Name, UID: saved.UID,
	}
}

func checkBindingIdentity(saved *ValidatingAdmissionPolicyBindingResourceOutput) error {
	if saved == nil || saved.Server == "" || saved.APIVersion != bindingAPIVersion ||
		saved.Kind != bindingKind || saved.Name == "" || saved.UID == "" {
		return fmt.Errorf("missing %s identity in prior state", bindingKind)
	}
	return nil
}

func bindingClient(
	cfg *config.Configuration, saved *ValidatingAdmissionPolicyBindingResourceOutput,
) (typedv1.ValidatingAdmissionPolicyBindingInterface, string, error) {
	current := policyValue(cfg)
	if saved != nil {
		if err := checkBindingIdentity(saved); err != nil {
			return nil, "", err
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create %s client: %w", bindingKind, err)
	}
	return api.ValidatingAdmissionPolicyBindings(), resolved.Host, nil
}

func bindingOutput(
	live *admissionv1.ValidatingAdmissionPolicyBinding, server string,
) (*ValidatingAdmissionPolicyBindingResourceOutput, error) {
	if live == nil || live.Name == "" || live.UID == "" || server == "" {
		return nil, fmt.Errorf("missing %s identity in API response", bindingKind)
	}
	return &ValidatingAdmissionPolicyBindingResourceOutput{
		Server: server, APIVersion: bindingAPIVersion, Kind: bindingKind,
		Name: live.Name, UID: string(live.UID),
		Metadata: PolicyMetadataOutput{
			Name: live.Name, GenerateName: live.GenerateName,
			Labels: policyOutputMap(&live.Labels), Annotations: policyOutputMap(&live.Annotations),
		},
		Spec: projectBindingSpec(live.Spec),
	}, nil
}

func (r ValidatingAdmissionPolicyBindingResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.Not(constraint.HasKey(r.Metadata, "namespace"))).
			Message("metadata.namespace is forbidden for this cluster-scoped binding"),
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.When(constraint.Present(r.Metadata.Name)).
			Require(constraint.NotEmpty(r.Metadata.Name)).Message("metadata.name must not be empty"),
		constraint.When(constraint.Present(r.Metadata.GenerateName)).
			Require(constraint.NotEmpty(r.Metadata.GenerateName)).
			Message("metadata.generate-name must not be empty"),
		constraint.Must(constraint.NotEmpty(r.Spec.PolicyName)).
			Message("spec.policy-name must not be empty"),
		constraint.Must(constraint.NotEmpty(r.Spec.ValidationActions)).
			Message("spec.validation-actions must not be empty"),
		constraint.Must(constraint.IsUnique(r.Spec.ValidationActions)).
			Message("spec.validation-actions must be unique"),
		constraint.Must(constraint.Not(constraint.All(
			constraint.HasItem(r.Spec.ValidationActions, "Deny"),
			constraint.HasItem(r.Spec.ValidationActions, "Warn"),
		))).Message("spec.validation-actions must not combine Deny and Warn"),
		constraint.ForEach(r.Spec.ValidationActions, func(action string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.OneOf(action, "Deny", "Warn", "Audit")).
					Message("invalid spec.validation-actions item"),
			}
		}),
		constraint.When(constraint.Present(r.Spec.ParamRef)).Require(constraint.Any(
			constraint.All(
				constraint.Present(r.Spec.ParamRef.Name), constraint.Absent(r.Spec.ParamRef.Selector),
			),
			constraint.All(
				constraint.Absent(r.Spec.ParamRef.Name), constraint.Present(r.Spec.ParamRef.Selector),
			),
		)).Message("spec.param-ref requires exactly one name or selector"),
		constraint.When(constraint.Present(r.Spec.ParamRef.Name)).
			Require(constraint.NotEmpty(r.Spec.ParamRef.Name)).
			Message("spec.param-ref.name must not be empty"),
		constraint.When(constraint.Present(r.Spec.ParamRef)).
			Require(constraint.OneOf(r.Spec.ParamRef.ParameterNotFoundAction, "Allow", "Deny")).
			Message("spec.param-ref.parameter-not-found-action must be Allow or Deny"),
		constraint.When(constraint.Present(r.Spec.MatchResources.MatchPolicy)).
			Require(constraint.OneOf(r.Spec.MatchResources.MatchPolicy, "Exact", "Equivalent")).
			Message("spec.match-resources.match-policy must be Exact or Equivalent"),
		constraint.ForEach(r.Spec.MatchResources.NamespaceSelector.MatchExpressions,
			func(e BindingSelectorExpression) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.NotEmpty(e.Key)).Message("selector key must not be empty"),
					constraint.Must(constraint.OneOf(e.Operator, "In", "NotIn", "Exists", "DoesNotExist")).
						Message("invalid selector operator"),
					constraint.When(constraint.OneOf(e.Operator, "In", "NotIn")).
						Require(constraint.NotEmpty(e.Values)).Message("selector operator requires values"),
					constraint.When(constraint.OneOf(e.Operator, "Exists", "DoesNotExist")).
						Require(constraint.MaxItems(e.Values, 0)).Message("selector operator forbids values"),
				}
			}),
		constraint.ForEach(r.Spec.MatchResources.ObjectSelector.MatchExpressions,
			func(e BindingSelectorExpression) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.NotEmpty(e.Key)).Message("selector key must not be empty"),
					constraint.Must(constraint.OneOf(e.Operator, "In", "NotIn", "Exists", "DoesNotExist")).
						Message("invalid selector operator"),
					constraint.When(constraint.OneOf(e.Operator, "In", "NotIn")).
						Require(constraint.NotEmpty(e.Values)).Message("selector operator requires values"),
					constraint.When(constraint.OneOf(e.Operator, "Exists", "DoesNotExist")).
						Require(constraint.MaxItems(e.Values, 0)).Message("selector operator forbids values"),
				}
			}),
		constraint.ForEach(r.Spec.ParamRef.Selector.MatchExpressions,
			func(e BindingSelectorExpression) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.NotEmpty(e.Key)).Message("selector key must not be empty"),
					constraint.Must(constraint.OneOf(e.Operator, "In", "NotIn", "Exists", "DoesNotExist")).
						Message("invalid selector operator"),
					constraint.When(constraint.OneOf(e.Operator, "In", "NotIn")).
						Require(constraint.NotEmpty(e.Values)).Message("selector operator requires values"),
					constraint.When(constraint.OneOf(e.Operator, "Exists", "DoesNotExist")).
						Require(constraint.MaxItems(e.Values, 0)).Message("selector operator forbids values"),
				}
			}),
		constraint.ForEach(r.Spec.MatchResources.ResourceRules,
			func(rule PolicyResourceRule) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.NotEmpty(rule.APIGroups)).
						Message("rule.api-groups must not be empty"),
					constraint.Must(constraint.NotEmpty(rule.APIVersions)).
						Message("rule.api-versions must not be empty"),
					constraint.Must(constraint.NotEmpty(rule.Operations)).
						Message("rule.operations must not be empty"),
					constraint.Must(constraint.NotEmpty(rule.Resources)).
						Message("rule.resources must not be empty"),
					constraint.When(constraint.HasItem(rule.APIGroups, "*")).
						Require(constraint.MaxItems(rule.APIGroups, 1)).
						Message("rule.api-groups wildcard must be the only item"),
					constraint.When(constraint.HasItem(rule.APIVersions, "*")).
						Require(constraint.MaxItems(rule.APIVersions, 1)).
						Message("rule.api-versions wildcard must be the only item"),
					constraint.When(constraint.HasItem(rule.Operations, "*")).
						Require(constraint.MaxItems(rule.Operations, 1)).
						Message("rule.operations wildcard must be the only item"),
					constraint.When(constraint.Present(rule.Scope)).
						Require(constraint.OneOf(rule.Scope, "Cluster", "Namespaced", "*")).
						Message("invalid rule.scope"),
					constraint.ForEach(rule.Operations, func(operation string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.OneOf(operation, "CREATE", "UPDATE", "DELETE", "CONNECT", "*")).
								Message("invalid rule.operations item"),
						}
					}),
					constraint.ForEach(rule.APIVersions, func(item string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.NotEmpty(item)).
								Message("rule.api-versions item must not be empty"),
						}
					}),
					constraint.ForEach(rule.Resources, func(item string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.NotEmpty(item)).Message("rule.resources item must not be empty"),
						}
					}),
				}
			}),
		constraint.ForEach(r.Spec.MatchResources.ExcludeResourceRules,
			func(rule PolicyResourceRule) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.NotEmpty(rule.APIGroups)).
						Message("rule.api-groups must not be empty"),
					constraint.Must(constraint.NotEmpty(rule.APIVersions)).
						Message("rule.api-versions must not be empty"),
					constraint.Must(constraint.NotEmpty(rule.Operations)).
						Message("rule.operations must not be empty"),
					constraint.Must(constraint.NotEmpty(rule.Resources)).
						Message("rule.resources must not be empty"),
					constraint.When(constraint.HasItem(rule.APIGroups, "*")).
						Require(constraint.MaxItems(rule.APIGroups, 1)).
						Message("rule.api-groups wildcard must be the only item"),
					constraint.When(constraint.HasItem(rule.APIVersions, "*")).
						Require(constraint.MaxItems(rule.APIVersions, 1)).
						Message("rule.api-versions wildcard must be the only item"),
					constraint.When(constraint.HasItem(rule.Operations, "*")).
						Require(constraint.MaxItems(rule.Operations, 1)).
						Message("rule.operations wildcard must be the only item"),
					constraint.When(constraint.Present(rule.Scope)).
						Require(constraint.OneOf(rule.Scope, "Cluster", "Namespaced", "*")).
						Message("invalid rule.scope"),
					constraint.ForEach(rule.Operations, func(operation string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.OneOf(operation, "CREATE", "UPDATE", "DELETE", "CONNECT", "*")).
								Message("invalid rule.operations item"),
						}
					}),
					constraint.ForEach(rule.APIVersions, func(item string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.NotEmpty(item)).
								Message("rule.api-versions item must not be empty"),
						}
					}),
					constraint.ForEach(rule.Resources, func(item string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.NotEmpty(item)).Message("rule.resources item must not be empty"),
						}
					}),
				}
			}),
	}
}
