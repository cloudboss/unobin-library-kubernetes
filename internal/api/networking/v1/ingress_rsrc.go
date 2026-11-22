package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	typednetworkingv1 "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type ingressPrior = runtime.Prior[IngressResource, *IngressResourceOutput, *config.Configuration]

func (r *IngressResource) Create(ctx context.Context, c *config.Configuration) (
	*IngressResourceOutput, error,
) {
	obj, err := r.expand(nil)
	if err != nil {
		return nil, err
	}
	d, err := duration(r.CreateTimeout, "create-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	api, server, err := ingressClient(c, nil, obj.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.mutate(ctx, "POST", obj)
	if err != nil {
		return nil, fmt.Errorf("create ingress: %w", err)
	}
	saved := &IngressResourceOutput{Server: server, APIVersion: "networking.k8s.io/v1",
		Kind: "Ingress", Name: created.Name, Namespace: created.Namespace, UID: string(created.UID)}
	if err := requireIngressIdentity(saved); err != nil {
		return nil, err
	}
	if value(r.WaitForLoadBalancer) {
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				obj, err := api.read(ctx, saved)
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				if err != nil {
					return false, err
				}
				return len(obj.Status.LoadBalancer.Ingress) > 0, nil
			})
		if err != nil {
			return nil, fmt.Errorf("wait for ingress load balancer: %w", err)
		}
	}
	return r.refresh(ctx, api, saved, r.Metadata)
}

func (r *IngressResource) Read(ctx context.Context, c *config.Configuration, p ingressPrior) (
	*IngressResourceOutput, error,
) {
	if err := requireIngressIdentity(p.Outputs); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, _, err := ingressClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	return r.refresh(ctx, api, p.Outputs, p.Outputs.Metadata)
}

func (r *IngressResource) Update(ctx context.Context, c *config.Configuration, p ingressPrior) (
	*IngressResourceOutput, error,
) {
	if err := requireIngressIdentity(p.Outputs); err != nil {
		return nil, err
	}
	if replaceMetadata(p.Inputs.Metadata, r.Metadata) {
		return nil, fmt.Errorf("ingress identity changed; replacement is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, _, err := ingressClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	live, err := api.read(ctx, p.Outputs)
	if err != nil {
		return nil, err
	}
	observed, err := r.output(live, p.Outputs.Server, p.Outputs.Metadata)
	if err != nil {
		return nil, err
	}
	desired, err := r.expand(&observed.Spec)
	if err != nil {
		return nil, err
	}
	if desired.Namespace == "" {
		desired.Namespace = "default"
	}
	setSavedIdentity(desired, p.Outputs)
	inputsEqual := equalMetadata(p.Inputs.Metadata, r.Metadata) &&
		equalSpec(p.Inputs.Spec, r.Spec) &&
		value(p.Inputs.WaitForLoadBalancer) == value(r.WaitForLoadBalancer)
	writableEqual := reflect.DeepEqual(desired.Spec, expandIngressSpec(observed.Spec)) &&
		maps.Equal(desired.Labels, value(observed.Metadata.Labels)) &&
		maps.Equal(desired.Annotations, value(observed.Metadata.Annotations))
	if inputsEqual && writableEqual {
		return observed, nil
	}
	if _, err := api.mutate(ctx, "PUT", desired); err != nil {
		return nil, fmt.Errorf("update ingress: %w", err)
	}
	return r.refresh(ctx, api, p.Outputs, r.Metadata)
}

func (r *IngressResource) Delete(
	ctx context.Context, c *config.Configuration, p ingressPrior,
) error {
	if err := requireIngressIdentity(p.Outputs); err != nil {
		return err
	}
	d, err := duration(p.Inputs.DeleteTimeout, "delete-timeout")
	if err != nil {
		return err
	}
	return object.Delete(ctx, c, p.Outputs.target(), metav1.DeleteOptions{}, d)
}

func (o *IngressResourceOutput) target() object.Target {
	return object.Target{Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID}
}

type ingressAPI struct {
	typednetworkingv1.IngressInterface
	rest      rest.Interface
	namespace string
}

func ingressClient(c *config.Configuration, saved *IngressResourceOutput, namespace string) (
	*ingressAPI, string, error,
) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if err := requireIngressIdentity(saved); err != nil {
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
		return nil, "", fmt.Errorf("create ingress client: %w", err)
	}
	return &ingressAPI{IngressInterface: api.Ingresses(namespace), rest: api.RESTClient(),
		namespace: namespace}, resolved.Host, nil
}

func (a *ingressAPI) mutate(ctx context.Context, method string, obj *networkingv1.Ingress) (
	*networkingv1.Ingress, error,
) {
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		return nil, err
	}
	delete(body, "status")
	body["apiVersion"], body["kind"] = "networking.k8s.io/v1", "Ingress"
	encoded, err = json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req := a.rest.Verb(method).Resource("ingresses")
	if a.namespace != "" {
		req = req.Namespace(a.namespace)
	}
	if method == "PUT" {
		req = req.Name(obj.Name)
	}
	out := &networkingv1.Ingress{}
	err = req.Body(encoded).Do(ctx).Into(out)
	return out, err
}

func (a *ingressAPI) read(ctx context.Context, saved *IngressResourceOutput) (
	*networkingv1.Ingress, error,
) {
	obj, err := a.Get(ctx, saved.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read ingress: %w", err)
	}
	if err := object.CheckUID(saved.target(), obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (r *IngressResource) refresh(ctx context.Context, api *ingressAPI,
	saved *IngressResourceOutput, tracked IngressMetadata,
) (*IngressResourceOutput, error) {
	obj, err := api.read(ctx, saved)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.output(obj, saved.Server, tracked)
}

func (r IngressResource) Constraints() []constraint.Constraint {
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
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.DeleteTimeout, constraint.FormatPositiveDuration)),
		constraint.ForEach(r.Spec.Rule, func(rule IngressRule) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.When(constraint.Present(rule.HTTP)).
					Require(constraint.NotEmpty(rule.HTTP.Path)),
				constraint.ForEach(rule.HTTP.Path, func(path IngressPath) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.When(constraint.Present(path.PathType)).
							Require(constraint.OneOf(path.PathType,
								"ImplementationSpecific", "Prefix", "Exact")),
					}
				}),
			}
		}),
	}
}
