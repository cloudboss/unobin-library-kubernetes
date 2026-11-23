package v1

import (
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
)

func expandEndpoints(input []EndpointSliceEndpoint) []discoveryv1.Endpoint {
	endpoints := make([]discoveryv1.Endpoint, 0, len(input))
	for _, endpoint := range input {
		out := discoveryv1.Endpoint{
			Addresses: append([]string(nil), endpoint.Addresses...),
			Hostname:  nonempty(value(endpoint.Hostname)),
			NodeName:  nonempty(value(endpoint.NodeName)),
			Zone:      nonempty(value(endpoint.Zone)),
		}
		if c := endpoint.Conditions; c != nil {
			out.Conditions = discoveryv1.EndpointConditions{
				Ready: new(value(c.Ready)), Serving: new(value(c.Serving)),
				Terminating: new(value(c.Terminating)),
			}
		}
		if ref := endpoint.TargetRef; ref != nil {
			out.TargetRef = &corev1.ObjectReference{
				Name: ref.Name, Namespace: defaultNamespace(ref.Namespace),
				ResourceVersion: value(ref.ResourceVersion), UID: types.UID(value(ref.UID)),
				FieldPath: value(ref.FieldPath),
			}
		}
		endpoints = append(endpoints, out)
	}
	return endpoints
}

func prepareEndpointSlicePorts(input []EndpointSlicePort) ([]discoveryv1.EndpointPort, error) {
	ports := make([]discoveryv1.EndpointPort, 0, len(input))
	for i, port := range input {
		number, err := strconv.ParseInt(port.Port, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("convert endpoint slice ports[%d].port: %w", i, err)
		}
		// Kubernetes defaults protocol to TCP. Managed protocol inputs are omitted in requests.
		ports = append(ports, discoveryv1.EndpointPort{
			Name: new(value(port.Name)), Port: new(int32(min(number, math.MaxInt32))),
			AppProtocol: new(port.AppProtocol),
		})
	}
	return ports, nil
}

func readEndpoints(input []discoveryv1.Endpoint) []EndpointSliceEndpointOutput {
	endpoints := make([]EndpointSliceEndpointOutput, 0, len(input))
	for _, endpoint := range input {
		out := EndpointSliceEndpointOutput{
			Addresses: append([]string{}, endpoint.Addresses...),
			Hostname:  endpoint.Hostname,
			NodeName:  endpoint.NodeName,
			Zone:      endpoint.Zone,
		}
		c := endpoint.Conditions
		if c.Ready != nil || c.Serving != nil || c.Terminating != nil {
			out.Conditions = &EndpointSliceConditions{
				Ready: c.Ready, Serving: c.Serving, Terminating: c.Terminating,
			}
		}
		if ref := endpoint.TargetRef; ref != nil {
			out.TargetRef = &EndpointSliceReferenceOutput{
				Name: nonempty(ref.Name), UID: nonempty(string(ref.UID)),
				ResourceVersion: nonempty(ref.ResourceVersion), FieldPath: nonempty(ref.FieldPath),
			}
			if ref.Name != "" {
				out.TargetRef.Namespace = new(ref.Namespace)
			}
		}
		endpoints = append(endpoints, out)
	}
	return endpoints
}

func readEndpointSlicePorts(input []discoveryv1.EndpointPort) []EndpointSlicePortOutput {
	ports := make([]EndpointSlicePortOutput, 0, len(input))
	for _, port := range input {
		out := EndpointSlicePortOutput{
			Name: nonempty(value(port.Name)), AppProtocol: port.AppProtocol,
		}
		if port.Port != nil {
			out.Port = new(strconv.FormatInt(int64(*port.Port), 10))
		}
		if port.Protocol != nil {
			out.Protocol = new(string(*port.Protocol))
		}
		ports = append(ports, out)
	}
	return ports
}

func projectMetadata(observed, tracked map[string]string, filters *[]string) *map[string]string {
	patterns := make([]*regexp.Regexp, 0, len(value(filters)))
	for _, expr := range value(filters) {
		// A malformed regex in saved inputs matches no metadata key.
		if pattern, err := regexp.Compile(expr); err == nil {
			patterns = append(patterns, pattern)
		}
	}
	out := maps.Clone(observed)
	if out == nil {
		out = map[string]string{}
	}
	maps.DeleteFunc(out, func(key, _ string) bool {
		if _, known := tracked[key]; known {
			return false
		}
		return internalMetadataKey(key) || slices.ContainsFunc(patterns, func(p *regexp.Regexp) bool {
			return p.MatchString(key)
		})
	})
	return &out
}

func internalMetadataKey(key string) bool {
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

func value[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func nonempty(s string) *string {
	if s == "" {
		return nil
	}
	return new(s)
}

func defaultNamespace(namespace *string) string {
	if namespace == nil {
		return "default"
	}
	return *namespace
}
