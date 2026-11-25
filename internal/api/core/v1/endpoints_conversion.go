package v1

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func desiredEndpointsSubsets(values *[]EndpointsSubset) []EndpointsSubsetOutput {
	return withoutEmptyEndpointsSubset(canonicalEndpoints(ptr.Deref(values, nil),
		func(value EndpointsSubset) EndpointsSubsetOutput {
			return EndpointsSubsetOutput{
				Address:         desiredEndpointsAddresses(value.Address),
				NotReadyAddress: desiredEndpointsAddresses(value.NotReadyAddress),
				Port: canonicalEndpoints(ptr.Deref(value.Port, nil),
					func(p EndpointsPort) EndpointsPortOutput {
						return EndpointsPortOutput{
							Name: ptr.Deref(p.Name, ""), Port: int64(int32(p.Port)),
							Protocol: ptr.Deref(p.Protocol, string(corev1.ProtocolTCP)),
						}
					}, compareEndpointsPort),
			}
		}, compareEndpointsSubset))
}

func desiredEndpointsAddresses(values *[]EndpointsAddress) []EndpointsAddressOutput {
	return canonicalEndpoints(ptr.Deref(values, nil),
		func(a EndpointsAddress) EndpointsAddressOutput {
			return EndpointsAddressOutput{
				IP: a.IP, Hostname: ptr.Deref(a.Hostname, ""), NodeName: ptr.Deref(a.NodeName, ""),
			}
		}, compareEndpointsAddress)
}

func readEndpointsSubsets(values []endpointsAPISubset) []EndpointsSubsetOutput {
	project := func(value endpointsAPISubset) EndpointsSubsetOutput {
		return EndpointsSubsetOutput{
			Address:         readEndpointsAddresses(value.Addresses),
			NotReadyAddress: readEndpointsAddresses(value.NotReadyAddresses),
			Port: canonicalEndpoints(value.Ports,
				func(p corev1.EndpointPort) EndpointsPortOutput {
					return EndpointsPortOutput{Name: p.Name, Port: int64(p.Port),
						Protocol: string(p.Protocol)}
				}, compareEndpointsPort),
		}
	}
	return withoutEmptyEndpointsSubset(canonicalEndpoints(values, project, compareEndpointsSubset))
}

func readEndpointsAddresses(values []corev1.EndpointAddress) []EndpointsAddressOutput {
	return canonicalEndpoints(values,
		func(a corev1.EndpointAddress) EndpointsAddressOutput {
			return EndpointsAddressOutput{
				IP: a.IP, Hostname: a.Hostname, NodeName: ptr.Deref(a.NodeName, ""),
			}
		}, compareEndpointsAddress)
}

func canonicalEndpoints[T, U any](values []T, convert func(T) U, compare func(U, U) int) []U {
	out := make([]U, 0, len(values))
	for _, value := range values {
		out = append(out, convert(value))
	}
	slices.SortFunc(out, compare)
	return slices.CompactFunc(out, func(a, b U) bool { return compare(a, b) == 0 })
}

func compareEndpointsAddress(a, b EndpointsAddressOutput) int {
	return cmp.Or(cmp.Compare(a.IP, b.IP), cmp.Compare(a.Hostname, b.Hostname),
		cmp.Compare(a.NodeName, b.NodeName))
}

func compareEndpointsPort(a, b EndpointsPortOutput) int {
	return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Port, b.Port),
		cmp.Compare(a.Protocol, b.Protocol))
}

func compareEndpointsSubset(a, b EndpointsSubsetOutput) int {
	return cmp.Or(
		slices.CompareFunc(a.Address, b.Address, compareEndpointsAddress),
		slices.CompareFunc(a.NotReadyAddress, b.NotReadyAddress, compareEndpointsAddress),
		slices.CompareFunc(a.Port, b.Port, compareEndpointsPort),
	)
}

func equalEndpointsSubset(a, b EndpointsSubsetOutput) bool {
	return compareEndpointsSubset(a, b) == 0
}

func withoutEmptyEndpointsSubset(values []EndpointsSubsetOutput) []EndpointsSubsetOutput {
	// The empty member sorts first and cannot be written to the Kubernetes API.
	if len(values) != 0 &&
		len(values[0].Address) == 0 && len(values[0].NotReadyAddress) == 0 &&
		len(values[0].Port) == 0 {
		return values[1:]
	}
	return values
}

