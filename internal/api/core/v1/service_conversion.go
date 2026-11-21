package v1

import (
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (r *ServiceResource) expand(previous *corev1.ServiceSpec) *corev1.Service {
	s := r.Spec
	if previous == nil {
		previous = &corev1.ServiceSpec{}
	}
	result := corev1.ServiceSpec{
		Type:                     corev1.ServiceType(serviceDefault(s.Type, "ClusterIP")),
		SessionAffinity:          corev1.ServiceAffinity(serviceDefault(s.SessionAffinity, "None")),
		PublishNotReadyAddresses: serviceValue(s.PublishNotReadyAddresses),
		ClusterIP:                serviceDefault(s.ClusterIP, previous.ClusterIP),
		ClusterIPs: append([]string(nil),
			serviceDefault(s.ClusterIps, previous.ClusterIPs)...),
		ExternalIPs:              serviceSet(serviceValue(s.ExternalIps)),
		ExternalName:             serviceValue(s.ExternalName),
		LoadBalancerIP:           serviceValue(s.LoadBalancerIP),
		LoadBalancerSourceRanges: serviceSet(serviceValue(s.LoadBalancerSourceRanges)),
		HealthCheckNodePort: int32(serviceDefault(
			s.HealthCheckNodePort, int64(previous.HealthCheckNodePort))),
		Selector:              serviceMap(s.Selector),
		ExternalTrafficPolicy: previous.ExternalTrafficPolicy,
		InternalTrafficPolicy: previous.InternalTrafficPolicy,
		IPFamilyPolicy:        previous.IPFamilyPolicy, IPFamilies: previous.IPFamilies,
		SessionAffinityConfig: previous.SessionAffinityConfig,
	}
	if result.Type == corev1.ServiceTypeLoadBalancer {
		result.AllocateLoadBalancerNodePorts = new(serviceDefault(s.AllocateLoadBalancerNodePorts, true))
		if serviceValue(s.LoadBalancerClass) != "" {
			result.LoadBalancerClass = s.LoadBalancerClass
		}
	}
	if s.ExternalTrafficPolicy != nil {
		result.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicy(*s.ExternalTrafficPolicy)
	}
	if s.InternalTrafficPolicy != nil {
		result.InternalTrafficPolicy = new(corev1.ServiceInternalTrafficPolicy(*s.InternalTrafficPolicy))
	}
	if s.IPFamilyPolicy != nil {
		result.IPFamilyPolicy = new(corev1.IPFamilyPolicy(*s.IPFamilyPolicy))
	}
	if s.IPFamilies != nil {
		result.IPFamilies = []corev1.IPFamily{}
		for _, family := range *s.IPFamilies {
			result.IPFamilies = append(result.IPFamilies, corev1.IPFamily(family))
		}
	}
	if s.SessionAffinityConfig != nil {
		result.SessionAffinityConfig = &corev1.SessionAffinityConfig{}
		if s.SessionAffinityConfig.ClientIP != nil {
			result.SessionAffinityConfig.ClientIP = &corev1.ClientIPConfig{
				TimeoutSeconds: new(int32(serviceValue(s.SessionAffinityConfig.ClientIP.TimeoutSeconds))),
			}
		}
	}
	for index, port := range serviceValue(s.Port) {
		converted := corev1.ServicePort{
			Port: int32(port.Port), Name: serviceValue(port.Name),
			Protocol: corev1.Protocol(serviceDefault(port.Protocol, "TCP")),
			NodePort: int32(serviceValue(port.NodePort)),
		}
		if index < len(previous.Ports) {
			if port.NodePort == nil {
				converted.NodePort = previous.Ports[index].NodePort
			}
			if port.TargetPort == nil {
				converted.TargetPort = previous.Ports[index].TargetPort
			}
		}
		if port.TargetPort != nil {
			converted.TargetPort = intstr.Parse(*port.TargetPort)
		}
		if serviceValue(port.AppProtocol) != "" {
			converted.AppProtocol = port.AppProtocol
		}
		result.Ports = append(result.Ports, converted)
	}
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name: serviceValue(r.Metadata.Name), GenerateName: serviceValue(r.Metadata.GenerateName),
		Namespace:   serviceDefault(r.Metadata.Namespace, "default"),
		Annotations: serviceMap(r.Metadata.Annotations), Labels: serviceMap(r.Metadata.Labels),
	}, Spec: result}
}

