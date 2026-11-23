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
	"k8s.io/apimachinery/pkg/types"
	typednetworkingv1 "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const ingressClassKind = "IngressClass"

type IngressClassResource struct {
	Metadata          IngressClassMetadata
	Spec              IngressClassSpec
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type IngressClassMetadata struct {
	Name         *string
	GenerateName *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type IngressClassSpec struct {
	Controller *string
	Parameters *IngressClassParameters
}

type IngressClassParameters struct {
	APIGroup  *string
	Kind      string
	Name      string
	Scope     *string
	Namespace *string
}

type IngressClassResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   IngressClassMetadata
	Spec       IngressClassSpec
}

type ingressClassPrior = runtime.Prior[
	IngressClassResource, *IngressClassResourceOutput, *config.Configuration,
]

func (r *IngressClassResource) Create(
	ctx context.Context, c *config.Configuration,
) (*IngressClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, server, err := ingressClassClient(c, nil)
	if err != nil {
		return nil, err
	}
	created, err := mutateIngressClass(ctx, api.RESTClient(), "POST", r.expand(nil))
	if err != nil {
		return nil, fmt.Errorf("create ingress class: %w", err)
	}
	return r.output(created, server, r.Metadata)
}

func (r *IngressClassResource) Read(
	ctx context.Context, c *config.Configuration, p ingressClassPrior,
) (*IngressClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := requireIngressClassIdentity(p.Outputs); err != nil {
		return nil, err
	}
	api, server, err := ingressClassClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	live, err := readIngressClass(ctx, api, p.Outputs)
	if err != nil {
		return nil, err
	}
	return r.output(live, server, p.Outputs.Metadata)
}

func (r *IngressClassResource) Update(
	ctx context.Context, c *config.Configuration, p ingressClassPrior,
) (*IngressClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := requireIngressClassIdentity(p.Outputs); err != nil {
		return nil, err
	}
	if replaceIngressClassMetadata(p.Inputs.Metadata, r.Metadata) {
		return nil, fmt.Errorf("ingress class identity changed; replacement is required")
	}
	api, server, err := ingressClassClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	live, err := readIngressClass(ctx, api, p.Outputs)
	if err != nil {
		return nil, err
	}
	var scope *string
	if live.Spec.Parameters != nil {
		scope = live.Spec.Parameters.Scope
	}
	desired := r.expand(scope)
	desired.Name, desired.UID = p.Outputs.Name, types.UID(p.Outputs.UID)
	desired.Namespace = "default"
	observed, err := r.output(live, server, p.Outputs.Metadata)
	if err != nil {
		return nil, err
	}
	inputsEqual := equalIngressClassMetadata(p.Inputs.Metadata, r.Metadata) &&
		equalIngressClassSpec(p.Inputs.Spec, r.Spec)
	writableEqual := reflect.DeepEqual(desired.Spec, live.Spec) &&
		maps.Equal(desired.Labels, value(observed.Metadata.Labels)) &&
		maps.Equal(desired.Annotations, value(observed.Metadata.Annotations))
	if inputsEqual && writableEqual {
		return observed, nil
	}
	if _, err := mutateIngressClass(ctx, api.RESTClient(), "PUT", desired); err != nil {
		return nil, fmt.Errorf("update ingress class: %w", err)
	}
	live, err = readIngressClass(ctx, api, p.Outputs)
	if err != nil {
		return nil, err
	}
	return r.output(live, server, r.Metadata)
}

func (r *IngressClassResource) Delete(
	ctx context.Context, c *config.Configuration, p ingressClassPrior,
) error {
	if err := requireIngressClassIdentity(p.Outputs); err != nil {
		return err
	}
	return object.Delete(ctx, c, p.Outputs.target(), metav1.DeleteOptions{}, 20*time.Minute)
}

func (o *IngressClassResourceOutput) target() object.Target {
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind, Name: o.Name, UID: o.UID,
	}
}

func requireIngressClassIdentity(o *IngressClassResourceOutput) error {
	if o == nil || o.Server == "" || o.APIVersion != networkingv1.SchemeGroupVersion.String() ||
		o.Kind != ingressClassKind || o.Name == "" || o.UID == "" {
		return fmt.Errorf("missing ingress class identity in prior state")
	}
	return nil
}