func endpointsSubsetsChanged(
	desired, observed []EndpointsSubsetOutput, prior *[]EndpointsSubset,
) bool {
	if slices.EqualFunc(desired, observed, equalEndpointsSubset) {
		return false
	}
	if !slices.EqualFunc(desired, desiredEndpointsSubsets(prior), equalEndpointsSubset) {
		return true
	}
	defaulted := canonicalEndpoints(desired, func(s EndpointsSubsetOutput) EndpointsSubsetOutput {
		s.Port = canonicalEndpoints(s.Port, func(p EndpointsPortOutput) EndpointsPortOutput {
			if p.Protocol == "" {
				p.Protocol = string(corev1.ProtocolTCP)
			}
			return p
		}, compareEndpointsPort)
		return s
	}, compareEndpointsSubset)
	return !slices.EqualFunc(defaulted, observed, equalEndpointsSubset)
}

func expandEndpointsSubsets(values []EndpointsSubsetOutput) []endpointsAPISubset {
	out := make([]endpointsAPISubset, 0, len(values))
	for _, value := range values {
		ports := make([]corev1.EndpointPort, 0, len(value.Port))
		for _, p := range value.Port {
			ports = append(ports, corev1.EndpointPort{
				Name: p.Name, Port: int32(p.Port), Protocol: corev1.Protocol(p.Protocol),
			})
		}
		out = append(out, endpointsAPISubset{
			Addresses:         expandEndpointsAddresses(value.Address),
			NotReadyAddresses: expandEndpointsAddresses(value.NotReadyAddress),
			Ports:             ports,
		})
	}
	return out
}

func expandEndpointsAddresses(values []EndpointsAddressOutput) []corev1.EndpointAddress {
	out := make([]corev1.EndpointAddress, 0, len(values))
	for _, a := range values {
		address := corev1.EndpointAddress{IP: a.IP, Hostname: a.Hostname}
		if a.NodeName != "" {
			address.NodeName = new(a.NodeName)
		}
		out = append(out, address)
	}
	return out
}

func endpointsMetadataValues(values *map[string]*string) map[string]string {
	out := map[string]string{}
	for key, value := range ptr.Deref(values, nil) {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func (r *EndpointsResource) project(live *endpointsObject, target *EndpointsResourceOutput,
	annotations, labels map[string]string,
) *EndpointsResourceOutput {
	return &EndpointsResourceOutput{
		Server: target.Server, APIVersion: target.APIVersion, Kind: target.Kind,
		Name: live.Name, Namespace: live.Namespace, UID: string(live.UID),
		Metadata: r.projectEndpointsMetadata(live, annotations, labels),
		Subset:   readEndpointsSubsets(live.Subsets),
	}
}

func (r *EndpointsResource) projectEndpointsMetadata(live *endpointsObject,
	annotations, labels map[string]string,
) EndpointsMetadataOutput {
	return EndpointsMetadataOutput{
		Name: live.Name, Namespace: live.Namespace, GenerateName: live.GenerateName,
		Annotations: projectEndpointsMetadataMap(live.Annotations, annotations, r.IgnoreAnnotations),
		Labels:      projectEndpointsMetadataMap(live.Labels, labels, r.IgnoreLabels),
	}
}

func projectEndpointsMetadataMap(
	observed, tracked map[string]string, filters *[]string,
) map[string]string {
	var patterns []*regexp.Regexp
	for _, expression := range ptr.Deref(filters, nil) {
		if compiled, err := regexp.Compile(expression); err == nil {
			patterns = append(patterns, compiled)
		}
	}
	result := map[string]string{}
	for key, value := range observed {
		if _, managed := tracked[key]; managed {
			result[key] = value
			continue
		}
		matches := slices.ContainsFunc(patterns, func(p *regexp.Regexp) bool {
			return p.MatchString(key)
		})
		if !internalEndpointsMetadataKey(key) && !matches {
			result[key] = value
		}
	}
	return result
}

func internalEndpointsMetadataKey(key string) bool {
	parsed, err := url.Parse("//" + key)
	if err != nil {
		return false
	}
	switch parsed.Host {
	case "app.kubernetes.io", "service.beta.kubernetes.io":
		return false
	}
	return strings.HasSuffix(parsed.Host, "kubernetes.io") ||
		strings.Contains(key, "deprecated.daemonset.template.generation")
}
