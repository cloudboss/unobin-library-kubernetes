package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type servicePrior = runtime.Prior[ServiceResource, *ServiceResourceOutput, *config.Configuration]

const serviceOperationTimeout = 20 * time.Minute

func (r *ServiceResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ServiceResourceOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout, err := serviceCreateDuration(r.CreateTimeout)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	desired := r.expand(nil)
	connection, err := serviceClient(c, nil, desired.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := connection.services.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Service: %w", err)
	}
	target := object.Target{Server: connection.server, APIVersion: "v1", Kind: "Service",
		Name: created.Name, Namespace: created.Namespace, UID: string(created.UID)}
	if target.Name == "" || target.Namespace == "" || target.UID == "" {
		return nil, fmt.Errorf("created Service has no name, namespace, or UID")
	}
	if created.Spec.Type == corev1.ServiceTypeLoadBalancer &&
		serviceDefault(r.WaitForLoadBalancer, true) {
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				live, err := readService(ctx, connection.services, target)
				if err != nil {
					return false, err
				}
				return len(live.Status.LoadBalancer.Ingress) > 0, nil
			})
		if err != nil {
			return nil, serviceWaitFailure(ctx, connection.api, target, err)
		}
	}
	live, err := readService(ctx, connection.services, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, target.Server, ServiceMetadataOutput{
		Annotations: serviceMap(r.Metadata.Annotations), Labels: serviceMap(r.Metadata.Labels)})
}

func (r *ServiceResource) Read(
	ctx context.Context, c *config.Configuration, p servicePrior,
) (*ServiceResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	connection, err := serviceClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	live, err := readService(ctx, connection.services, p.Outputs.target())
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.output(live, p.Outputs.Server, p.Outputs.Metadata)
}

func (r *ServiceResource) Update(
	ctx context.Context, c *config.Configuration, p servicePrior,
) (*ServiceResourceOutput, error) {
	if p.Outputs == nil || serviceIdentity(p.Inputs.Metadata) != serviceIdentity(r.Metadata) ||
		replaceServiceSpec(p.Inputs.Spec, r.Spec) {
		return nil, fmt.Errorf("service identity or immutable field changed; replacement is required")
	}
	ctx, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	connection, err := serviceClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	target := p.Outputs.target()
	live, err := readService(ctx, connection.services, target)
	if err != nil {
		return nil, err
	}
	tracked := p.Outputs.Metadata
	if p.Observed != nil {
		tracked = p.Observed.Metadata
	}
	observed, err := r.output(live, target.Server, tracked)
	if err != nil {
		return nil, err
	}
	operations := []servicePatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	for _, field := range []struct {
		path          string
		before, after map[string]string
	}{
		{"annotations", observed.Metadata.Annotations, serviceMap(r.Metadata.Annotations)},
		{"labels", observed.Metadata.Labels, serviceMap(r.Metadata.Labels)},
	} {
		for _, op := range configMapMapPatch("/metadata/"+field.path, field.before, field.after) {
			operations = append(operations, servicePatchOperation(op))
		}
	}
	desired := r.expand(&live.Spec)
	specPatch := serviceSpecPatch(live.Spec, desired.Spec, r.Spec, false)
	if len(specPatch) > 0 {
		legacy, err := connection.legacyVersion(ctx)
		if err != nil {
			return nil, err
		}
		if legacy {
			specPatch = serviceSpecPatch(live.Spec, desired.Spec, r.Spec, true)
		}
		operations = append(operations, specPatch...)
	}
	if len(operations) == 1 {
		return observed, nil
	}
	body, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode Service patch: %w", err)
	}
	if _, err := connection.services.Patch(ctx, target.Name, types.JSONPatchType,
		body, metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch Service: %w", err)
	}
	live, err = readService(ctx, connection.services, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, target.Server, ServiceMetadataOutput{
		Annotations: serviceMap(r.Metadata.Annotations), Labels: serviceMap(r.Metadata.Labels)})
}

func (r *ServiceResource) Delete(
	ctx context.Context, c *config.Configuration, p servicePrior,
) error {
	if p.Outputs == nil || p.Outputs.APIVersion != "v1" || p.Outputs.Kind != "Service" ||
		p.Outputs.Namespace == "" {
		return fmt.Errorf("missing Service identity in prior state")
	}
	return object.Delete(ctx, c, p.Outputs.target(), metav1.DeleteOptions{}, serviceOperationTimeout)
}

func (o *ServiceResourceOutput) target() object.Target {
	if o == nil {
		return object.Target{}
	}
	return object.Target{Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID}
}

type serviceConnection struct {
	api           typedcorev1.CoreV1Interface
	services      typedcorev1.ServiceInterface
	server        string
	configuration *rest.Config
}

func serviceClient(
	c *config.Configuration, saved *ServiceResourceOutput, namespace string,
) (*serviceConnection, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "Service" ||
			saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, fmt.Errorf("missing Service identity in prior state")
		}
		current.Server = &saved.Server
		namespace = saved.Namespace
	} else if namespace == "" {
		return nil, fmt.Errorf("missing Service identity in prior state")
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create Service client: %w", err)
	}
	return &serviceConnection{api: api, services: api.Services(namespace),
		server: resolved.Host, configuration: resolved}, nil
}

