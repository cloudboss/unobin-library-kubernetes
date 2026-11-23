package v1

import (
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (r *IngressResource) expand(previous *IngressSpec) (*networkingv1.Ingress, error) {
	m := r.Metadata
	namespace := "default"
	if m.Namespace != nil {
		namespace = *m.Namespace
	}
	spec := expandIngressSpec(r.Spec)
	if r.Spec.IngressClassName == nil && previous != nil {
		spec.IngressClassName = nonempty(value(previous.IngressClassName))
	}
	return &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
		Name: value(m.Name), GenerateName: value(m.GenerateName), Namespace: namespace,
		Labels: maps.Clone(value(m.Labels)), Annotations: maps.Clone(value(m.Annotations)),
	}, Spec: spec}, nil
}

func value[T any](p *T) T {
	if p != nil {
		return *p
	}
	var zero T
	return zero
}

func nonempty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func expandIngressSpec(s IngressSpec) networkingv1.IngressSpec {
	out := networkingv1.IngressSpec{IngressClassName: nonempty(value(s.IngressClassName))}
	if s.DefaultBackend != nil {
		b := expandBackend(s.DefaultBackend)
		out.DefaultBackend = &b
	}
	for _, rule := range value(s.Rule) {
		r := networkingv1.IngressRule{Host: value(rule.Host)}
		if rule.HTTP != nil {
			r.HTTP = &networkingv1.HTTPIngressRuleValue{}
			for _, path := range rule.HTTP.Path {
				kind := networkingv1.PathTypeImplementationSpecific
				if path.PathType != nil {
					kind = networkingv1.PathType(*path.PathType)
				}
				r.HTTP.Paths = append(r.HTTP.Paths, networkingv1.HTTPIngressPath{
					Path: value(path.Path), PathType: &kind, Backend: expandBackend(path.Backend),
				})
			}
		}
		out.Rules = append(out.Rules, r)
	}
	for _, tls := range value(s.TLS) {
		out.TLS = append(out.TLS, networkingv1.IngressTLS{
			Hosts: append([]string(nil), value(tls.Hosts)...), SecretName: value(tls.SecretName),
		})
	}
	return out
}

func expandBackend(b *IngressBackend) networkingv1.IngressBackend {
	out := networkingv1.IngressBackend{}
	if b == nil {
		return out
	}
	if b.Resource != nil {
		out.Resource = &corev1.TypedLocalObjectReference{APIGroup: new(b.Resource.APIGroup),
			Kind: b.Resource.Kind, Name: b.Resource.Name}
	}
	if b.Service != nil {
		out.Service = &networkingv1.IngressServiceBackend{Name: b.Service.Name,
			Port: networkingv1.ServiceBackendPort{Number: int32(value(b.Service.Port.Number)),
				Name: value(b.Service.Port.Name)}}
	}
	return out
}

func readIngressSpec(s networkingv1.IngressSpec) IngressSpec {
	out := IngressSpec{IngressClassName: nonempty(value(s.IngressClassName))}
	if s.DefaultBackend != nil {
		out.DefaultBackend = readBackend(*s.DefaultBackend)
	}
	for _, rule := range s.Rules {
		r := IngressRule{Host: nonempty(rule.Host)}
		if rule.HTTP != nil {
			r.HTTP = &IngressHTTP{Path: []IngressPath{}}
			for _, path := range rule.HTTP.Paths {
				kind := string(value(path.PathType))
				r.HTTP.Path = append(r.HTTP.Path, IngressPath{Path: nonempty(path.Path),
					PathType: nonempty(kind), Backend: readBackend(path.Backend)})
			}
		}
		if out.Rule == nil {
			out.Rule = new([]IngressRule{})
		}
		*out.Rule = append(*out.Rule, r)
	}
	for _, tls := range s.TLS {
		r := IngressTLS{SecretName: nonempty(tls.SecretName)}
		if len(tls.Hosts) > 0 {
			r.Hosts = new(append([]string(nil), tls.Hosts...))
		}
		if out.TLS == nil {
			out.TLS = new([]IngressTLS{})
		}
		*out.TLS = append(*out.TLS, r)
	}
	return out
}

func readBackend(b networkingv1.IngressBackend) *IngressBackend {
	out := &IngressBackend{}
	if b.Resource != nil {
		out.Resource = &IngressResourceReference{
			APIGroup: value(b.Resource.APIGroup), Kind: b.Resource.Kind, Name: b.Resource.Name,
		}
	}
	if b.Service != nil {
		out.Service = &IngressServiceBackend{Name: b.Service.Name}
		if b.Service.Port.Number != 0 {
			out.Service.Port.Number = new(int64(b.Service.Port.Number))
		}
		out.Service.Port.Name = nonempty(b.Service.Port.Name)
	}
	return out
}

func (r *IngressResource) output(
	obj *networkingv1.Ingress, server string, tracked IngressMetadata,
) (*IngressResourceOutput, error) {
	annotations, err := projectMetadata(
		obj.Annotations, value(tracked.Annotations), r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectMetadata(obj.Labels, value(tracked.Labels), r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	out := &IngressResourceOutput{Server: server, APIVersion: "networking.k8s.io/v1", Kind: "Ingress",
		Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID), Spec: readIngressSpec(obj.Spec),
		Metadata: IngressMetadata{Name: new(obj.Name), Namespace: new(obj.Namespace),
			GenerateName: nonempty(obj.GenerateName), Annotations: &annotations, Labels: &labels},
		Status: readIngressStatus(obj.Status),
	}
	return out, nil
}

func readIngressStatus(s networkingv1.IngressStatus) IngressStatus {
	endpoints := make([]IngressEndpoint, 0, len(s.LoadBalancer.Ingress))
	for _, endpoint := range s.LoadBalancer.Ingress {
		endpoints = append(endpoints, IngressEndpoint{IP: endpoint.IP, Hostname: endpoint.Hostname})
	}
	return IngressStatus{LoadBalancer: IngressLoadBalancer{Ingress: endpoints}}
}

func projectMetadata(observed, tracked map[string]string, expressions *[]string) (
	map[string]string, error,
) {
	var patterns []*regexp.Regexp
	for _, expr := range value(expressions) {
		pattern, err := regexp.Compile(expr)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pattern)
	}
	out := map[string]string{}
	for key, v := range observed {
		_, known := tracked[key]
		hidden := internalKey(key)
		for _, pattern := range patterns {
			hidden = hidden || pattern.MatchString(key)
		}
		if known || !hidden {
			out[key] = v
		}
	}
	return out, nil
}

func internalKey(key string) bool {
	parsed, err := url.Parse("//" + key)
	if err != nil {
		return false
	}
	if parsed.Host == "app.kubernetes.io" || parsed.Host == "service.beta.kubernetes.io" {
		return false
	}
	return strings.HasSuffix(parsed.Host, "kubernetes.io") ||
		strings.Contains(key, "deprecated.daemonset.template.generation")
}

func setSavedIdentity(obj *networkingv1.Ingress, o *IngressResourceOutput) {
	obj.Name, obj.Namespace, obj.UID = o.Name, o.Namespace, types.UID(o.UID)
}

func requireIngressIdentity(o *IngressResourceOutput) error {
	if o == nil || o.Server == "" || o.APIVersion != "networking.k8s.io/v1" ||
		o.Kind != "Ingress" || o.Name == "" || o.Namespace == "" || o.UID == "" {
		return fmt.Errorf("missing ingress identity in prior state")
	}
	return nil
}
