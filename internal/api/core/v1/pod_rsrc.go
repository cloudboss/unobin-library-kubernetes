package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// PodResource manages a namespaced Pod and waits for a requested phase on creation.
type PodResource struct {
	Metadata          ServiceMetadata
	Spec              PodSpec
	TargetState       *[]string
	CreateTimeout     *string
	DeleteTimeout     *string
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string

	capturePrior *podPrior `ub:"-"`
}

type PodResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	UID        string
	Metadata   ServiceMetadataOutput
	Spec       PodSpecOutput
}

type podPrior = runtime.Prior[PodResource, *PodResourceOutput, *config.Configuration]

const (
	podOperationTimeout = 20 * time.Minute
	podDefaultWait      = 5 * time.Minute
	podPollInterval     = 250 * time.Millisecond
)

func (r *PodResource) Create(
	ctx context.Context, c *config.Configuration,
) (*PodResourceOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout, err := podDuration(r.CreateTimeout, "create-timeout")
	if err != nil {
		return nil, err
	}
	desired, err := r.prepare()
	if err != nil {
		return nil, err
	}
	api, server, err := podClient(c, "")
	if err != nil {
		return nil, err
	}
	operation, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	created, err := api.Pods(desired.Namespace).Create(operation, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Pod: %w", err)
	}
	target := object.Target{Server: server, APIVersion: "v1", Kind: "Pod",
		Name: created.Name, Namespace: created.Namespace, UID: string(created.UID)}
	if target.Name == "" || target.Namespace == "" || target.UID == "" {
		return nil, fmt.Errorf("created Pod has no name, namespace, or UID")
	}
	pods := api.Pods(target.Namespace)
	phases := serviceDefault(r.TargetState, []string{string(corev1.PodRunning)})
	err = wait.PollUntilContextCancel(operation, podPollInterval, true,
		func(ctx context.Context) (bool, error) {
			pod, err := readPod(ctx, pods, target)
			if err != nil {
				return false, err
			}
			if slices.Contains(phases, string(pod.Status.Phase)) {
				return true, nil
			}
			if pod.Status.Phase != corev1.PodPending {
				return false, fmt.Errorf("unexpected Pod phase %q", pod.Status.Phase)
			}
			return false, nil
		})
	if err != nil {
		return nil, podWaitFailure(operation, api, target, err)
	}
	live, err := readPod(operation, pods, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, server, podTrackedMetadata(r.Metadata, ServiceMetadataOutput{}))
}

func (r *PodResource) Read(
	ctx context.Context, c *config.Configuration, prior podPrior,
) (*PodResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = prior
		return prior.Outputs, nil
	}
	target, err := prior.Outputs.target()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, podOperationTimeout)
	defer cancel()
	api, _, err := podClient(c, target.Server)
	if err != nil {
		return nil, err
	}
	live, err := readPod(ctx, api.Pods(target.Namespace), target)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	saved := prior.Inputs
	saved.IgnoreAnnotations = podSavedIgnorePatterns(saved.IgnoreAnnotations)
	saved.IgnoreLabels = podSavedIgnorePatterns(saved.IgnoreLabels)
	tracked := podTrackedMetadata(saved.Metadata, ServiceMetadataOutput{})
	return saved.output(live, target.Server, tracked)
}

