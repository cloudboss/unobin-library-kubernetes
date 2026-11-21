package v1

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ServiceResource) ResourceDefinition() runtime.ResourceDefinition[
	ServiceResource, *ServiceResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *ServiceResource) *ServiceMetadata { return &r.Metadata })
	spec := runtime.InputField(func(r *ServiceResource) *ServiceSpec { return &r.Spec })
	return runtime.ResourceDefinition[
		ServiceResource, *ServiceResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, r ServiceResource, _ *config.Configuration) error {
			return r.validate()
		},
		StableID: func(_ ServiceResource, out *ServiceResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing Service server or UID")
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[ServiceResource]{
			runtime.EqualBy(metadata, func(a, b ServiceMetadata) bool {
				return serviceIdentity(a) == serviceIdentity(b) &&
					reflect.DeepEqual(serviceMap(a.Labels), serviceMap(b.Labels)) &&
					reflect.DeepEqual(serviceMap(a.Annotations), serviceMap(b.Annotations))
			}),
			runtime.EqualBy(spec, equalServiceSpec),
			runtime.EqualBy(runtime.InputField(func(r *ServiceResource) **bool {
				return &r.WaitForLoadBalancer
			}), func(a, b *bool) bool { return serviceDefault(a, true) == serviceDefault(b, true) }),
			runtime.EqualBy(runtime.InputField(func(r *ServiceResource) **string {
				return &r.CreateTimeout
			}), func(a, b *string) bool {
				left, leftErr := serviceCreateDuration(a)
				right, rightErr := serviceCreateDuration(b)
				return leftErr == nil && rightErr == nil && left == right
			}),
			runtime.EqualBy(runtime.InputField(func(r *ServiceResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *ServiceResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		Replace: runtime.Replacement[
			ServiceResource, *ServiceResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ServiceResource]{
				runtime.ReplaceWhen(metadata, func(a, b ServiceMetadata) bool {
					return serviceIdentity(a) != serviceIdentity(b)
				}),
				runtime.ReplaceWhen(spec, replaceServiceSpec),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func serviceIdentity(m ServiceMetadata) [3]string {
	return [3]string{serviceValue(m.Name), serviceValue(m.GenerateName),
		serviceDefault(m.Namespace, "default")}
}

func serviceValue[T any](v *T) T {
	var zero T
	return serviceDefault(v, zero)
}

func serviceDefault[T any](v *T, fallback T) T {
	if v == nil {
		return fallback
	}
	return *v
}

func serviceMap(v *map[string]string) map[string]string {
	if v == nil || *v == nil {
		return map[string]string{}
	}
	return *v
}

func serviceSet(v []string) []string {
	result := append([]string{}, v...)
	slices.Sort(result)
	return slices.Compact(result)
}

func serviceCreateDuration(v *string) (time.Duration, error) {
	if v == nil {
		return 10 * time.Minute, nil
	}
	duration, err := time.ParseDuration(*v)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("create-timeout must be a positive Go duration")
	}
	return duration, nil
}

func equalServiceSpec(a, b ServiceSpec) bool {
	if (&ServiceResource{Spec: a}).validate() != nil || (&ServiceResource{Spec: b}).validate() != nil {
		return false
	}
	left := (&ServiceResource{Spec: a}).expand(nil).Spec
	right := (&ServiceResource{Spec: b}).expand(nil).Spec
	equal := reflect.DeepEqual(left, right) &&
		serviceDefault(a.AllocateLoadBalancerNodePorts, true) ==
			serviceDefault(b.AllocateLoadBalancerNodePorts, true) &&
		serviceValue(a.LoadBalancerClass) == serviceValue(b.LoadBalancerClass)
	if !equal {
		return false
	}
	for index, port := range serviceValue(a.Port) {
		other := (*b.Port)[index]
		if (port.NodePort == nil) != (other.NodePort == nil) ||
			(port.TargetPort == nil) != (other.TargetPort == nil) {
			return false
		}
	}
	return true
}

func replaceServiceSpec(a, b ServiceSpec) bool {
	return b.ClusterIP != nil && serviceValue(a.ClusterIP) != *b.ClusterIP ||
		b.ClusterIps != nil && !slices.Equal(serviceValue(a.ClusterIps), *b.ClusterIps) ||
		serviceValue(a.LoadBalancerClass) != serviceValue(b.LoadBalancerClass) ||
		b.HealthCheckNodePort != nil && serviceValue(a.HealthCheckNodePort) != *b.HealthCheckNodePort
}

func (r *ServiceResource) validate() error {
	m, s := r.Metadata, r.Spec
	if m.Name != nil && m.GenerateName != nil {
		return fmt.Errorf("metadata.name conflicts with metadata.generate-name")
	}
	if m.Name != nil {
		if problems := validation.IsDNS1123Subdomain(*m.Name); len(problems) != 0 {
			return fmt.Errorf("invalid metadata.name: %s", strings.Join(problems, "; "))
		}
	}
	if m.GenerateName != nil {
		if problems := apivalidation.NameIsDNSLabel(*m.GenerateName, true); len(problems) != 0 {
			return fmt.Errorf("invalid metadata.generate-name: %s", strings.Join(problems, "; "))
		}
	}
	for key, value := range serviceMap(m.Labels) {
		if problems := validation.IsQualifiedName(key); len(problems) != 0 {
			return fmt.Errorf("invalid label key %q: %s", key, strings.Join(problems, "; "))
		}
		if problems := validation.IsValidLabelValue(value); len(problems) != 0 {
			return fmt.Errorf("invalid label value %q: %s", value, strings.Join(problems, "; "))
		}
	}
	for key := range serviceMap(m.Annotations) {
		if problems := validation.IsQualifiedName(strings.ToLower(key)); len(problems) != 0 {
			return fmt.Errorf("invalid annotation key %q: %s", key, strings.Join(problems, "; "))
		}
	}
	for _, expressions := range []*[]string{r.IgnoreAnnotations, r.IgnoreLabels} {
		for _, expression := range serviceValue(expressions) {
			if _, err := regexp.Compile(expression); err != nil {
				return fmt.Errorf("invalid metadata ignore expression: %w", err)
			}
		}
	}
	if _, err := serviceCreateDuration(r.CreateTimeout); err != nil {
		return err
	}
	for _, enum := range []struct {
		name    string
		value   *string
		allowed []string
	}{
		{"type", s.Type, []string{"ClusterIP", "NodePort", "LoadBalancer", "ExternalName"}},
		{"session-affinity", s.SessionAffinity, []string{"None", "ClientIP"}},
		{"external-traffic-policy", s.ExternalTrafficPolicy, []string{"Cluster", "Local"}},
		{"internal-traffic-policy", s.InternalTrafficPolicy, []string{"Cluster", "Local"}},
		{"ip-family-policy", s.IPFamilyPolicy,
			[]string{"SingleStack", "PreferDualStack", "RequireDualStack"}},
	} {
		if enum.value != nil && !slices.Contains(enum.allowed, *enum.value) {
			return fmt.Errorf("invalid spec.%s: %q", enum.name, *enum.value)
		}
	}
	if len(serviceValue(s.ClusterIps)) > 2 || len(serviceValue(s.IPFamilies)) > 2 {
		return fmt.Errorf("cluster-ips and ip-families allow at most two entries")
	}
	for _, family := range serviceValue(s.IPFamilies) {
		if family != "IPv4" && family != "IPv6" {
			return fmt.Errorf("invalid IP family %q", family)
		}
	}
	addresses := append([]string{}, serviceValue(s.ClusterIps)...)
	if s.ClusterIP != nil {
		addresses = append(addresses, *s.ClusterIP)
	}
	for _, address := range addresses {
		if address != "None" && net.ParseIP(address) == nil {
			return fmt.Errorf("invalid cluster IP %q", address)
		}
	}
	addresses = append([]string{}, serviceValue(s.ExternalIps)...)
	if s.LoadBalancerIP != nil {
		addresses = append(addresses, *s.LoadBalancerIP)
	}
	for _, address := range addresses {
		if net.ParseIP(address) == nil {
			return fmt.Errorf("invalid IP %q", address)
		}
	}
	for _, cidr := range serviceValue(s.LoadBalancerSourceRanges) {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid CIDR: %w", err)
		}
	}
	for _, port := range serviceValue(s.Port) {
		if port.Port < 1 || port.Port > 65535 {
			return fmt.Errorf("port must be in 1–65535")
		}
		if port.NodePort != nil && (*port.NodePort < 0 || *port.NodePort > 65535) {
			return fmt.Errorf("node-port must be in 0–65535")
		}
		if !slices.Contains([]string{"TCP", "UDP", "SCTP"}, serviceDefault(port.Protocol, "TCP")) {
			return fmt.Errorf("invalid port protocol")
		}
	}
	if s.HealthCheckNodePort != nil && (*s.HealthCheckNodePort < 1 || *s.HealthCheckNodePort > 65535) {
		return fmt.Errorf("health-check-node-port must be in 1–65535")
	}
	if s.SessionAffinityConfig != nil && s.SessionAffinityConfig.ClientIP != nil {
		if timeout := s.SessionAffinityConfig.ClientIP.TimeoutSeconds; timeout != nil &&
			(*timeout < 1 || *timeout > 86400) {
			return fmt.Errorf("timeout-seconds must be in 1–86400")
		}
	}
	return nil
}
