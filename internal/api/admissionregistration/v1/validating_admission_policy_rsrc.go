package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedv1 "k8s.io/client-go/kubernetes/typed/admissionregistration/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type ValidatingAdmissionPolicyResource struct {
	Metadata      PolicyMetadata
	Spec          PolicySpec
	CreateTimeout *string
	ReadTimeout   *string
	UpdateTimeout *string
	DeleteTimeout *string

	capturePrior *policyPrior `ub:"-"`
}

type PolicyMetadata struct {
	Name         *string
	GenerateName *string
	Labels       *map[string]string
	Annotations  *map[string]string
}

type PolicySpec struct {
	AuditAnnotations []PolicyAuditAnnotation
	FailurePolicy    string
	MatchConstraints PolicyMatchConstraints
	MatchConditions  *[]PolicyMatchCondition
	ParamKind        *PolicyParamKind
	Validations      *[]PolicyValidation
	Variables        *[]PolicyVariable
}

type PolicyAuditAnnotation struct {
	Key             string
	ValueExpression string
}

type PolicyMatchCondition struct {
	Name       string
	Expression string
}

type PolicyParamKind struct {
	APIVersion string
	Kind       string
}

type PolicyValidation struct {
	Expression        string
	Message           string
	MessageExpression *string
}

type PolicyVariable struct {
	Name       *string
	Expression *string
}

type PolicyMatchConstraints struct {
	MatchPolicy          *string
	NamespaceSelector    *PolicySelector
	ObjectSelector       *PolicySelector
	ResourceRules        *[]PolicyResourceRule
	ExcludeResourceRules *[]PolicyResourceRule
}

type PolicySelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]PolicySelectorExpression
}

type PolicySelectorExpression struct {
	Key      *string
	Operator *string
	Values   *[]string
}

type PolicyResourceRule struct {
	APIGroups     []string
	APIVersions   []string
	Operations    []string
	Resources     []string
	ResourceNames *[]string
	Scope         *string
}

type ValidatingAdmissionPolicyResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   PolicyMetadataOutput
	Spec       PolicySpec
}

type PolicyMetadataOutput struct {
	Name         string
	GenerateName string
	Labels       map[string]string
	Annotations  map[string]string
}

type policyPrior = runtime.Prior[
	ValidatingAdmissionPolicyResource, *ValidatingAdmissionPolicyResourceOutput, *config.Configuration,
]

const (
	policyAPIVersion = "admissionregistration.k8s.io/v1"
	policyKind       = "ValidatingAdmissionPolicy"
	policyTimeout    = 20 * time.Minute
)

func (r ValidatingAdmissionPolicyResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.When(constraint.Present(r.Metadata.Name)).
			Require(constraint.NotEmpty(r.Metadata.Name)).
			Message("metadata.name must not be empty"),
		constraint.When(constraint.Present(r.Metadata.GenerateName)).
			Require(constraint.NotEmpty(r.Metadata.GenerateName)).
			Message("metadata.generate-name must not be empty"),
		constraint.Must(constraint.OneOf(r.Spec.FailurePolicy, "Fail", "Ignore")).
			Message("spec.failure-policy must be Fail or Ignore"),
		constraint.When(constraint.Present(r.Spec.MatchConstraints.MatchPolicy)).
			Require(constraint.OneOf(r.Spec.MatchConstraints.MatchPolicy, "Exact", "Equivalent")).
			Message("spec.match-constraints.match-policy must be Exact or Equivalent"),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.ReadTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.UpdateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.DeleteTimeout, constraint.FormatPositiveDuration)),
	}
}

func (r *ValidatingAdmissionPolicyResource) Create(
	ctx context.Context, cfg *config.Configuration,
) (*ValidatingAdmissionPolicyResourceOutput, error) {
	ctx, cancel, err := policyDeadline(ctx, r.CreateTimeout, "create-timeout")
	if err != nil {
		return nil, err
	}
	defer cancel()
	api, server, err := policyClient(cfg, nil)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &admissionv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: policyValue(r.Metadata.Name), GenerateName: policyValue(r.Metadata.GenerateName),
			Labels: policyValue(r.Metadata.Labels), Annotations: policyValue(r.Metadata.Annotations),
		},
		Spec: nativePolicySpec(r.Spec),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create ValidatingAdmissionPolicy: %w", err)
	}
	return r.policyOutput(created, server), nil
}