func (r *PodResource) Update(
	ctx context.Context, c *config.Configuration, prior podPrior,
) (*PodResourceOutput, error) {
	target, err := prior.Outputs.target()
	if err != nil {
		return nil, err
	}
	desired, err := r.prepare()
	if err != nil {
		return nil, err
	}
	metadata, spec := podImmutableChanges(r, prior, true)
	if metadata || spec {
		return nil, fmt.Errorf("immutable Pod field or identity changed, replacement is required")
	}
	ctx, cancel := context.WithTimeout(ctx, podOperationTimeout)
	defer cancel()
	api, _, err := podClient(c, target.Server)
	if err != nil {
		return nil, err
	}
	pods := api.Pods(target.Namespace)
	live, err := readPod(ctx, pods, target)
	if err != nil {
		return nil, err
	}
	tracked := podTrackedMetadata(prior.Inputs.Metadata, ServiceMetadataOutput{})
	tracked = podTrackedMetadata(r.Metadata, tracked)
	observed, err := r.output(live, target.Server, tracked)
	if err != nil {
		return nil, err
	}
	prior.Observed = observed
	_, spec = podImmutableChanges(r, prior, false)
	if spec {
		return nil, fmt.Errorf("immutable Pod fields changed, replacement is required")
	}
	operations := []configMapPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	for _, field := range []struct {
		name                            string
		live, projected, prior, desired map[string]string
		priorIgnore, desiredIgnore      *[]string
	}{
		{
			name: "annotations", live: live.Annotations, projected: observed.Metadata.Annotations,
			prior: serviceMap(prior.Inputs.Metadata.Annotations), desired: desired.Annotations,
			priorIgnore: prior.Inputs.IgnoreAnnotations, desiredIgnore: r.IgnoreAnnotations,
		},
		{
			name: "labels", live: live.Labels, projected: observed.Metadata.Labels,
			prior: serviceMap(prior.Inputs.Metadata.Labels), desired: desired.Labels,
			priorIgnore: prior.Inputs.IgnoreLabels, desiredIgnore: r.IgnoreLabels,
		},
	} {
		before := field.projected
		if maps.Equal(field.prior, field.desired) &&
			equalConfigMapLists(field.priorIgnore, field.desiredIgnore) {
			// An unchanged map can still need managed drift repair.
			before = make(map[string]string, len(field.desired))
			for key := range field.desired {
				if value, ok := field.projected[key]; ok {
					before[key] = value
				}
			}
		}
		operations = append(operations, podMetadataPatch(
			"/metadata/"+field.name, field.live, before, field.desired,
		)...)
	}
	if runtime.Changed(live.Spec.ActiveDeadlineSeconds, desired.Spec.ActiveDeadlineSeconds) {
		operations = append(operations, configMapPatchOperation{
			Op: "replace", Path: "/spec/activeDeadlineSeconds",
			Value: serviceValue(desired.Spec.ActiveDeadlineSeconds),
		})
	}
	if len(operations) == 1 {
		return observed, nil
	}
	body, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode Pod patch: %w", err)
	}
	if _, err := pods.Patch(ctx, target.Name, types.JSONPatchType, body,
		metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch Pod: %w", err)
	}
	live, err = readPod(ctx, pods, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, target.Server, podTrackedMetadata(r.Metadata, ServiceMetadataOutput{}))
}

func (r *PodResource) Delete(ctx context.Context, c *config.Configuration, prior podPrior) error {
	target, err := prior.Outputs.target()
	if err != nil {
		return err
	}
	timeout, err := podDuration(prior.Inputs.DeleteTimeout, "delete-timeout")
	if err != nil || timeout <= 0 {
		timeout = podDefaultWait
	}
	return object.Delete(ctx, c, target, metav1.DeleteOptions{}, timeout)
}

func (o *PodResourceOutput) target() (object.Target, error) {
	if o == nil || o.Server == "" || o.APIVersion != "v1" || o.Kind != "Pod" ||
		o.Name == "" || o.Namespace == "" || o.UID == "" {
		return object.Target{}, fmt.Errorf("missing Pod identity in prior state")
	}
	return object.Target{Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID}, nil
}

func podClient(
	c *config.Configuration, server string,
) (typedcorev1.CoreV1Interface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if server != "" {
		current.Server = &server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Pod client: %w", err)
	}
	return api, resolved.Host, nil
}

func readPod(
	ctx context.Context, api typedcorev1.PodInterface, target object.Target,
) (*corev1.Pod, error) {
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read Pod: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return nil, err
	}
	return live, nil
}

func podWaitFailure(
	ctx context.Context, api typedcorev1.CoreV1Interface, target object.Target, cause error,
) error {
	if ctx.Err() != nil {
		return fmt.Errorf("wait for Pod phase: %w", cause)
	}
	events, err := api.Events(target.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Pod,involvedObject.name=" + target.Name +
			",involvedObject.namespace=" + target.Namespace,
	})
	if err != nil {
		return fmt.Errorf("list Pod warning events: %w (Pod phase wait: %w)", err, cause)
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
		return fmt.Errorf("wait for Pod phase: %w", cause)
	}
	return fmt.Errorf("wait for Pod phase: %w (%s)", cause, strings.Join(messages, ", "))
}

