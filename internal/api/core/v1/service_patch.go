package v1

import (
	"reflect"
	"slices"

	corev1 "k8s.io/api/core/v1"
)

type servicePatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func serviceSpecPatch(
	before, after corev1.ServiceSpec, desired ServiceSpec, legacy bool,
) []servicePatchOperation {
	operations := []servicePatchOperation{}
	add := func(field string, value any) {
		operations = append(operations, servicePatchOperation{
			Op: "replace", Path: "/spec/" + field, Value: value,
		})
	}
	allocation := serviceDefault(desired.AllocateLoadBalancerNodePorts, true)
	priorAllocation := true
	if before.Type == corev1.ServiceTypeLoadBalancer {
		priorAllocation = serviceDefault(before.AllocateLoadBalancerNodePorts, true)
	}
	if allocation != priorAllocation ||
		before.Type != after.Type && after.Type == corev1.ServiceTypeLoadBalancer {
		add("allocateLoadBalancerNodePorts", allocation)
	}
	if before.ExternalName != after.ExternalName {
		add("externalName", after.ExternalName)
	}
	if !slices.Equal(serviceSet(before.ExternalIPs), after.ExternalIPs) {
		if legacy {
			add("deprecatedPublicIPs", serviceJSONNull{})
		}
		add("externalIPs", after.ExternalIPs)
	}
	if before.Type != after.Type && after.Type == corev1.ServiceTypeClusterIP {
		for index := range after.Ports {
			after.Ports[index].NodePort = 0
		}
	}
	portsChanged := !reflect.DeepEqual(before.Ports, after.Ports) &&
		(len(before.Ports) != 0 || len(after.Ports) != 0)
	if portsChanged ||
		before.Type != after.Type && after.Type == corev1.ServiceTypeClusterIP {
		ports := after.Ports
		if ports == nil {
			ports = []corev1.ServicePort{}
		}
		add("ports", ports)
	}
	if before.Type != after.Type {
		if after.Type == corev1.ServiceTypeExternalName {
			operations = append(operations, servicePatchOperation{Op: "remove", Path: "/spec/clusterIP"})
		}
		add("type", after.Type)
	}
	if !reflect.DeepEqual(serviceMap(&before.Selector), serviceMap(&after.Selector)) {
		add("selector", after.Selector)
	}
	if before.LoadBalancerIP != after.LoadBalancerIP {
		add("loadBalancerIP", after.LoadBalancerIP)
	}
	if !slices.Equal(serviceSet(before.LoadBalancerSourceRanges), after.LoadBalancerSourceRanges) {
		add("loadBalancerSourceRanges", after.LoadBalancerSourceRanges)
	}
	if before.SessionAffinity != after.SessionAffinity {
		add("sessionAffinity", after.SessionAffinity)
	}
	if !reflect.DeepEqual(before.SessionAffinityConfig, after.SessionAffinityConfig) {
		add("sessionAffinityConfig", after.SessionAffinityConfig)
	}
	if before.ExternalTrafficPolicy != after.ExternalTrafficPolicy {
		add("externalTrafficPolicy", after.ExternalTrafficPolicy)
	}
	if !reflect.DeepEqual(before.InternalTrafficPolicy, after.InternalTrafficPolicy) {
		add("internalTrafficPolicy", after.InternalTrafficPolicy)
	}
	if !slices.Equal(before.IPFamilies, after.IPFamilies) {
		add("ipFamilies", after.IPFamilies)
	}
	if !reflect.DeepEqual(before.IPFamilyPolicy, after.IPFamilyPolicy) {
		add("ipFamilyPolicy", after.IPFamilyPolicy)
	}
	if before.PublishNotReadyAddresses != after.PublishNotReadyAddresses {
		operation := servicePatchOperation{Op: "remove", Path: "/spec/publishNotReadyAddresses"}
		if after.PublishNotReadyAddresses {
			operation.Op, operation.Value = "add", true
		}
		operations = append(operations, operation)
	}
	return operations
}

type serviceJSONNull struct{}

func (serviceJSONNull) MarshalJSON() ([]byte, error) { return []byte("null"), nil }