func readService(
	ctx context.Context, api typedcorev1.ServiceInterface, target object.Target,
) (*corev1.Service, error) {
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read Service: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return nil, err
	}
	return live, nil
}

func (c *serviceConnection) legacyVersion(ctx context.Context) (bool, error) {
	api, err := discovery.NewDiscoveryClientForConfig(c.configuration)
	if err != nil {
		return false, fmt.Errorf("create Service discovery client: %w", err)
	}
	var info struct {
		GitVersion string `json:"gitVersion"`
	}
	body, err := api.RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return false, fmt.Errorf("discover Kubernetes version: %w", err)
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, fmt.Errorf("decode Kubernetes version: %w", err)
	}
	v, err := version.ParseSemantic(info.GitVersion)
	if err != nil {
		return false, fmt.Errorf("parse Kubernetes version: %w", err)
	}
	return v.LessThan(version.MustParseSemantic("v1.8.0")), nil
}

func serviceWaitFailure(
	ctx context.Context, api typedcorev1.CoreV1Interface, target object.Target, cause error,
) error {
	events, err := api.Events(target.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Service,involvedObject.name=" + target.Name +
			",involvedObject.namespace=" + target.Namespace,
	})
	if err != nil {
		return fmt.Errorf("list Service warning events: %w", err)
	}
	slices.SortStableFunc(events.Items, func(a, b corev1.Event) int {
		return b.LastTimestamp.Compare(a.LastTimestamp.Time)
	})
	messages := []string{}
	seen := map[string]bool{}
	for _, event := range events.Items {
		if event.Type != corev1.EventTypeWarning || seen[event.Message] {
			continue
		}
		seen[event.Message] = true
		messages = append(messages, event.Reason+": "+event.Message)
		if len(messages) == 3 {
			break
		}
	}
	if len(messages) == 0 {
		return fmt.Errorf("wait for Service load balancer: %w", cause)
	}
	return fmt.Errorf("wait for Service load balancer: %w; %s", cause, strings.Join(messages, "; "))
}

func (r ServiceResource) Constraints() []constraint.Constraint {
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
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.Any(
			constraint.Equals(r.Spec.ClusterIP, "None"),
			constraint.IsFormat(r.Spec.ClusterIP, constraint.FormatIP),
		)),
		constraint.ForEach(r.Spec.ClusterIps, func(address string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.Any(
					constraint.Equals(address, "None"),
					constraint.IsFormat(address, constraint.FormatIP),
				)),
			}
		}),
		constraint.ForEach(r.Spec.ExternalIps, func(address string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(address, constraint.FormatIP)),
			}
		}),
		constraint.Must(constraint.IsFormat(r.Spec.LoadBalancerIP, constraint.FormatIP)),
		constraint.ForEach(r.Spec.LoadBalancerSourceRanges, func(cidr string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(cidr, constraint.FormatCIDR)),
			}
		}),
		constraint.When(constraint.Present(r.Spec.Type)).
			Require(constraint.OneOf(r.Spec.Type, "ClusterIP", "NodePort", "LoadBalancer", "ExternalName")),
		constraint.When(constraint.Present(r.Spec.SessionAffinity)).
			Require(constraint.OneOf(r.Spec.SessionAffinity, "None", "ClientIP")),
		constraint.When(constraint.Present(r.Spec.ExternalTrafficPolicy)).
			Require(constraint.OneOf(r.Spec.ExternalTrafficPolicy, "Cluster", "Local")),
		constraint.When(constraint.Present(r.Spec.InternalTrafficPolicy)).
			Require(constraint.OneOf(r.Spec.InternalTrafficPolicy, "Cluster", "Local")),
		constraint.When(constraint.Present(r.Spec.IPFamilyPolicy)).
			Require(
				constraint.OneOf(r.Spec.IPFamilyPolicy, "SingleStack", "PreferDualStack", "RequireDualStack"),
			),
		constraint.Must(constraint.MaxItems(r.Spec.ClusterIps, 2)),
		constraint.Must(constraint.MaxItems(r.Spec.IPFamilies, 2)),
		constraint.Must(
			constraint.AtLeast(r.Spec.HealthCheckNodePort, 1),
			constraint.AtMost(r.Spec.HealthCheckNodePort, 65535),
		).
			Message("spec.health-check-node-port must be in 1–65535"),
		constraint.Must(
			constraint.AtLeast(r.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds, 1),
			constraint.AtMost(r.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds, 86400),
		).
			Message("spec.session-affinity-config.client-ip.timeout-seconds must be in 1–86400"),
		constraint.ForEach(r.Spec.IPFamilies, func(f string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.OneOf(f, "IPv4", "IPv6")),
			}
		}),
		constraint.ForEach(r.Spec.Port, func(p ServicePort) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.AtLeast(p.Port, 1), constraint.AtMost(p.Port, 65535)).
					Message("spec.port.port must be in 1–65535"),
				constraint.Must(constraint.AtLeast(p.NodePort, 0), constraint.AtMost(p.NodePort, 65535)).
					Message("spec.port.node-port must be in 0–65535"),
				constraint.When(constraint.Present(p.Protocol)).
					Require(constraint.OneOf(p.Protocol, "TCP", "UDP", "SCTP")),
			}
		}),
	}
}
