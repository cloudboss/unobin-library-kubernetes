package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedadmissionv1 "k8s.io/client-go/kubernetes/typed/admissionregistration/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type MutatingWebhookConfigurationResource struct {
	Metadata          WebhookConfigurationMetadata
	Webhook           []MutatingWebhook
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type MutatingWebhook struct {
	Name                    string
	ClientConfig            WebhookClientConfig
	AdmissionReviewVersions *[]string
	FailurePolicy           *string
	MatchPolicy             *string
	NamespaceSelector       *WebhookSelector
	ObjectSelector          *WebhookSelector
	ReinvocationPolicy      *string
	Rule                    *[]WebhookRule
	SideEffects             *string
	TimeoutSeconds          *int64
}

type MutatingWebhookConfigurationResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   WebhookConfigurationMetadataOutput
	Webhook    []MutatingWebhookOutput
}

type MutatingWebhookOutput struct {
	Name                    string
	ClientConfig            WebhookClientConfig
	AdmissionReviewVersions []string
	FailurePolicy           *string
	MatchPolicy             *string
	NamespaceSelector       *WebhookSelectorOutput
	ObjectSelector          *WebhookSelectorOutput
	ReinvocationPolicy      *string
	Rule                    []WebhookRule
	SideEffects             *string
	TimeoutSeconds          *int64
}

type mutatingWebhookPrior = runtime.Prior[
	MutatingWebhookConfigurationResource, *MutatingWebhookConfigurationResourceOutput,
	*config.Configuration,
]

const mutatingWebhookConfigurationKind = "MutatingWebhookConfiguration"

func (r *MutatingWebhookConfigurationResource) Create(
	ctx context.Context, c *config.Configuration,
) (*MutatingWebhookConfigurationResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	webhooks, err := prepareMutatingWebhooks(r.Webhook)
	if err != nil {
		return nil, err
	}
	api, server, err := mutatingWebhookConfigurationClient(c, nil)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &admissionv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: valueOr(r.Metadata.Name, ""), GenerateName: valueOr(r.Metadata.GenerateName, ""),
			Annotations: valueOr(r.Metadata.Annotations, nil),
			Labels:      valueOr(r.Metadata.Labels, nil),
		},
		Webhooks: webhooks,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", mutatingWebhookConfigurationKind, err)
	}
	return r.refresh(ctx, c, mutatingWebhookIdentity(created, server))
}

func (r *MutatingWebhookConfigurationResource) Read(
	ctx context.Context, c *config.Configuration, p mutatingWebhookPrior,
) (*MutatingWebhookConfigurationResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	return r.refresh(ctx, c, p.Outputs)
}

func (r *MutatingWebhookConfigurationResource) Update(
	ctx context.Context, c *config.Configuration, p mutatingWebhookPrior,
) (*MutatingWebhookConfigurationResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing %s identity in prior state", mutatingWebhookConfigurationKind)
	}
	if !reflect.DeepEqual(p.Inputs.Metadata.Name, r.Metadata.Name) ||
		!reflect.DeepEqual(p.Inputs.Metadata.GenerateName, r.Metadata.GenerateName) ||
		r.Metadata.Name != nil && *r.Metadata.Name != p.Outputs.Name {
		return nil, fmt.Errorf("%s identity changed; replacement is required",
			mutatingWebhookConfigurationKind)
	}
	webhooks, err := prepareMutatingWebhooks(r.Webhook)
	if err != nil {
		return nil, err
	}
	api, _, err := mutatingWebhookConfigurationClient(c, p.Outputs)
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
			return nil, fmt.Errorf("read %s metadata for update: %w",
				mutatingWebhookConfigurationKind, err)
		}
		if err := checkMutatingWebhookUID(p.Outputs, current); err != nil {
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
	if !reflect.DeepEqual(previous.Webhook, projectMutatingWebhooks(webhooks)) {
		patch = append(patch, webhookPatchOperation{Op: "replace", Path: "/webhooks", Value: webhooks})
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("encode %s patch: %w", mutatingWebhookConfigurationKind, err)
	}
	updated, err := api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, body, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch %s: %w", mutatingWebhookConfigurationKind, err)
	}
	if err := checkMutatingWebhookUID(p.Outputs, updated); err != nil {
		return nil, err
	}
	return r.refresh(ctx, c, p.Outputs)
}

