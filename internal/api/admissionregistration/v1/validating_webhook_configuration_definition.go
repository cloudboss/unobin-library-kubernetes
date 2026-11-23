package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ValidatingWebhookConfigurationResource) ResourceDefinition() runtime.ResourceDefinition[
	ValidatingWebhookConfigurationResource, *ValidatingWebhookConfigurationResourceOutput,
	*config.Configuration,
] {
	return runtime.ResourceDefinition[
		ValidatingWebhookConfigurationResource, *ValidatingWebhookConfigurationResourceOutput,
		*config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input ValidatingWebhookConfigurationResource,
			_ *config.Configuration,
		) error {
			_, err := prepareValidatingWebhooks(input.Webhook)
			return err
		},
		StableID: func(_ ValidatingWebhookConfigurationResource,
			out *ValidatingWebhookConfigurationResourceOutput,
		) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing %s server or UID", webhookConfigurationKind)
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[ValidatingWebhookConfigurationResource]{
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingWebhookConfigurationResource,
			) *[]ValidatingWebhook {
				return &r.Webhook
			}), equalValidatingWebhooks),
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingWebhookConfigurationResource,
			) **map[string]string {
				return &r.Metadata.Annotations
			}), equalWebhookMaps),
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingWebhookConfigurationResource,
			) **map[string]string {
				return &r.Metadata.Labels
			}), equalWebhookMaps),
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingWebhookConfigurationResource,
			) **[]string {
				return &r.IgnoreAnnotations
			}), equalWebhookLists),
			runtime.EqualBy(runtime.InputField(func(
				r *ValidatingWebhookConfigurationResource,
			) **[]string {
				return &r.IgnoreLabels
			}), equalWebhookLists),
		},
		Replace: runtime.Replacement[
			ValidatingWebhookConfigurationResource, *ValidatingWebhookConfigurationResourceOutput,
			*config.Configuration,
		]{
			Fields: []runtime.AnyInputField[ValidatingWebhookConfigurationResource]{
				runtime.InputField(func(r *ValidatingWebhookConfigurationResource) **string {
					return &r.Metadata.Name
				}),
				runtime.InputField(func(r *ValidatingWebhookConfigurationResource) **string {
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

func equalValidatingWebhooks(before, after []ValidatingWebhook) bool {
	return reflect.DeepEqual(
		projectValidatingWebhooks(nativeValidatingWebhooks(before)),
		projectValidatingWebhooks(nativeValidatingWebhooks(after)),
	)
}

func equalWebhookLists(before, after *[]string) bool {
	return slices.Equal(valueOr(before, nil), valueOr(after, nil))
}

func equalWebhookMaps(before, after *map[string]string) bool {
	return maps.Equal(valueOr(before, nil), valueOr(after, nil))
}
