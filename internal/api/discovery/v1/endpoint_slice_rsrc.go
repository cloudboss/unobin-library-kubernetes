package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typeddiscoveryv1 "k8s.io/client-go/kubernetes/typed/discovery/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const endpointSliceTimeout = 20 * time.Minute

type endpointSlicePrior = runtime.Prior[
	EndpointSliceResource, *EndpointSliceResourceOutput, *config.Configuration,
]

func (r *EndpointSliceResource) Create(ctx context.Context, c *config.Configuration) (
	*EndpointSliceResourceOutput, error,
) {
	ctx, cancel := context.WithTimeout(ctx, endpointSliceTimeout)
	defer cancel()
	ports, err := prepareEndpointSlicePorts(r.Ports)
	if err != nil {
		return nil, err
	}
	namespace := defaultNamespace(r.Metadata.Namespace)
	api, server, err := endpointSliceClient(c, nil, namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: value(r.Metadata.Name), GenerateName: value(r.Metadata.GenerateName),
			Namespace: namespace, Labels: maps.Clone(value(r.Metadata.Labels)),
			Annotations: maps.Clone(value(r.Metadata.Annotations)),
		},
		AddressType: discoveryv1.AddressType(r.AddressType),
		Endpoints:   expandEndpoints(r.Endpoints),
		Ports:       ports,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create endpoint slice: %w", err)
	}
	saved := &EndpointSliceResourceOutput{
		Server: server, APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice",
		Name: created.Name, Namespace: created.Namespace, UID: string(created.UID),
		AddressType: r.AddressType,
	}
	if err := requireEndpointSliceIdentity(saved); err != nil {
		return nil, err
	}
	return r.refresh(ctx, api, saved, r.Metadata)
}

func (r *EndpointSliceResource) Read(ctx context.Context, c *config.Configuration,
	p endpointSlicePrior,
) (*EndpointSliceResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointSliceTimeout)
	defer cancel()
	if err := requireEndpointSliceIdentity(p.Outputs); err != nil {
		return nil, err
	}
	api, _, err := endpointSliceClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	out, err := r.refresh(ctx, api, p.Outputs, p.Outputs.Metadata)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	return out, err
}

func (r *EndpointSliceResource) Update(ctx context.Context, c *config.Configuration,
	p endpointSlicePrior,
) (*EndpointSliceResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointSliceTimeout)
	defer cancel()
	if p.Inputs.AddressType != r.AddressType ||
		replaceEndpointSliceMetadata(p.Inputs.Metadata, r.Metadata) {
		return nil, fmt.Errorf("endpoint slice identity changed; replacement is required")
	}
	if err := requireEndpointSliceIdentity(p.Outputs); err != nil {
		return nil, err
	}
	api, _, err := endpointSliceClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	baseline := p.Observed
	if baseline == nil {
		baseline = p.Outputs
	}
	if err := requireEndpointSliceIdentity(baseline); err != nil {
		return nil, err
	}
	if err := object.CheckUID(p.Outputs.target(), &metav1.ObjectMeta{
		UID: types.UID(baseline.UID),
	}); err != nil {
		return nil, err
	}
	ports, err := prepareEndpointSlicePorts(r.Ports)
	if err != nil {
		return nil, err
	}
	var live *discoveryv1.EndpointSlice
	if !slices.Equal(value(r.IgnoreLabels), value(p.Inputs.IgnoreLabels)) ||
		!slices.Equal(value(r.IgnoreAnnotations), value(p.Inputs.IgnoreAnnotations)) {
		// The plan observation projected metadata with the prior filters.
		live, err = getEndpointSlice(ctx, api, p.Outputs)
		if err != nil {
			return nil, err
		}
		updated := *baseline
		updated.Metadata = r.sliceMetadata(live, p.Outputs.Metadata)
		baseline = &updated
	}
	patch := r.patch(p.Inputs, baseline, expandEndpoints(r.Endpoints), ports)
	if len(patch) == 1 && live != nil {
		return r.sliceOutput(live, p.Outputs, r.Metadata), nil
	}
	if len(patch) > 1 {
		body, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		if _, err := api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, body,
			metav1.PatchOptions{}); err != nil {
			return nil, fmt.Errorf("update endpoint slice: %w", err)
		}
	}
	return r.refresh(ctx, api, p.Outputs, r.Metadata)
}

