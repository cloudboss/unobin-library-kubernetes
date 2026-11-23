package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedadmissionv1 "k8s.io/client-go/kubernetes/typed/admissionregistration/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type ValidatingWebhookConfigurationResource struct {
	Metadata          WebhookConfigurationMetadata
	Webhook           []ValidatingWebhook
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type WebhookConfigurationMetadata struct {
	Name         *string
	GenerateName *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type ValidatingWebhook struct {
	Name                    string
	ClientConfig            WebhookClientConfig
	AdmissionReviewVersions *[]string
	FailurePolicy           *string
	MatchPolicy             *string
	NamespaceSelector       *WebhookSelector
	ObjectSelector          *WebhookSelector
	Rule                    *[]WebhookRule
	SideEffects             *string
	TimeoutSeconds          *int64
}

type WebhookClientConfig struct {
	CABundle *string
	Service  *WebhookService
	URL      *string
}

type WebhookService struct {
	Name      string
	Namespace string
	Path      *string
	Port      *int64
}

type WebhookSelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]WebhookSelectorExpression
}

type WebhookSelectorExpression struct {
	Key      *string
	Operator *string
	Values   *[]string
}

type WebhookRule struct {
	APIGroups   []string
	APIVersions []string
	Operations  []string
	Resources   []string
	Scope       *string
}

type ValidatingWebhookConfigurationResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   WebhookConfigurationMetadataOutput
	Webhook    []ValidatingWebhookOutput
}

type WebhookConfigurationMetadataOutput struct {
	Name         string
	GenerateName string
	Annotations  map[string]string
	Labels       map[string]string
}

type ValidatingWebhookOutput struct {
	Name                    string
	ClientConfig            WebhookClientConfig
	AdmissionReviewVersions []string
	FailurePolicy           *string
	MatchPolicy             *string
	NamespaceSelector       *WebhookSelectorOutput
	ObjectSelector          *WebhookSelectorOutput
	Rule                    []WebhookRule
	SideEffects             *string
	TimeoutSeconds          *int64
}

type WebhookSelectorOutput struct {
	MatchLabels      map[string]string
	MatchExpressions []WebhookSelectorExpressionOutput
}

type WebhookSelectorExpressionOutput struct {
	Key      string
	Operator string
	Values   []string
}

type webhookPrior = runtime.Prior[
	ValidatingWebhookConfigurationResource, *ValidatingWebhookConfigurationResourceOutput,
	*config.Configuration,
]

const (
	webhookConfigurationAPIVersion = "admissionregistration.k8s.io/v1"
	webhookConfigurationKind       = "ValidatingWebhookConfiguration"
	webhookOperationTimeout        = 20 * time.Minute
)

func (r *ValidatingWebhookConfigurationResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ValidatingWebhookConfigurationResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	webhooks, err := prepareValidatingWebhooks(r.Webhook)
	if err != nil {
		return nil, err
	}
	api, server, err := webhookConfigurationClient(c, nil)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: valueOr(r.Metadata.Name, ""), GenerateName: valueOr(r.Metadata.GenerateName, ""),
			Annotations: valueOr(r.Metadata.Annotations, nil),
			Labels:      valueOr(r.Metadata.Labels, nil),
		},
		Webhooks: webhooks,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", webhookConfigurationKind, err)
	}
	return r.refresh(ctx, c, webhookIdentity(created, server))
}

func (r *ValidatingWebhookConfigurationResource) Read(
	ctx context.Context, c *config.Configuration, p webhookPrior,
) (*ValidatingWebhookConfigurationResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	return r.refresh(ctx, c, p.Outputs)
}