func serviceMetadataProject(
	actual, tracked map[string]string, expressions *[]string,
) (map[string]string, error) {
	patterns := []*regexp.Regexp{}
	for _, expression := range serviceValue(expressions) {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pattern)
	}
	result := map[string]string{}
	for key, value := range actual {
		if _, exists := tracked[key]; exists {
			result[key] = value
			continue
		}
		host, _, _ := strings.Cut(key, "/")
		internal := false
		if host != "app.kubernetes.io" && host != "service.beta.kubernetes.io" {
			internal = strings.HasSuffix(host, "kubernetes.io") ||
				strings.Contains(key, "deprecated.daemonset.template.generation")
		}
		for _, pattern := range patterns {
			internal = internal || pattern.MatchString(key)
		}
		if !internal {
			result[key] = value
		}
	}
	return result, nil
}

func (r *ServiceResource) output(
	obj *corev1.Service, server string, tracked ServiceMetadataOutput,
) (*ServiceResourceOutput, error) {
	annotations, err := serviceMetadataProject(
		obj.Annotations, tracked.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := serviceMetadataProject(obj.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	s := obj.Spec
	result := &ServiceResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Service", Name: obj.Name,
		Namespace: obj.Namespace, UID: string(obj.UID),
		Metadata: ServiceMetadataOutput{Name: obj.Name, Namespace: obj.Namespace,
			GenerateName: obj.GenerateName, Annotations: annotations, Labels: labels},
		Spec: ServiceSpecOutput{
			Type: string(s.Type), AllocateLoadBalancerNodePorts: true,
			ClusterIP: s.ClusterIP, ClusterIps: append([]string{}, s.ClusterIPs...),
			ExternalIps: serviceSet(s.ExternalIPs), ExternalName: s.ExternalName,
			ExternalTrafficPolicy: string(s.ExternalTrafficPolicy),
			InternalTrafficPolicy: string(serviceValue(s.InternalTrafficPolicy)),
			IPFamilies:            []string{}, IPFamilyPolicy: string(serviceValue(s.IPFamilyPolicy)),
			LoadBalancerClass: serviceValue(s.LoadBalancerClass), LoadBalancerIP: s.LoadBalancerIP,
			LoadBalancerSourceRanges: serviceSet(s.LoadBalancerSourceRanges),
			PublishNotReadyAddresses: s.PublishNotReadyAddresses, Selector: serviceMap(&s.Selector),
			SessionAffinity: string(s.SessionAffinity), Port: []ServicePortOutput{},
		}, Status: ServiceStatus{LoadBalancer: ServiceLoadBalancer{Ingress: []ServiceIngress{}}},
	}
	if s.Type == corev1.ServiceTypeLoadBalancer {
		result.Spec.AllocateLoadBalancerNodePorts = serviceDefault(s.AllocateLoadBalancerNodePorts, true)
		if s.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal {
			result.Spec.HealthCheckNodePort = s.HealthCheckNodePort
		}
	}
	for _, family := range s.IPFamilies {
		result.Spec.IPFamilies = append(result.Spec.IPFamilies, string(family))
	}
	if s.SessionAffinityConfig != nil {
		result.Spec.SessionAffinityConfig = &ServiceAffinityConfigOutput{}
		if s.SessionAffinityConfig.ClientIP != nil {
			result.Spec.SessionAffinityConfig.ClientIP = &ServiceClientIPOutput{
				TimeoutSeconds: s.SessionAffinityConfig.ClientIP.TimeoutSeconds,
			}
		}
	}
	for _, port := range s.Ports {
		result.Spec.Port = append(result.Spec.Port, ServicePortOutput{
			Port: port.Port, Name: port.Name, Protocol: string(port.Protocol),
			AppProtocol: serviceValue(port.AppProtocol), NodePort: port.NodePort,
			TargetPort: port.TargetPort.String(),
		})
	}
	for _, ingress := range obj.Status.LoadBalancer.Ingress {
		entry := ServiceIngress{IP: ingress.IP, Hostname: ingress.Hostname}
		if ingress.IPMode != nil {
			entry.IPMode = new(string(*ingress.IPMode))
		}
		result.Status.LoadBalancer.Ingress = append(result.Status.LoadBalancer.Ingress, entry)
	}
	return result, nil
}