func podMetadataPatch(
	path string, live, before, after map[string]string,
) []configMapPatchOperation {
	if len(before) == 0 && len(live) != 0 {
		var operations []configMapPatchOperation
		for _, key := range slices.Sorted(maps.Keys(after)) {
			operations = append(operations, configMapPatchOperation{
				Op: "add", Path: configMapPointer(path, key), Value: after[key],
			})
		}
		return operations
	}
	operations := configMapMapPatch(path, before, after)
	slices.SortFunc(operations, func(a, b configMapPatchOperation) int {
		return strings.Compare(a.Path, b.Path)
	})
	return operations
}

func podTrackedMetadata(
	input ServiceMetadata, observed ServiceMetadataOutput,
) ServiceMetadataOutput {
	labels := maps.Clone(observed.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, serviceMap(input.Labels))
	annotations := maps.Clone(observed.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	maps.Copy(annotations, serviceMap(input.Annotations))
	return ServiceMetadataOutput{Labels: labels, Annotations: annotations}
}

func podDuration(value *string, field string) (time.Duration, error) {
	if value == nil {
		return podDefaultWait, nil
	}
	duration, err := time.ParseDuration(*value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", field, err)
	}
	return duration, nil
}

const (
	podDNSPattern           = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	podGeneratedNamePattern = `^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|` +
		`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`
	podLabelKeyPattern = `^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?` +
		`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`
	podAnnotationKeyPattern = `^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?` +
		`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?` +
		`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`
	podMetadataKeyLengths = `^([^/]{1,253}/)?[^/]{1,63}$`
	podLabelValuePattern  = `^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`
	podNumericPortPattern = `^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|` +
		`65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`
	podNamedPortPattern           = `^[a-z0-9]+(-[a-z0-9]+)*$`
	podInvalidRelativePathPattern = `^(/|\.\.)|(^|/)\.\.(/|$)`
	podModePattern                = `^0+[0-7]{0,3}$`
)

func (r PodResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.Metadata.Namespace, "default"),
		defaults.NullableValue(r.Spec.SecurityContext.SeccompProfile.Type, "Unconfined"),
		defaults.NullableValue(r.TargetState, []string{"Running"}),
		defaults.NullableValue(r.CreateTimeout, "5m"),
		defaults.NullableValue(r.DeleteTimeout, "5m"),
	}
}

