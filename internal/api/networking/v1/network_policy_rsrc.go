package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typednetworkingv1 "k8s.io/client-go/kubernetes/typed/networking/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type networkPolicyPrior = runtime.Prior[
	NetworkPolicyResource, *NetworkPolicyResourceOutput, *config.Configuration,
]

func (r *NetworkPolicyResource) Create(ctx context.Context, c *config.Configuration) (
	*NetworkPolicyResourceOutput, error,
) {
	ctx, cancel := networkPolicyContext(ctx)
	defer cancel()
	api, server, err := networkPolicyClient(c, nil, policyNamespace(r.Metadata))
	if err != nil {
		return nil, err
	}
	obj := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: value(r.Metadata.Name), GenerateName: value(r.Metadata.GenerateName),
		Namespace: policyNamespace(r.Metadata), Labels: maps.Clone(value(r.Metadata.Labels)),
		Annotations: maps.Clone(value(r.Metadata.Annotations)),
	}, Spec: expandNetworkPolicySpec(r.Spec)}
	created, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create network policy: %w", err)
	}
	saved := &NetworkPolicyResourceOutput{Server: server, APIVersion: "networking.k8s.io/v1",
		Kind: "NetworkPolicy", Name: created.Name, Namespace: created.Namespace, UID: string(created.UID)}
	if err := requireNetworkPolicyIdentity(saved); err != nil {
		return nil, err
	}
	return r.refreshPolicy(ctx, api, saved, r.Metadata)
}

func (r *NetworkPolicyResource) Read(ctx context.Context, c *config.Configuration,
	p networkPolicyPrior,
) (*NetworkPolicyResourceOutput, error) {
	ctx, cancel := networkPolicyContext(ctx)
	defer cancel()
	if err := requireNetworkPolicyIdentity(p.Outputs); err != nil {
		return nil, err
	}
	api, _, err := networkPolicyClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	return r.refreshPolicy(ctx, api, p.Outputs, p.Outputs.Metadata)
}

func (r *NetworkPolicyResource) Update(ctx context.Context, c *config.Configuration,
	p networkPolicyPrior,
) (*NetworkPolicyResourceOutput, error) {
	ctx, cancel := networkPolicyContext(ctx)
	defer cancel()
	if err := requireNetworkPolicyIdentity(p.Outputs); err != nil {
		return nil, err
	}
	if replacePolicyMetadata(p.Inputs.Metadata, r.Metadata) {
		return nil, fmt.Errorf("network policy identity changed; replacement is required")
	}
	api, _, err := networkPolicyClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	baseline := p.Observed
	if baseline == nil {
		baseline = p.Outputs
	}
	if err := requireNetworkPolicyIdentity(baseline); err != nil {
		return nil, err
	}
	if err := object.CheckUID(p.Outputs.target(), &metav1.ObjectMeta{
		UID: types.UID(baseline.UID),
	}); err != nil {
		return nil, err
	}
	patch := r.policyPatch(baseline)
	if len(patch) > 1 {
		body, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		if _, err := api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, body,
			metav1.PatchOptions{}); err != nil {
			return nil, fmt.Errorf("update network policy: %w", err)
		}
	}
	return r.refreshPolicy(ctx, api, p.Outputs, r.Metadata)
}

func (r *NetworkPolicyResource) Delete(ctx context.Context, c *config.Configuration,
	p networkPolicyPrior,
) error {
	ctx, cancel := networkPolicyContext(ctx)
	defer cancel()
	if err := requireNetworkPolicyIdentity(p.Outputs); err != nil {
		return err
	}
	api, _, err := networkPolicyClient(c, p.Outputs, "")
	if err != nil {
		return err
	}
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: new(types.UID(p.Outputs.UID))},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete network policy: %w", err)
	}
	return nil
}

func networkPolicyContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 20*time.Minute)
}

func networkPolicyClient(c *config.Configuration, saved *NetworkPolicyResourceOutput,
	namespace string,
) (typednetworkingv1.NetworkPolicyInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if err := requireNetworkPolicyIdentity(saved); err != nil {
			return nil, "", err
		}
		current.Server, namespace = &saved.Server, saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typednetworkingv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create network policy client: %w", err)
	}
	return api.NetworkPolicies(namespace), resolved.Host, nil
}

func (r *NetworkPolicyResource) refreshPolicy(ctx context.Context,
	api typednetworkingv1.NetworkPolicyInterface, saved *NetworkPolicyResourceOutput,
	tracked NetworkPolicyMetadata,
) (*NetworkPolicyResourceOutput, error) {
	obj, err := api.Get(ctx, saved.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read network policy: %w", err)
	}
	if err := object.CheckUID(saved.target(), obj); err != nil {
		return nil, err
	}
	return r.policyOutput(obj, saved.Server, tracked)
}

func (r *NetworkPolicyResource) policyOutput(obj *networkingv1.NetworkPolicy, server string,
	tracked NetworkPolicyMetadata,
) (*NetworkPolicyResourceOutput, error) {
	annotations, err := projectMetadata(
		obj.Annotations, value(tracked.Annotations), r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectMetadata(obj.Labels, value(tracked.Labels), r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	return &NetworkPolicyResourceOutput{Server: server, APIVersion: "networking.k8s.io/v1",
		Kind: "NetworkPolicy", Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
		Metadata: NetworkPolicyMetadata{Name: new(obj.Name), GenerateName: nonempty(obj.GenerateName),
			Namespace: new(obj.Namespace), Annotations: &annotations, Labels: &labels},
		Spec: readNetworkPolicySpec(obj.Spec),
	}, nil
}

func (r NetworkPolicyResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		// Generated-prefix validation masks the final two bytes before checking DNS label syntax.
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
				`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`)),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?`+
								`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
			}
		}),
		constraint.Must(
			constraint.MinItems(r.Spec.PolicyTypes, 1),
			constraint.MaxItems(r.Spec.PolicyTypes, 2),
		),
	}
}
