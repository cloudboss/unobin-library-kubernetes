package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// ServiceDataSource reads one Service by name without changing it. A missing
// Service returns lookup metadata with absent spec and status.
type ServiceDataSource struct {
	// Metadata selects the Service. Namespace defaults to default independently
	// of configuration. Optional annotations and labels are validated metadata,
	// not selectors. An omitted name reaches the native empty-name error.
	Metadata ServiceLookupMetadata
}

type ServiceLookupMetadata struct {
	Name        *string
	Namespace   *string
	Annotations *map[string]string
	Labels      *map[string]string
}

// ServiceDataSourceOutput contains live, unfiltered metadata, spec, and ingress.
// Changes to these values between plan and apply require a new plan.
type ServiceDataSourceOutput struct {
	// Metadata uses the API response on success and lookup values on NotFound.
	Metadata ServiceLookupMetadataOutput
	// Spec is absent on NotFound. Ordered lists retain response order; external
	// addresses and source ranges are sorted unique strings. Allocation defaults
	// to true unless a LoadBalancer explicitly returns false.
	Spec *ServiceLookupSpec
	// Status is absent on NotFound. Empty ingress succeeds without a wait.
	Status *ServiceLookupStatus
}

type ServiceLookupMetadataOutput struct {
	Name            string
	Namespace       string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

type ServiceLookupSpec struct {
	Type                          string
	AllocateLoadBalancerNodePorts bool
	ClusterIP                     string
	ClusterIps                    []string
	ExternalIps                   []string
	ExternalName                  string
	ExternalTrafficPolicy         string
	InternalTrafficPolicy         string
	IPFamilies                    []string
	IPFamilyPolicy                string
	LoadBalancerClass             string
	LoadBalancerIP                string
	LoadBalancerSourceRanges      []string
	HealthCheckNodePort           int32
	PublishNotReadyAddresses      bool
	Selector                      map[string]string
	SessionAffinity               string
	SessionAffinityConfig         *ServiceLookupAffinityConfig
	Port                          []ServicePortOutput
}

type ServiceLookupAffinityConfig struct{ ClientIP *ServiceLookupClientIP }
type ServiceLookupClientIP struct{ TimeoutSeconds int32 }
type ServiceLookupStatus struct{ LoadBalancer ServiceLookupLoadBalancer }
type ServiceLookupLoadBalancer struct{ Ingress []ServiceLookupIngress }
type ServiceLookupIngress struct {
	IP       string
	Hostname string
	IPMode   string
}

var _ runtime.TypedDataSource[
	*ServiceDataSourceOutput, *config.Configuration,
] = (*ServiceDataSource)(nil)

func (r *ServiceDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ServiceDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, err := client.New(c)
	if err != nil {
		return nil, err
	}
	namespace := serviceDefault(r.Metadata.Namespace, "default")
	name := serviceValue(r.Metadata.Name)
	obj, err := api.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &ServiceDataSourceOutput{Metadata: ServiceLookupMetadataOutput{
			Name: name, Namespace: namespace,
			Annotations: maps.Clone(serviceMap(r.Metadata.Annotations)),
			Labels:      maps.Clone(serviceMap(r.Metadata.Labels)),
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Service %s/%s: %w", namespace, name, err)
	}
	s := obj.Spec
	out := &ServiceDataSourceOutput{
		Metadata: ServiceLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion,
			Annotations: serviceMap(&obj.Annotations), Labels: serviceMap(&obj.Labels),
		},
		Spec: &ServiceLookupSpec{
			Type: string(s.Type), AllocateLoadBalancerNodePorts: true,
			ClusterIP: s.ClusterIP, ClusterIps: append([]string{}, s.ClusterIPs...),
			ExternalIps: serviceSet(s.ExternalIPs), ExternalName: s.ExternalName,
			ExternalTrafficPolicy: string(s.ExternalTrafficPolicy),
			InternalTrafficPolicy: string(serviceValue(s.InternalTrafficPolicy)),
			IPFamilies:            []string{}, IPFamilyPolicy: string(serviceValue(s.IPFamilyPolicy)),
			LoadBalancerIP:           s.LoadBalancerIP,
			LoadBalancerSourceRanges: serviceSet(s.LoadBalancerSourceRanges),
			HealthCheckNodePort:      s.HealthCheckNodePort,
			PublishNotReadyAddresses: s.PublishNotReadyAddresses, Selector: serviceMap(&s.Selector),
			SessionAffinity: string(s.SessionAffinity), Port: []ServicePortOutput{},
		},
		Status: &ServiceLookupStatus{
			LoadBalancer: ServiceLookupLoadBalancer{Ingress: []ServiceLookupIngress{}},
		},
	}
	if s.Type == corev1.ServiceTypeLoadBalancer {
		out.Spec.AllocateLoadBalancerNodePorts = serviceDefault(s.AllocateLoadBalancerNodePorts, true)
		out.Spec.LoadBalancerClass = serviceValue(s.LoadBalancerClass)
	}
	for _, family := range s.IPFamilies {
		out.Spec.IPFamilies = append(out.Spec.IPFamilies, string(family))
	}
	if s.SessionAffinityConfig != nil {
		out.Spec.SessionAffinityConfig = &ServiceLookupAffinityConfig{}
		if s.SessionAffinityConfig.ClientIP != nil {
			out.Spec.SessionAffinityConfig.ClientIP = &ServiceLookupClientIP{
				TimeoutSeconds: serviceValue(s.SessionAffinityConfig.ClientIP.TimeoutSeconds),
			}
		}
	}
	for _, port := range s.Ports {
		out.Spec.Port = append(out.Spec.Port, ServicePortOutput{
			Port: port.Port, Name: port.Name, Protocol: string(port.Protocol),
			AppProtocol: serviceValue(port.AppProtocol), NodePort: port.NodePort,
			TargetPort: port.TargetPort.String(),
		})
	}
	for _, ingress := range obj.Status.LoadBalancer.Ingress {
		out.Status.LoadBalancer.Ingress = append(out.Status.LoadBalancer.Ingress, ServiceLookupIngress{
			IP: ingress.IP, Hostname: ingress.Hostname, IPMode: string(serviceValue(ingress.IPMode)),
		})
	}
	return out, nil
}

func (r ServiceDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
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
	}
}