func (r PodResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name, podDNSPattern),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName, podGeneratedNamePattern)),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key, podLabelKeyPattern),
						constraint.IsMatch(entry.Key, podMetadataKeyLengths),
						constraint.IsMatch(entry.Value, podLabelValuePattern),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key, podAnnotationKeyPattern),
						constraint.IsMatch(entry.Key, podMetadataKeyLengths),
					),
				}
			},
		),
		constraint.When(constraint.Present(r.TargetState)).
			Require(constraint.MinItems(r.TargetState, 1)),
		constraint.ForEach(r.TargetState, func(phase string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(
					constraint.OneOf(phase, "Pending", "Running", "Succeeded", "Failed", "Unknown"),
				),
			}
		}),
		constraint.Must(
			constraint.Any(
				constraint.Equals(r.Spec.SecurityContext.FSGroup, ""),
				constraint.IsFormat(r.Spec.SecurityContext.FSGroup, constraint.FormatInt64),
			),
		),
		constraint.Must(
			constraint.Any(
				constraint.Equals(r.Spec.SecurityContext.RunAsGroup, ""),
				constraint.IsFormat(r.Spec.SecurityContext.RunAsGroup, constraint.FormatInt64),
			),
		),
		constraint.Must(
			constraint.Any(
				constraint.Equals(r.Spec.SecurityContext.RunAsUser, ""),
				constraint.IsFormat(r.Spec.SecurityContext.RunAsUser, constraint.FormatInt64),
			),
		),
		constraint.Must(constraint.AtLeast(r.Spec.ActiveDeadlineSeconds, 1)),
		constraint.When(constraint.Present(r.Spec.DNSPolicy)).
			Require(
				constraint.OneOf(
					r.Spec.DNSPolicy,
					`ClusterFirst`,
					`ClusterFirstWithHostNet`,
					`Default`,
					`None`,
				),
			),
		constraint.When(constraint.Present(r.Spec.RestartPolicy)).
			Require(constraint.OneOf(r.Spec.RestartPolicy, `Always`, `OnFailure`, `Never`)),
		constraint.Must(constraint.AtLeast(r.Spec.TerminationGracePeriodSeconds, 0)),
		constraint.ForEach(
			r.Spec.Container,
			func(container PodContainer) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.Any(
							constraint.Equals(container.SecurityContext.RunAsGroup, ""),
							constraint.IsFormat(container.SecurityContext.RunAsGroup, constraint.FormatInt64),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.Equals(container.SecurityContext.RunAsUser, ""),
							constraint.IsFormat(container.SecurityContext.RunAsUser, constraint.FormatInt64),
						),
					),
					constraint.When(constraint.Present(container.Lifecycle.PostStart.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.Lifecycle.PostStart.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.Lifecycle.PostStart.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(container.Lifecycle.PostStart.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PostStart.TCPSocket.Port,
									podNamedPortPattern,
								),
								constraint.IsMatch(container.Lifecycle.PostStart.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.Lifecycle.PreStop.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.Lifecycle.PreStop.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.Lifecycle.PreStop.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(container.Lifecycle.PreStop.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.Lifecycle.PreStop.TCPSocket.Port, podNamedPortPattern),
								constraint.IsMatch(container.Lifecycle.PreStop.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.LivenessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.LivenessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.LivenessProbe.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(container.LivenessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.LivenessProbe.TCPSocket.Port, podNamedPortPattern),
								constraint.IsMatch(container.LivenessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(container.LivenessProbe.GRPC.Port, 1),
						constraint.AtMost(container.LivenessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.ReadinessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.ReadinessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.ReadinessProbe.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(container.ReadinessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.ReadinessProbe.TCPSocket.Port, podNamedPortPattern),
								constraint.IsMatch(container.ReadinessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(container.ReadinessProbe.GRPC.Port, 1),
						constraint.AtMost(container.ReadinessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.StartupProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.StartupProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.StartupProbe.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(container.StartupProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(container.StartupProbe.TCPSocket.Port, podNamedPortPattern),
								constraint.IsMatch(container.StartupProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(container.StartupProbe.GRPC.Port, 1),
						constraint.AtMost(container.StartupProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.StartupProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.TimeoutSeconds, 1)),
					constraint.ForEach(container.Port, func(port PodPort) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.AtLeast(port.ContainerPort, 1),
								constraint.AtMost(port.ContainerPort, 65535),
							),
							constraint.Must(
								constraint.AtLeast(port.HostPort, 1),
								constraint.AtMost(port.HostPort, 65535),
							),
							constraint.Must(
								constraint.Any(
									constraint.IsMatch(
										port.Name,
										podNumericPortPattern,
									),
									constraint.All(
										constraint.IsMatch(port.Name, podNamedPortPattern),
										constraint.IsMatch(port.Name, `[a-z]`),
										constraint.MaxItems(port.Name, 15),
									),
								),
							),
							constraint.When(constraint.Present(port.Protocol)).
								Require(constraint.OneOf(port.Protocol, `TCP`, `UDP`)),
						}
					}),
					constraint.When(constraint.Present(container.SecurityContext.SeccompProfile.Type)).
						Require(
							constraint.OneOf(
								container.SecurityContext.SeccompProfile.Type,
								`Localhost`,
								`RuntimeDefault`,
								`Unconfined`,
							),
						),
					constraint.When(constraint.Present(container.RestartPolicy)).
						Require(constraint.OneOf(container.RestartPolicy, `Always`)),
					constraint.When(constraint.Present(container.TerminationMessagePolicy)).
						Require(
							constraint.OneOf(container.TerminationMessagePolicy, `File`, `FallbackToLogsOnError`),
						),
					constraint.ForEach(container.VolumeMount, func(volumeMount PodMount) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.When(constraint.Present(volumeMount.MountPropagation)).
								Require(
									constraint.OneOf(volumeMount.MountPropagation, `None`, `HostToContainer`, `Bidirectional`),
								),
						}
					}),
				}
			},
		),
		constraint.ForEach(
			r.Spec.InitContainer,
			func(initContainer PodContainer) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.Any(
							constraint.Equals(initContainer.SecurityContext.RunAsGroup, ""),
							constraint.IsFormat(initContainer.SecurityContext.RunAsGroup, constraint.FormatInt64),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.Equals(initContainer.SecurityContext.RunAsUser, ""),
							constraint.IsFormat(initContainer.SecurityContext.RunAsUser, constraint.FormatInt64),
						),
					),
					constraint.When(constraint.Present(initContainer.Lifecycle.PostStart.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.Lifecycle.PostStart.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PostStart.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PostStart.HTTPGet.Port,
									podNamedPortPattern,
								),
								constraint.IsMatch(initContainer.Lifecycle.PostStart.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PostStart.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PostStart.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PostStart.TCPSocket.Port,
									podNamedPortPattern,
								),
								constraint.IsMatch(initContainer.Lifecycle.PostStart.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PostStart.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(initContainer.Lifecycle.PreStop.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.Lifecycle.PreStop.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PreStop.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PreStop.HTTPGet.Port,
									podNamedPortPattern,
								),
								constraint.IsMatch(initContainer.Lifecycle.PreStop.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PreStop.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PreStop.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PreStop.TCPSocket.Port,
									podNamedPortPattern,
								),
								constraint.IsMatch(initContainer.Lifecycle.PreStop.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PreStop.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(initContainer.LivenessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.LivenessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.LivenessProbe.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(initContainer.LivenessProbe.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(initContainer.LivenessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.LivenessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.LivenessProbe.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(initContainer.LivenessProbe.TCPSocket.Port, podNamedPortPattern),
								constraint.IsMatch(initContainer.LivenessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.LivenessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(initContainer.LivenessProbe.GRPC.Port, 1),
						constraint.AtMost(initContainer.LivenessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(initContainer.ReadinessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.ReadinessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.ReadinessProbe.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(initContainer.ReadinessProbe.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(initContainer.ReadinessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.ReadinessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.ReadinessProbe.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(initContainer.ReadinessProbe.TCPSocket.Port, podNamedPortPattern),
								constraint.IsMatch(initContainer.ReadinessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.ReadinessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(initContainer.ReadinessProbe.GRPC.Port, 1),
						constraint.AtMost(initContainer.ReadinessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(initContainer.StartupProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.StartupProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.StartupProbe.HTTPGet.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(initContainer.StartupProbe.HTTPGet.Port, podNamedPortPattern),
								constraint.IsMatch(initContainer.StartupProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.StartupProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.StartupProbe.TCPSocket.Port,
								podNumericPortPattern,
							),
							constraint.All(
								constraint.IsMatch(initContainer.StartupProbe.TCPSocket.Port, podNamedPortPattern),
								constraint.IsMatch(initContainer.StartupProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.StartupProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(initContainer.StartupProbe.GRPC.Port, 1),
						constraint.AtMost(initContainer.StartupProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.TimeoutSeconds, 1)),
					constraint.ForEach(initContainer.Port, func(port PodPort) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.AtLeast(port.ContainerPort, 1),
								constraint.AtMost(port.ContainerPort, 65535),
							),
							constraint.Must(
								constraint.AtLeast(port.HostPort, 1),
								constraint.AtMost(port.HostPort, 65535),
							),
							constraint.Must(
								constraint.Any(
									constraint.IsMatch(
										port.Name,
										podNumericPortPattern,
									),
									constraint.All(
										constraint.IsMatch(port.Name, podNamedPortPattern),
										constraint.IsMatch(port.Name, `[a-z]`),
										constraint.MaxItems(port.Name, 15),
									),
								),
							),
							constraint.When(constraint.Present(port.Protocol)).
								Require(constraint.OneOf(port.Protocol, `TCP`, `UDP`)),
						}
					}),
					constraint.When(constraint.Present(initContainer.SecurityContext.SeccompProfile.Type)).
						Require(
							constraint.OneOf(
								initContainer.SecurityContext.SeccompProfile.Type,
								`Localhost`,
								`RuntimeDefault`,
								`Unconfined`,
							),
						),
					constraint.When(constraint.Present(initContainer.RestartPolicy)).
						Require(constraint.OneOf(initContainer.RestartPolicy, `Always`)),
					constraint.When(constraint.Present(initContainer.TerminationMessagePolicy)).
						Require(
							constraint.OneOf(initContainer.TerminationMessagePolicy, `File`, `FallbackToLogsOnError`),
						),
					constraint.ForEach(initContainer.VolumeMount, func(mount PodMount) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.When(constraint.Present(mount.MountPropagation)).
								Require(
									constraint.OneOf(mount.MountPropagation, `None`, `HostToContainer`, `Bidirectional`),
								),
						}
					}),
				}
			},
		),
		constraint.ForEach(
			r.Spec.HostAliases,
			func(hostAliases PodHostAlias) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(hostAliases.IP, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.DNSConfig.Nameservers,
			func(nameservers string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(nameservers, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.DNSConfig.Searches,
			func(searches string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							searches,
							`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`,
						),
						constraint.MaxItems(searches, 253),
					),
				}
			},
		),
		constraint.When(constraint.Present(r.Spec.SecurityContext.SeccompProfile.Type)).
			Require(
				constraint.OneOf(
					r.Spec.SecurityContext.SeccompProfile.Type,
					`Localhost`,
					`RuntimeDefault`,
					`Unconfined`,
				),
			),
		constraint.When(constraint.Present(r.Spec.SecurityContext.FSGroupChangePolicy)).
			Require(
				constraint.OneOf(
					r.Spec.SecurityContext.FSGroupChangePolicy,
					`Always`,
					`OnRootMismatch`,
				),
			),
		constraint.When(constraint.Present(r.Spec.OS)).
			Require(constraint.OneOf(r.Spec.OS.Name, `linux`, `windows`)),
		constraint.ForEach(
			r.Spec.Toleration,
			func(toleration PodToleration) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.Any(
							constraint.Equals(toleration.TolerationSeconds, ""),
							constraint.IsFormat(toleration.TolerationSeconds, constraint.FormatInt64),
						),
					),
					constraint.When(constraint.Present(toleration.Effect)).
						Require(constraint.OneOf(toleration.Effect, `NoSchedule`, `PreferNoSchedule`, `NoExecute`)),
					constraint.When(constraint.Present(toleration.Operator)).
						Require(constraint.OneOf(toleration.Operator, `Exists`, `Equal`)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.TopologySpreadConstraint,
			func(spread PodTopologySpreadConstraint) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.AtLeast(spread.MaxSkew, 1)),
					constraint.Must(constraint.AtLeast(spread.MinDomains, 1)),
					constraint.When(constraint.Present(spread.NodeAffinityPolicy)).
						Require(constraint.OneOf(spread.NodeAffinityPolicy, `Honor`, `Ignore`)),
					constraint.When(constraint.Present(spread.NodeTaintsPolicy)).
						Require(constraint.OneOf(spread.NodeTaintsPolicy, `Honor`, `Ignore`)),
					constraint.When(constraint.Present(spread.WhenUnsatisfiable)).
						Require(constraint.OneOf(spread.WhenUnsatisfiable, `DoNotSchedule`, `ScheduleAnyway`)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
				NodeSelectorTerm,
			func(term PodNodeTerm) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.ForEach(
						term.MatchExpressions,
						func(expression PodNodeExpression) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(expression.Operator)).
									Require(
										constraint.OneOf(
											expression.Operator,
											`In`,
											`NotIn`,
											`Exists`,
											`DoesNotExist`,
											`Gt`,
											`Lt`,
										),
									),
							}
						},
					),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
			func(preferred PodPreferredSchedulingTerm) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.ForEach(
						preferred.Preference.MatchExpressions,
						func(expression PodNodeExpression) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(expression.Operator)).
									Require(
										constraint.OneOf(
											expression.Operator,
											`In`,
											`NotIn`,
											`Exists`,
											`DoesNotExist`,
											`Gt`,
											`Lt`,
										),
									),
							}
						},
					),
				}
			},
		),
		constraint.ForEach(r.Spec.Volume, func(volume PodVolume) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(
					constraint.IsMatch(volume.ConfigMap.DefaultMode, podModePattern),
				),
				constraint.ForEach(volume.ConfigMap.Items, func(items PodKeyPath) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.When(constraint.Present(items.Path)).
							Require(constraint.Not(constraint.IsMatch(items.Path, podInvalidRelativePathPattern))),
						constraint.Must(
							constraint.IsMatch(items.Mode, podModePattern),
						),
					}
				}),
				constraint.Must(
					constraint.IsMatch(volume.Secret.DefaultMode, podModePattern),
				),
				constraint.ForEach(volume.Secret.Items, func(items PodKeyPath) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.When(constraint.Present(items.Path)).
							Require(constraint.Not(constraint.IsMatch(items.Path, podInvalidRelativePathPattern))),
						constraint.Must(
							constraint.IsMatch(items.Mode, podModePattern),
						),
					}
				}),
				constraint.Must(
					constraint.IsMatch(volume.DownwardAPI.DefaultMode, podModePattern),
				),
				constraint.ForEach(volume.DownwardAPI.Items, func(i PodDownwardItem) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.When(constraint.Present(i.Path)).
							Require(constraint.Not(constraint.IsMatch(i.Path, podInvalidRelativePathPattern))),
						constraint.Must(
							constraint.IsMatch(i.Mode, podModePattern),
						),
					}
				}),
				constraint.When(constraint.Present(volume.EmptyDir.Medium)).
					Require(
						constraint.OneOf(
							volume.EmptyDir.Medium,
							``,
							`Memory`,
							`HugePages`,
							`HugePages-2Mi`,
							`HugePages-1Gi`,
						),
					),
				constraint.When(constraint.Present(volume.GitRepo.Directory)).Require(
					constraint.Not(constraint.IsMatch(volume.GitRepo.Directory, podInvalidRelativePathPattern)),
				),
				constraint.Must(
					constraint.IsMatch(volume.Projected.DefaultMode, podModePattern),
				),
				constraint.ForEach(
					volume.Projected.Sources,
					func(sources PodProjectionGroup) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.ForEach(sources.Secret, func(secret PodSecretProjection) []constraint.Constraint {
								return []constraint.Constraint{
									constraint.ForEach(secret.Items, func(items PodKeyPath) []constraint.Constraint {
										return []constraint.Constraint{
											constraint.When(constraint.Present(items.Path)).
												Require(constraint.Not(constraint.IsMatch(items.Path, podInvalidRelativePathPattern))),
											constraint.Must(
												constraint.IsMatch(items.Mode, podModePattern),
											),
										}
									}),
								}
							}),
							constraint.ForEach(
								sources.ConfigMap,
								func(configMap PodConfigMapProjection) []constraint.Constraint {
									return []constraint.Constraint{
										constraint.ForEach(configMap.Items, func(items PodKeyPath) []constraint.Constraint {
											return []constraint.Constraint{
												constraint.When(constraint.Present(items.Path)).
													Require(constraint.Not(constraint.IsMatch(items.Path, podInvalidRelativePathPattern))),
												constraint.Must(
													constraint.IsMatch(items.Mode, podModePattern),
												),
											}
										}),
									}
								},
							),
							constraint.ForEach(
								sources.DownwardAPI.Items,
								func(items PodProjectedDownwardItem) []constraint.Constraint {
									return []constraint.Constraint{
										constraint.When(constraint.Present(items.Path)).
											Require(constraint.Not(constraint.IsMatch(items.Path, podInvalidRelativePathPattern))),
										constraint.Must(
											constraint.IsMatch(items.Mode, podModePattern),
										),
									}
								},
							),
							constraint.Must(constraint.AtLeast(sources.ServiceAccountToken.ExpirationSeconds, 600)),
						}
					},
				),
				constraint.Must(constraint.MinItems(volume.Projected.Sources, 1)),
				constraint.ForEach(
					volume.Ephemeral.VolumeClaimTemplate.Metadata.Labels,
					func(entry constraint.MapEntry[string]) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.IsMatch(entry.Key, podLabelKeyPattern),
								constraint.IsMatch(entry.Key, podMetadataKeyLengths),
								constraint.IsMatch(entry.Value, podLabelValuePattern),
							),
						}
					},
				),
				constraint.ForEach(
					volume.Ephemeral.VolumeClaimTemplate.Metadata.Annotations,
					func(entry constraint.MapEntry[string]) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.IsMatch(entry.Key, podAnnotationKeyPattern),
								constraint.IsMatch(entry.Key, podMetadataKeyLengths),
							),
						}
					},
				),
				constraint.ForEach(
					volume.Ephemeral.VolumeClaimTemplate.Spec.AccessModes,
					func(mode string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.OneOf(
									mode,
									`ReadWriteOnce`,
									`ReadOnlyMany`,
									`ReadWriteMany`,
									`ReadWriteOncePod`,
								),
							),
						}
					},
				),
				constraint.When(constraint.Present(volume.Ephemeral.VolumeClaimTemplate.Spec.VolumeMode)).
					Require(
						constraint.OneOf(volume.Ephemeral.VolumeClaimTemplate.Spec.VolumeMode, `Block`, `Filesystem`),
					),
				constraint.When(constraint.Present(volume.HostPath.Type)).
					Require(
						constraint.OneOf(
							volume.HostPath.Type,
							``,
							`DirectoryOrCreate`,
							`Directory`,
							`FileOrCreate`,
							`File`,
							`Socket`,
							`CharDevice`,
							`BlockDevice`,
						),
					),
			}
		}),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.DeleteTimeout, constraint.FormatPositiveDuration)),
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
	}
}