func (r *ValidatingWebhookConfigurationResource) Update(
	ctx context.Context, c *config.Configuration, p webhookPrior,
) (*ValidatingWebhookConfigurationResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing %s identity in prior state", webhookConfigurationKind)
	}
	if !reflect.DeepEqual(p.Inputs.Metadata.Name, r.Metadata.Name) ||
		!reflect.DeepEqual(p.Inputs.Metadata.GenerateName, r.Metadata.GenerateName) ||
		r.Metadata.Name != nil && *r.Metadata.Name != p.Outputs.Name {
		return nil, fmt.Errorf("%s identity changed; replacement is required", webhookConfigurationKind)
	}
	webhooks, err := prepareValidatingWebhooks(r.Webhook)
	if err != nil {
		return nil, err
	}
	api, _, err := webhookConfigurationClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	annotations, labels := previous.Metadata.Annotations, previous.Metadata.Labels
	desiredAnnotations := valueOr(r.Metadata.Annotations, nil)
	desiredLabels := valueOr(r.Metadata.Labels, nil)
	annotationsFilterChanged := !equalWebhookLists(p.Inputs.IgnoreAnnotations, r.IgnoreAnnotations)
	labelsFilterChanged := !equalWebhookLists(p.Inputs.IgnoreLabels, r.IgnoreLabels)
	if annotationsFilterChanged || labelsFilterChanged {
		// The plan observation uses saved filters and can omit newly visible keys.
		current, err := api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read %s metadata for update: %w", webhookConfigurationKind, err)
		}
		if err := checkWebhookUID(p.Outputs, current); err != nil {
			return nil, err
		}
		if annotationsFilterChanged {
			annotations, desiredAnnotations = reconcileWebhookMetadata(
				annotations, current.Annotations, valueOr(p.Inputs.Metadata.Annotations, nil),
				desiredAnnotations, p.Inputs.IgnoreAnnotations, r.IgnoreAnnotations,
			)
		}
		if labelsFilterChanged {
			labels, desiredLabels = reconcileWebhookMetadata(
				labels, current.Labels, valueOr(p.Inputs.Metadata.Labels, nil),
				desiredLabels, p.Inputs.IgnoreLabels, r.IgnoreLabels,
			)
		}
	}
	patch := []webhookPatchOperation{{Op: "test", Path: "/metadata/uid", Value: p.Outputs.UID}}
	patch = append(patch, webhookMetadataPatch("/metadata/annotations",
		annotations, desiredAnnotations)...)
	patch = append(patch, webhookMetadataPatch("/metadata/labels", labels, desiredLabels)...)
	if !reflect.DeepEqual(previous.Webhook, projectValidatingWebhooks(webhooks)) {
		patch = append(patch, webhookPatchOperation{Op: "replace", Path: "/webhooks", Value: webhooks})
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("encode %s patch: %w", webhookConfigurationKind, err)
	}
	updated, err := api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, body, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch %s: %w", webhookConfigurationKind, err)
	}
	if err := checkWebhookUID(p.Outputs, updated); err != nil {
		return nil, err
	}
	return r.refresh(ctx, c, p.Outputs)
}

func (r *ValidatingWebhookConfigurationResource) Delete(
	ctx context.Context, c *config.Configuration, p webhookPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing %s identity in prior state", webhookConfigurationKind)
	}
	api, _, err := webhookConfigurationClient(c, p.Outputs)
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.UID)
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !webhookNotFound(err) {
		return fmt.Errorf("delete %s: %w", webhookConfigurationKind, err)
	}
	return nil
}

func (r *ValidatingWebhookConfigurationResource) refresh(
	ctx context.Context, c *config.Configuration, saved *ValidatingWebhookConfigurationResourceOutput,
) (*ValidatingWebhookConfigurationResourceOutput, error) {
	if saved == nil {
		return nil, fmt.Errorf("missing %s identity in prior state", webhookConfigurationKind)
	}
	api, _, err := webhookConfigurationClient(c, saved)
	if err != nil {
		return nil, err
	}
	var observed *admissionv1.ValidatingWebhookConfiguration
	for attempt := range 2 {
		observed, err = api.Get(ctx, saved.Name, metav1.GetOptions{})
		if attempt == 0 && webhookNotFound(err) {
			return nil, fmt.Errorf("read %s: %w", webhookConfigurationKind, runtime.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", webhookConfigurationKind, err)
		}
		if err := checkWebhookUID(saved, observed); err != nil {
			return nil, err
		}
	}
	annotations := projectWebhookMetadata(
		observed.Annotations, r.IgnoreAnnotations, valueOr(r.Metadata.Annotations, nil),
	)
	labels := projectWebhookMetadata(
		observed.Labels, r.IgnoreLabels, valueOr(r.Metadata.Labels, nil),
	)
	out := webhookIdentity(observed, saved.Server)
	out.Metadata = WebhookConfigurationMetadataOutput{
		Name: observed.Name, GenerateName: observed.GenerateName,
		Annotations: annotations, Labels: labels,
	}
	out.Webhook = projectValidatingWebhooks(observed.Webhooks)
	return out, nil
}

func webhookConfigurationClient(
	c *config.Configuration, saved *ValidatingWebhookConfigurationResourceOutput,
) (typedadmissionv1.ValidatingWebhookConfigurationInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != webhookConfigurationAPIVersion ||
			saved.Kind != webhookConfigurationKind || saved.Name == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing %s identity in prior state", webhookConfigurationKind)
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedadmissionv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create %s client: %w", webhookConfigurationKind, err)
	}
	return api.ValidatingWebhookConfigurations(), resolved.Host, nil
}