func (r *ValidatingAdmissionPolicyResource) Read(
	ctx context.Context, cfg *config.Configuration, p policyPrior,
) (*ValidatingAdmissionPolicyResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		return p.Outputs, nil
	}
	ctx, cancel := policyPriorDeadline(ctx, p.Inputs.ReadTimeout)
	defer cancel()
	api, err := savedPolicyClient(cfg, p.Outputs)
	if err != nil {
		return nil, err
	}
	live, err := api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read ValidatingAdmissionPolicy: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read ValidatingAdmissionPolicy: %w", err)
	}
	if err := checkPolicyUID(p.Outputs, live); err != nil {
		return nil, err
	}
	out := *p.Outputs
	if len(live.Labels) != 0 {
		out.Metadata.Labels = live.Labels
	}
	if len(live.Annotations) != 0 {
		out.Metadata.Annotations = live.Annotations
	}
	return &out, nil
}

func (r *ValidatingAdmissionPolicyResource) Update(
	ctx context.Context, cfg *config.Configuration, p policyPrior,
) (*ValidatingAdmissionPolicyResourceOutput, error) {
	ctx, cancel, err := policyDeadline(ctx, r.UpdateTimeout, "update-timeout")
	if err != nil {
		return nil, err
	}
	defer cancel()
	api, err := savedPolicyClient(cfg, p.Outputs)
	if err != nil {
		return nil, err
	}
	live, err := api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read ValidatingAdmissionPolicy for update: %w", err)
	}
	if err := checkPolicyUID(p.Outputs, live); err != nil {
		return nil, err
	}
	live.Spec = nativePolicySpec(r.Spec)
	live.Labels = policyValue(r.Metadata.Labels)
	live.Annotations = policyValue(r.Metadata.Annotations)
	updated, err := api.Update(ctx, live, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update ValidatingAdmissionPolicy: %w", err)
	}
	return r.policyOutput(updated, p.Outputs.Server), nil
}

func (r *ValidatingAdmissionPolicyResource) Delete(
	ctx context.Context, cfg *config.Configuration, p policyPrior,
) error {
	ctx, cancel := policyPriorDeadline(ctx, p.Inputs.DeleteTimeout)
	defer cancel()
	api, err := savedPolicyClient(cfg, p.Outputs)
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.UID)
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete ValidatingAdmissionPolicy: %w", err)
	}
	return nil
}

func policyPriorDeadline(
	ctx context.Context, input *string,
) (context.Context, context.CancelFunc) {
	timeout, err := policyDuration(input)
	if err != nil || timeout <= 0 {
		timeout = policyTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func policyDeadline(
	ctx context.Context, input *string, field string,
) (context.Context, context.CancelFunc, error) {
	timeout, err := policyDuration(input)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", field, err)
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	return bounded, cancel, nil
}

func policyDuration(input *string) (time.Duration, error) {
	if input == nil {
		return policyTimeout, nil
	}
	return time.ParseDuration(*input)
}

func savedPolicyClient(
	cfg *config.Configuration, saved *ValidatingAdmissionPolicyResourceOutput,
) (typedv1.ValidatingAdmissionPolicyInterface, error) {
	if saved == nil || saved.Server == "" || saved.APIVersion != policyAPIVersion ||
		saved.Kind != policyKind || saved.Name == "" || saved.UID == "" {
		return nil, fmt.Errorf("missing ValidatingAdmissionPolicy identity in prior state")
	}
	api, _, err := policyClient(cfg, &saved.Server)
	return api, err
}

func policyClient(
	cfg *config.Configuration, server *string,
) (typedv1.ValidatingAdmissionPolicyInterface, string, error) {
	current := policyValue(cfg)
	if server != nil {
		current.Server = server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	resolved.AcceptContentTypes = "application/json"
	api, err := typedv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create ValidatingAdmissionPolicy client: %w", err)
	}
	return api.ValidatingAdmissionPolicies(), resolved.Host, nil
}

func checkPolicyUID(
	saved *ValidatingAdmissionPolicyResourceOutput, live *admissionv1.ValidatingAdmissionPolicy,
) error {
	return object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, live)
}

func (r *ValidatingAdmissionPolicyResource) policyOutput(
	live *admissionv1.ValidatingAdmissionPolicy, server string,
) *ValidatingAdmissionPolicyResourceOutput {
	return &ValidatingAdmissionPolicyResourceOutput{
		Server: server, APIVersion: policyAPIVersion, Kind: policyKind,
		Name: live.Name, UID: string(live.UID),
		Metadata: PolicyMetadataOutput{
			Name: live.Name, GenerateName: live.GenerateName,
			Labels: policyOutputMap(r.Metadata.Labels), Annotations: policyOutputMap(r.Metadata.Annotations),
		},
		Spec: r.Spec,
	}
}

func policyOutputMap(input *map[string]string) map[string]string {
	if input == nil || *input == nil {
		return map[string]string{}
	}
	return maps.Clone(*input)
}

func policyValue[T any](input *T) T {
	if input != nil {
		return *input
	}
	var empty T
	return empty
}