func (r *MutatingWebhookConfigurationResource) Delete(
	ctx context.Context, c *config.Configuration, p mutatingWebhookPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing %s identity in prior state", mutatingWebhookConfigurationKind)
	}
	api, _, err := mutatingWebhookConfigurationClient(c, p.Outputs)
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.UID)
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !webhookNotFound(err) {
		return fmt.Errorf("delete %s: %w", mutatingWebhookConfigurationKind, err)
	}
	return nil
}

func (r *MutatingWebhookConfigurationResource) refresh(
	ctx context.Context, c *config.Configuration, saved *MutatingWebhookConfigurationResourceOutput,
) (*MutatingWebhookConfigurationResourceOutput, error) {
	if saved == nil {
		return nil, fmt.Errorf("missing %s identity in prior state", mutatingWebhookConfigurationKind)
	}
	api, _, err := mutatingWebhookConfigurationClient(c, saved)
	if err != nil {
		return nil, err
	}
	var observed *admissionv1.MutatingWebhookConfiguration
	for attempt := range 2 {
		observed, err = api.Get(ctx, saved.Name, metav1.GetOptions{})
		if attempt == 0 && webhookNotFound(err) {
			return nil, fmt.Errorf("read %s: %w", mutatingWebhookConfigurationKind, runtime.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", mutatingWebhookConfigurationKind, err)
		}
		if err := checkMutatingWebhookUID(saved, observed); err != nil {
			return nil, err
		}
	}
	annotations := projectWebhookMetadata(
		observed.Annotations, r.IgnoreAnnotations, valueOr(r.Metadata.Annotations, nil),
	)
	labels := projectWebhookMetadata(
		observed.Labels, r.IgnoreLabels, valueOr(r.Metadata.Labels, nil),
	)
	out := mutatingWebhookIdentity(observed, saved.Server)
	out.Metadata = WebhookConfigurationMetadataOutput{
		Name: observed.Name, GenerateName: observed.GenerateName,
		Annotations: annotations, Labels: labels,
	}
	out.Webhook = projectMutatingWebhooks(observed.Webhooks)
	return out, nil
}

func mutatingWebhookConfigurationClient(
	c *config.Configuration, saved *MutatingWebhookConfigurationResourceOutput,
) (typedadmissionv1.MutatingWebhookConfigurationInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != webhookConfigurationAPIVersion ||
			saved.Kind != mutatingWebhookConfigurationKind || saved.Name == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing %s identity in prior state",
				mutatingWebhookConfigurationKind)
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
		return nil, "", fmt.Errorf("create %s client: %w", mutatingWebhookConfigurationKind, err)
	}
	return api.MutatingWebhookConfigurations(), resolved.Host, nil
}

func mutatingWebhookIdentity(
	observed *admissionv1.MutatingWebhookConfiguration, server string,
) *MutatingWebhookConfigurationResourceOutput {
	return &MutatingWebhookConfigurationResourceOutput{
		Server: server, APIVersion: webhookConfigurationAPIVersion,
		Kind: mutatingWebhookConfigurationKind, Name: observed.Name, UID: string(observed.UID),
	}
}

func checkMutatingWebhookUID(
	saved *MutatingWebhookConfigurationResourceOutput,
	observed *admissionv1.MutatingWebhookConfiguration,
) error {
	return object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, observed)
}

func (r MutatingWebhookConfigurationResource) Constraints() []constraint.Constraint {
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
		constraint.ForEach(r.Webhook, func(webhook MutatingWebhook) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.When(constraint.Present(webhook.FailurePolicy)).
					Require(constraint.OneOf(webhook.FailurePolicy, "Fail", "Ignore")).
					Message("failure-policy must be Fail or Ignore"),
				constraint.When(constraint.Present(webhook.MatchPolicy)).
					Require(constraint.OneOf(webhook.MatchPolicy, "Equivalent", "Exact")).
					Message("match-policy must be Equivalent or Exact"),
				constraint.When(constraint.Present(webhook.ReinvocationPolicy)).
					Require(constraint.OneOf(webhook.ReinvocationPolicy, "Never", "IfNeeded")).
					Message("reinvocation-policy must be Never or IfNeeded"),
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
						constraint.ForEach(rule.Operations,
							func(operation string) []constraint.Constraint {
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