func webhookIdentity(
	observed *admissionv1.ValidatingWebhookConfiguration, server string,
) *ValidatingWebhookConfigurationResourceOutput {
	return &ValidatingWebhookConfigurationResourceOutput{
		Server: server, APIVersion: webhookConfigurationAPIVersion, Kind: webhookConfigurationKind,
		Name: observed.Name, UID: string(observed.UID),
	}
}

func checkWebhookUID(
	saved *ValidatingWebhookConfigurationResourceOutput,
	observed *admissionv1.ValidatingWebhookConfiguration,
) error {
	return object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, observed)
}

func webhookNotFound(err error) bool {
	status, ok := err.(*apierrors.StatusError)
	return ok && apierrors.IsNotFound(status)
}

func (r ValidatingWebhookConfigurationResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.When(constraint.Present(r.Metadata.Name)).
			Require(constraint.NotEmpty(r.Metadata.Name)).Message("metadata.name must not be empty"),
		constraint.When(constraint.Present(r.Metadata.GenerateName)).
			Require(constraint.NotEmpty(r.Metadata.GenerateName)).
			Message("metadata.generate-name must not be empty"),
		constraint.Must(constraint.IsMatch(r.Metadata.Name,
			`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253)).Message("invalid metadata.name"),
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|`+
				`([a-z0-9][-a-z0-9]{0,61})?[\x00-\x7f]-)$`)).
			Message("invalid metadata.generate-name"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
							`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
							`[A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.labels key"),
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?)?$`)).
						Message("invalid metadata.labels value"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?`+
							`(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?)*/)?`+
							`[A-Za-z0-9İK]([-_.A-Za-z0-9İK]{0,61}[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.annotations key"),
				}
			},
		),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("invalid ignore-annotations expression"),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("invalid ignore-labels expression"),
			}
		}),
		constraint.Must(constraint.MinItems(r.Webhook, 1)).
			Message("webhook must contain at least one item"),
		constraint.ForEach(r.Webhook, func(webhook ValidatingWebhook) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.When(constraint.Present(webhook.FailurePolicy)).
					Require(constraint.OneOf(webhook.FailurePolicy, "Fail", "Ignore")).
					Message("failure-policy must be Fail or Ignore"),
				constraint.When(constraint.Present(webhook.MatchPolicy)).
					Require(constraint.OneOf(webhook.MatchPolicy, "Equivalent", "Exact")).
					Message("match-policy must be Equivalent or Exact"),
				constraint.When(constraint.Present(webhook.SideEffects)).
					Require(constraint.OneOf(
						webhook.SideEffects, "Unknown", "None", "Some", "NoneOnDryRun",
					)).Message("invalid side-effects"),
				constraint.Must(
					constraint.AtLeast(webhook.TimeoutSeconds, 1),
					constraint.AtMost(webhook.TimeoutSeconds, 30),
				).Message("timeout-seconds must be between 1 and 30"),
				constraint.ForEach(webhook.Rule, func(rule WebhookRule) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.Must(constraint.MinItems(rule.APIGroups, 1)).
							Message("rule.api-groups must contain at least one item"),
						constraint.Must(constraint.MinItems(rule.APIVersions, 1)).
							Message("rule.api-versions must contain at least one item"),
						constraint.Must(constraint.MinItems(rule.Operations, 1)).
							Message("rule.operations must contain at least one item"),
						constraint.Must(constraint.MinItems(rule.Resources, 1)).
							Message("rule.resources must contain at least one item"),
						constraint.ForEach(rule.Operations, func(operation string) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.Must(constraint.OneOf(
									operation, "*", "CREATE", "UPDATE", "DELETE", "CONNECT",
								)).Message("invalid rule operation"),
							}
						}),
					}
				}),
			}
		}),
	}
}