func ingressClassClient(c *config.Configuration, saved *IngressClassResourceOutput) (
	*typednetworkingv1.NetworkingV1Client, string, error,
) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typednetworkingv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create ingress class client: %w", err)
	}
	return api, resolved.Host, nil
}

func mutateIngressClass(
	ctx context.Context, api rest.Interface, method string, obj *networkingv1.IngressClass,
) (*networkingv1.IngressClass, error) {
	// The API model's scope field lacks omitempty. Omit a nil scope from the request.
	type parameters struct {
		*networkingv1.IngressClassParametersReference
		Scope *string `json:"scope,omitempty"`
	}
	type spec struct {
		networkingv1.IngressClassSpec
		Parameters *parameters `json:"parameters,omitempty"`
	}
	body := struct {
		*networkingv1.IngressClass
		Spec spec `json:"spec"`
	}{IngressClass: obj, Spec: spec{IngressClassSpec: obj.Spec}}
	if p := obj.Spec.Parameters; p != nil {
		body.Spec.Parameters = &parameters{IngressClassParametersReference: p, Scope: p.Scope}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req := api.Verb(method).Resource("ingressclasses")
	if method == "PUT" {
		req = req.Name(obj.Name)
	}
	out := &networkingv1.IngressClass{}
	err = req.Body(encoded).Do(ctx).Into(out)
	return out, err
}

func readIngressClass(
	ctx context.Context, api *typednetworkingv1.NetworkingV1Client, saved *IngressClassResourceOutput,
) (*networkingv1.IngressClass, error) {
	obj, err := api.IngressClasses().Get(ctx, saved.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read ingress class: %w", err)
	}
	if err := object.CheckUID(saved.target(), obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (r *IngressClassResource) expand(scope *string) *networkingv1.IngressClass {
	spec := expandIngressClassSpec(r.Spec)
	if p := spec.Parameters; p != nil && p.Scope == nil {
		p.Scope = scope
	}
	return &networkingv1.IngressClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: networkingv1.SchemeGroupVersion.String(), Kind: ingressClassKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: value(r.Metadata.Name), GenerateName: value(r.Metadata.GenerateName),
			Labels:      maps.Clone(value(r.Metadata.Labels)),
			Annotations: maps.Clone(value(r.Metadata.Annotations)),
		},
		Spec: spec,
	}
}

func expandIngressClassSpec(s IngressClassSpec) networkingv1.IngressClassSpec {
	out := networkingv1.IngressClassSpec{Controller: value(s.Controller)}
	if p := s.Parameters; p != nil {
		out.Parameters = &networkingv1.IngressClassParametersReference{
			APIGroup: nonempty(value(p.APIGroup)), Kind: p.Kind, Name: p.Name,
			Scope: p.Scope, Namespace: nonempty(value(p.Namespace)),
		}
	}
	return out
}

func (r *IngressClassResource) output(
	obj *networkingv1.IngressClass, server string, tracked IngressClassMetadata,
) (*IngressClassResourceOutput, error) {
	annotations, err := projectMetadata(
		obj.Annotations, value(tracked.Annotations), r.IgnoreAnnotations,
	)
	if err != nil {
		return nil, err
	}
	labels, err := projectMetadata(obj.Labels, value(tracked.Labels), r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	spec := IngressClassSpec{Controller: nonempty(obj.Spec.Controller)}
	if p := obj.Spec.Parameters; p != nil {
		spec.Parameters = &IngressClassParameters{
			APIGroup: p.APIGroup, Kind: p.Kind, Name: p.Name, Scope: p.Scope, Namespace: p.Namespace,
		}
	}
	out := &IngressClassResourceOutput{
		Server: server, APIVersion: networkingv1.SchemeGroupVersion.String(), Kind: ingressClassKind,
		Name: obj.Name, UID: string(obj.UID),
		Metadata: IngressClassMetadata{
			Name: new(obj.Name), GenerateName: nonempty(obj.GenerateName),
			Labels: &labels, Annotations: &annotations,
		},
		Spec: spec,
	}
	if err := requireIngressClassIdentity(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r IngressClassResource) Constraints() []constraint.Constraint {
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
		constraint.When(constraint.Present(r.Spec.Parameters.Scope)).
			Require(constraint.OneOf(r.Spec.Parameters.Scope, "Cluster", "Namespace")).
			Message("spec.parameters.scope must be Cluster or Namespace"),
	}
}
