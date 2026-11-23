package v1

import (
	networkingv1 "k8s.io/api/networking/v1"
)

func ingressLookupMap(input *map[string]*string) map[string]string {
	out := map[string]string{}
	for key, entry := range value(input) {
		if entry != nil {
			out[key] = *entry
		}
	}
	return out
}

func ingressLookupSpec(s networkingv1.IngressSpec) *IngressLookupSpec {
	out := &IngressLookupSpec{
		IngressClassName: s.IngressClassName,
		Rule:             make([]IngressLookupRule, 0, len(s.Rules)),
		TLS:              make([]IngressLookupTLS, 0, len(s.TLS)),
	}
	if s.DefaultBackend != nil {
		out.DefaultBackend = readBackend(*s.DefaultBackend)
	}
	for _, rule := range s.Rules {
		result := IngressLookupRule{Host: rule.Host}
		if rule.HTTP != nil {
			paths := make([]IngressLookupPath, 0, len(rule.HTTP.Paths))
			for _, path := range rule.HTTP.Paths {
				paths = append(paths, IngressLookupPath{
					Path:     path.Path,
					PathType: (*string)(path.PathType),
					Backend:  *readBackend(path.Backend),
				})
			}
			result.HTTP = &IngressLookupHTTP{Path: paths}
		}
		out.Rule = append(out.Rule, result)
	}
	for _, tls := range s.TLS {
		out.TLS = append(out.TLS, IngressLookupTLS{
			Hosts: append([]string{}, tls.Hosts...), SecretName: tls.SecretName,
		})
	}
	return out
}
