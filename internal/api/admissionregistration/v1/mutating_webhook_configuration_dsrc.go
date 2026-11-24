package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type MutatingWebhookConfigurationDataSource struct {
	Metadata MutatingWebhookLookupMetadata
}

type MutatingWebhookLookupMetadata struct {
	Name        string
	Annotations *map[string]*string
	Labels      *map[string]*string
}

type MutatingWebhookConfigurationDataSourceOutput struct {
	Metadata MutatingWebhookLookupMetadataOutput
	Webhook  []MutatingWebhookOutput
}

type MutatingWebhookLookupMetadataOutput struct {
	Name            string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

var _ runtime.TypedDataSource[
	*MutatingWebhookConfigurationDataSourceOutput, *config.Configuration,
] = (*MutatingWebhookConfigurationDataSource)(nil)

func (r *MutatingWebhookConfigurationDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*MutatingWebhookConfigurationDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, webhookOperationTimeout)
	defer cancel()
	name := r.Metadata.Name
	api, _, err := mutatingWebhookConfigurationClient(c, nil)
	if err != nil {
		return nil, fmt.Errorf("get %s %s: %w", mutatingWebhookConfigurationKind, name, err)
	}
	obj, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &MutatingWebhookConfigurationDataSourceOutput{
			Metadata: MutatingWebhookLookupMetadataOutput{
				Name:        name,
				Annotations: webhookLookupMap(r.Metadata.Annotations),
				Labels:      webhookLookupMap(r.Metadata.Labels),
			},
			Webhook: []MutatingWebhookOutput{},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get %s %s: %w", mutatingWebhookConfigurationKind, name, err)
	}

	annotations, labels := obj.Annotations, obj.Labels
	if annotations == nil {
		annotations = map[string]string{}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	webhooks := projectMutatingWebhooks(obj.Webhooks)
	for i, webhook := range obj.Webhooks {
		preserveEmptyLookupSelector(&webhooks[i].NamespaceSelector, webhook.NamespaceSelector)
		preserveEmptyLookupSelector(&webhooks[i].ObjectSelector, webhook.ObjectSelector)
	}
	return &MutatingWebhookConfigurationDataSourceOutput{
		Metadata: MutatingWebhookLookupMetadataOutput{
			Name: obj.Name, Annotations: annotations, Labels: labels, Generation: obj.Generation,
			ResourceVersion: obj.ResourceVersion, UID: string(obj.UID),
		},
		Webhook: webhooks,
	}, nil
}

func webhookLookupMap(input *map[string]*string) map[string]string {
	out := map[string]string{}
	for key, value := range valueOr(input, nil) {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func preserveEmptyLookupSelector(out **WebhookSelectorOutput, selector *metav1.LabelSelector) {
	if *out == nil && selector != nil &&
		(selector.MatchLabels != nil || selector.MatchExpressions != nil) {
		*out = &WebhookSelectorOutput{
			MatchLabels:      map[string]string{},
			MatchExpressions: []WebhookSelectorExpressionOutput{},
		}
	}
}

func (r MutatingWebhookConfigurationDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		).Message("metadata.name must be a DNS subdomain of at most 253 characters"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					).Message("metadata.labels keys must be valid Kubernetes label keys"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`),
					).Message("metadata.labels values must be valid Kubernetes label values"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?`+
								`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					).Message("metadata.annotations keys must be valid Kubernetes annotation keys"),
				}
			},
		),
	}
}