func (r *EndpointSliceResource) Delete(ctx context.Context, c *config.Configuration,
	p endpointSlicePrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, endpointSliceTimeout)
	defer cancel()
	if err := requireEndpointSliceIdentity(p.Outputs); err != nil {
		return err
	}
	api, _, err := endpointSliceClient(c, p.Outputs, "")
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
		return fmt.Errorf("delete endpoint slice: %w", err)
	}
	return nil
}

func endpointSliceClient(c *config.Configuration, saved *EndpointSliceResourceOutput,
	namespace string,
) (typeddiscoveryv1.EndpointSliceInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		current.Server, namespace = &saved.Server, saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typeddiscoveryv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create endpoint slice client: %w", err)
	}
	return api.EndpointSlices(namespace), resolved.Host, nil
}

func (r *EndpointSliceResource) refresh(ctx context.Context,
	api typeddiscoveryv1.EndpointSliceInterface, saved *EndpointSliceResourceOutput,
	tracked EndpointSliceMetadata,
) (*EndpointSliceResourceOutput, error) {
	obj, err := getEndpointSlice(ctx, api, saved)
	if err != nil {
		return nil, err
	}
	return r.sliceOutput(obj, saved, tracked), nil
}

func getEndpointSlice(ctx context.Context, api typeddiscoveryv1.EndpointSliceInterface,
	saved *EndpointSliceResourceOutput,
) (*discoveryv1.EndpointSlice, error) {
	obj, err := api.Get(ctx, saved.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read endpoint slice: %w", err)
	}
	if err := object.CheckUID(saved.target(), obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (r *EndpointSliceResource) sliceOutput(obj *discoveryv1.EndpointSlice,
	saved *EndpointSliceResourceOutput, tracked EndpointSliceMetadata,
) *EndpointSliceResourceOutput {
	return &EndpointSliceResourceOutput{
		Server: saved.Server, APIVersion: saved.APIVersion, Kind: saved.Kind,
		Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
		AddressType: saved.AddressType,
		Metadata:    r.sliceMetadata(obj, tracked),
		Endpoints:   readEndpoints(obj.Endpoints),
		Ports:       readEndpointSlicePorts(obj.Ports),
	}
}

func (r *EndpointSliceResource) sliceMetadata(obj *discoveryv1.EndpointSlice,
	tracked EndpointSliceMetadata,
) EndpointSliceMetadata {
	return EndpointSliceMetadata{
		Name: new(obj.Name), GenerateName: nonempty(obj.GenerateName),
		Namespace:   new(obj.Namespace),
		Annotations: projectMetadata(obj.Annotations, value(tracked.Annotations), r.IgnoreAnnotations),
		Labels:      projectMetadata(obj.Labels, value(tracked.Labels), r.IgnoreLabels),
	}
}

func (r EndpointSliceResource) Constraints() []constraint.Constraint {
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
		constraint.Must(constraint.OneOf(r.AddressType, "IPv4", "IPv6", "FQDN")).
			Message("address-type must be IPv4, IPv6 or FQDN"),
		constraint.Must(
			constraint.MinItems(r.Endpoints, 1),
			constraint.MaxItems(r.Endpoints, 1000),
		).Message("endpoints must contain 1 to 1000 items"),
		constraint.Must(
			constraint.MinItems(r.Ports, 1),
			constraint.MaxItems(r.Ports, 100),
		).Message("ports must contain 1 to 100 items"),
		constraint.ForEach(
			r.Endpoints, func(endpoint EndpointSliceEndpoint) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(endpoint.Hostname)).
						Require(
							constraint.IsMatch(endpoint.Hostname,
								`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
							constraint.MaxItems(endpoint.Hostname, 63),
						).Message("hostname must be a DNS label of at most 63 bytes"),
				}
			},
		),
		constraint.ForEach(r.Ports, func(port EndpointSlicePort) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(
					constraint.IsFormat(port.Port, constraint.FormatInt64),
					constraint.IsMatch(port.Port, `^([+]?[0-9]+|-0+)$`),
				).Message("port must be a nonnegative int64 decimal string"),
				constraint.When(constraint.Present(port.Protocol)).
					Require(constraint.OneOf(port.Protocol, "TCP", "UDP", "SCTP")),
			}
		}),
	}
}
