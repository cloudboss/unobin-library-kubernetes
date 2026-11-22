package v1

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"

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
				if leftErr != nil || rightErr != nil {
					return serviceValue(a) == serviceValue(b)
				}
				return left == right
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
	if err != nil {
		return 0, fmt.Errorf("parse create-timeout: %w", err)
	}
	return duration, nil
}

func equalServiceSpec(a, b ServiceSpec) bool {
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
