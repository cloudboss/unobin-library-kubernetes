package v1

import (
	"context"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *MutatingWebhookConfigurationResource) ResourceDefinition() runtime.ResourceDefinition[
	MutatingWebhookConfigurationResource, *MutatingWebhookConfigurationResourceOutput,
	*config.Configuration,
] {
	return runtime.ResourceDefinition[
		MutatingWebhookConfigurationResource, *MutatingWebhookConfigurationResourceOutput,
		*config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input MutatingWebhookConfigurationResource,
			_ *config.Configuration,
		) error {
			_, err := prepareMutatingWebhooks(input.Webhook)
			return err
		},
		StableID: func(_ MutatingWebhookConfigurationResource,
			out *MutatingWebhookConfigurationResourceOutput,
		) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing %s server or UID", mutatingWebhookConfigurationKind)
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[MutatingWebhookConfigurationResource]{
			runtime.EqualBy(runtime.InputField(func(
				r *MutatingWebhookConfigurationResource,
			) *[]MutatingWebhook {
				return &r.Webhook
			}), equalMutatingWebhooks),
			runtime.EqualBy(runtime.InputField(func(
				r *MutatingWebhookConfigurationResource,
			) **map[string]string {
				return &r.Metadata.Annotations
			}), equalWebhookMaps),
			runtime.EqualBy(runtime.InputField(func(
				r *MutatingWebhookConfigurationResource,
			) **map[string]string {
				return &r.Metadata.Labels
			}), equalWebhookMaps),
			runtime.EqualBy(runtime.InputField(func(
				r *MutatingWebhookConfigurationResource,
			) **[]string {
				return &r.IgnoreAnnotations
			}), equalWebhookLists),
			runtime.EqualBy(runtime.InputField(func(
				r *MutatingWebhookConfigurationResource,
			) **[]string {
				return &r.IgnoreLabels
			}), equalWebhookLists),
		},
		Replace: runtime.Replacement[
			MutatingWebhookConfigurationResource, *MutatingWebhookConfigurationResourceOutput,
			*config.Configuration,
		]{
			Fields: []runtime.AnyInputField[MutatingWebhookConfigurationResource]{
				runtime.InputField(func(r *MutatingWebhookConfigurationResource) **string {
					return &r.Metadata.Name
				}),
				runtime.InputField(func(r *MutatingWebhookConfigurationResource) **string {
					return &r.Metadata.GenerateName
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func equalMutatingWebhooks(before, after []MutatingWebhook) bool {
	return reflect.DeepEqual(
		projectMutatingWebhooks(nativeMutatingWebhooks(before)),
		projectMutatingWebhooks(nativeMutatingWebhooks(after)),
	)
}
