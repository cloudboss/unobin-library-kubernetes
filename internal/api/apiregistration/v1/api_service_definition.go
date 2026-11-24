package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *APIServiceResource) ResourceDefinition() runtime.ResourceDefinition[
	APIServiceResource, *APIServiceResourceOutput, *config.Configuration,
] {
	spec := runtime.InputField(func(r *APIServiceResource) *APIServiceSpec { return &r.Spec })
	return runtime.ResourceDefinition[
		APIServiceResource, *APIServiceResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(_ APIServiceResource, out *APIServiceResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing APIService server or UID")
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[APIServiceResource]{
			runtime.EqualBy(spec, func(before, after APIServiceSpec) bool {
				return reflect.DeepEqual(desiredSpec(before), desiredSpec(after))
			}),
			runtime.EqualBy(runtime.InputField(func(r *APIServiceResource) **map[string]string {
				return &r.Metadata.Annotations
			}), equalMaps),
			runtime.EqualBy(runtime.InputField(func(r *APIServiceResource) **map[string]string {
				return &r.Metadata.Labels
			}), equalMaps),
			runtime.EqualBy(runtime.InputField(func(r *APIServiceResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalLists),
			runtime.EqualBy(runtime.InputField(func(r *APIServiceResource) **[]string {
				return &r.IgnoreLabels
			}), equalLists),
		},
		Replace: runtime.Replacement[
			APIServiceResource, *APIServiceResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[APIServiceResource]{
				runtime.InputField(func(r *APIServiceResource) *string { return &r.Metadata.Name }),
			},
			Rules: []runtime.ReplacementRule[APIServiceResource]{
				runtime.ReplaceWhen(spec, func(before, after APIServiceSpec) bool {
					return (before.Service == nil) != (after.Service == nil)
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
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

func equalMaps(before, after *map[string]string) bool {
	return maps.Equal(ptr.Deref(before, nil), ptr.Deref(after, nil))
}

// APIServiceRegistration preserves explicit input/output types for schema extraction.
func APIServiceRegistration[In APIServiceResource, Out *APIServiceResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) apiServiceRegistration[P] {
	return apiServiceRegistration[P]{base, read, known}
}

type apiServiceRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r apiServiceRegistration[P]) ReplacementReasons(receiver, cfg any, p P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r apiServiceRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(slices.Clone(reasons), func(reason string) bool {
		return reason == "spec"
	})
	if _, unknown := pending["spec"]; unknown || configPending {
		return reasons, nil
	}

	// Decode the prior values through the typed registration without making a request.
	var prior apiServicePrior
	capture := &APIServiceResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	observed := prior.Observed
	if observed == nil {
		observed = prior.Outputs
	}
	previous := prior.Inputs.Spec.Service
	if observed != nil {
		previous = observed.Spec.Service
	}
	desired := receiver.(*APIServiceResource).Spec.Service
	if (previous == nil) != (desired == nil) {
		reasons = append(reasons, "spec")
	}
	slices.Sort(reasons)
	return reasons, nil
}

func (r apiServiceRegistration[P]) PendingReplacementReasons(
	unresolved map[string][]string, configPending bool,
) []string {
	reasons := r.ResourceRegistration.PendingReplacementReasons(unresolved, configPending)
	// A deferred read can detect a change in backend presence.
	if configPending && !slices.Contains(reasons, "spec") {
		reasons = append(reasons, "spec")
		slices.Sort(reasons)
	}
	return reasons
}
