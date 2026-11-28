package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type ReplicationControllerResource struct {
	Metadata          ServiceMetadata
	Spec              ReplicationControllerSpec
	CreateTimeout     *string
	UpdateTimeout     *string
	DeleteTimeout     *string
	IgnoreLabels      *[]string
	IgnoreAnnotations *[]string
	capturePrior      *replicationControllerPrior `ub:"-"`
}

type ReplicationControllerResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	UID        string
	Metadata   ServiceMetadataOutput
	Spec       ReplicationControllerSpecOutput
}

type replicationControllerPrior = runtime.Prior[
	ReplicationControllerResource, *ReplicationControllerResourceOutput, *config.Configuration,
]

func replicationControllerDuration(value *string, field string) (time.Duration, error) {
	if value == nil {
		return 10 * time.Minute, nil
	}
	d, err := time.ParseDuration(*value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", field, err)
	}
	return d, nil
}

func (r *ReplicationControllerResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ReplicationControllerResourceOutput, error) {
	desired, err := r.prepare()
	if err != nil {
		return nil, err
	}
	timeout, err := replicationControllerDuration(r.CreateTimeout, "create-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	api, server, err := podClient(c, "")
	if err != nil {
		return nil, err
	}
	controllers := api.ReplicationControllers(desired.Namespace)
	created, err := controllers.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create ReplicationController: %w", err)
	}
	target := object.Target{Server: server, APIVersion: "v1", Kind: "ReplicationController",
		Name: created.Name, Namespace: created.Namespace, UID: string(created.UID)}
	if target.Name == "" || target.Namespace == "" || target.UID == "" {
		return nil, fmt.Errorf("created ReplicationController has no name, namespace, or UID")
	}
	if err := replicationControllerWait(ctx, controllers, target); err != nil {
		return nil, err
	}
	live, err := replicationControllerRead(ctx, controllers, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, server, podTrackedMetadata(r.Metadata, ServiceMetadataOutput{}))
}

func (r *ReplicationControllerResource) Read(
	ctx context.Context, c *config.Configuration, prior replicationControllerPrior,
) (*ReplicationControllerResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = prior
		return prior.Outputs, nil
	}
	target, err := prior.Outputs.target()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	api, _, err := podClient(c, target.Server)
	if err != nil {
		return nil, err
	}
	live, err := replicationControllerRead(ctx, api.ReplicationControllers(target.Namespace), target)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	saved := prior.Inputs
	saved.IgnoreLabels = podSavedIgnorePatterns(saved.IgnoreLabels)
	saved.IgnoreAnnotations = podSavedIgnorePatterns(saved.IgnoreAnnotations)
	return saved.output(live, target.Server,
		podTrackedMetadata(saved.Metadata, ServiceMetadataOutput{}))
}

func (r *ReplicationControllerResource) Update(
	ctx context.Context, c *config.Configuration, prior replicationControllerPrior,
) (*ReplicationControllerResourceOutput, error) {
	target, err := prior.Outputs.target()
	if err != nil {
		return nil, err
	}
	if _, err := r.prepare(); err != nil {
		return nil, err
	}
	metadata, spec := replicationControllerImmutableChanges(r, prior, true)
	if metadata || spec {
		return nil, fmt.Errorf("immutable ReplicationController change requires replacement")
	}
	timeout, err := replicationControllerDuration(r.UpdateTimeout, "update-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	api, _, err := podClient(c, target.Server)
	if err != nil {
		return nil, err
	}
	controllers := api.ReplicationControllers(target.Namespace)
	live, err := replicationControllerRead(ctx, controllers, target)
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
	_, spec = replicationControllerImmutableChanges(r, prior, false)
	if spec {
		return nil, fmt.Errorf("immutable ReplicationController drift requires replacement")
	}
	desiredInput := *r
	// Use a deep copy so computed-value adoption does not change saved desired inputs.
	encoded, err := ub.Marshal(r.Spec)
	if err != nil {
		return nil, err
	}
	if err := ub.Unmarshal(encoded, &desiredInput.Spec); err != nil {
		return nil, err
	}
	replicationControllerComputed(reflect.ValueOf(&desiredInput.Spec), reflect.ValueOf(observed.Spec))
	desired, err := desiredInput.prepare()
	if err != nil {
		return nil, err
	}
	operations := []configMapPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	for _, field := range []struct {
		name          string
		before, after map[string]string
	}{
		{"annotations", observed.Metadata.Annotations, desired.Annotations},
		{"labels", observed.Metadata.Labels, desired.Labels},
	} {
		patch := configMapMapPatch("/metadata/"+field.name, field.before, field.after)
		slices.SortFunc(patch, func(a, b configMapPatchOperation) int {
			return strings.Compare(a.Path, b.Path)
		})
		operations = append(operations, patch...)
	}
	if (replicationControllerCompare{input: reflect.ValueOf(r.Spec)}).changed(
		reflect.TypeFor[ReplicationControllerSpec](), reflect.ValueOf(observed.Spec),
		reflect.ValueOf(r.Spec)) {
		operations = append(operations, configMapPatchOperation{Op: "replace", Path: "/spec",
			Value: desired.Spec})
	}
	if len(operations) == 1 {
		return observed, nil
	}
	body, err := json.Marshal(operations)
	if err != nil {
		return nil, err
	}
	if _, err := controllers.Patch(ctx, target.Name, types.JSONPatchType, body,
		metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch ReplicationController: %w", err)
	}
	if err := replicationControllerWait(ctx, controllers, target); err != nil {
		return nil, err
	}
	live, err = replicationControllerRead(ctx, controllers, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, target.Server, podTrackedMetadata(r.Metadata, ServiceMetadataOutput{}))
}

func (r *ReplicationControllerResource) Delete(
	ctx context.Context, c *config.Configuration, prior replicationControllerPrior,
) error {
	target, err := prior.Outputs.target()
	if err != nil {
		return err
	}
	timeout, err := replicationControllerDuration(prior.Inputs.DeleteTimeout, "delete-timeout")
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Minute
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	api, _, err := podClient(c, target.Server)
	if err != nil {
		return err
	}
	controllers := api.ReplicationControllers(target.Namespace)
	if _, err := replicationControllerRead(ctx, controllers, target); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	body, err := json.Marshal([]configMapPatchOperation{
		{Op: "test", Path: "/metadata/uid", Value: target.UID},
		{Op: "replace", Path: "/spec/replicas", Value: 0},
	})
	if err != nil {
		return err
	}
	if _, err := controllers.Patch(ctx, target.Name, types.JSONPatchType, body,
		metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("drain ReplicationController: %w", err)
	}
	if err := replicationControllerWait(ctx, controllers, target); err != nil {
		return err
	}
	return object.Delete(parent, c, target, metav1.DeleteOptions{
		PropagationPolicy: new(metav1.DeletePropagationForeground),
	}, timeout)
}

func (o *ReplicationControllerResourceOutput) target() (object.Target, error) {
	if o == nil || o.Server == "" || o.APIVersion != "v1" || o.Kind != "ReplicationController" ||
		o.Namespace == "" || o.Name == "" || o.UID == "" {
		return object.Target{}, fmt.Errorf("missing ReplicationController identity in prior state")
	}
	return object.Target{Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Namespace: o.Namespace, Name: o.Name, UID: o.UID}, nil
}

func replicationControllerRead(
	ctx context.Context, api typedcorev1.ReplicationControllerInterface, target object.Target,
) (*corev1.ReplicationController, error) {
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read ReplicationController: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return nil, err
	}
	return live, nil
}

func replicationControllerWait(
	ctx context.Context, api typedcorev1.ReplicationControllerInterface, target object.Target,
) error {
	err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			live, err := replicationControllerRead(ctx, api, target)
			if err != nil {
				return false, err
			}
			if live.Spec.Replicas == nil {
				return false, fmt.Errorf("ReplicationController has no replica count")
			}
			return live.Status.FullyLabeledReplicas == *live.Spec.Replicas, nil
		})
	if err != nil {
		return fmt.Errorf("wait for fully labelled ReplicationController replicas: %w", err)
	}
	return nil
}

func (r ReplicationControllerResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.Metadata.Namespace, "default"),
		defaults.NullableValue(r.CreateTimeout, "10m"),
		defaults.NullableValue(r.UpdateTimeout, "10m"),
		defaults.NullableValue(r.DeleteTimeout, "10m"),
	}
}

func (r ReplicationControllerResource) Constraints() []constraint.Constraint {
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
		constraint.AtMostOneOf(r.Spec.Template.Metadata.Name, r.Spec.Template.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Spec.Template.Metadata.Name, podDNSPattern),
			constraint.MaxItems(r.Spec.Template.Metadata.Name, 253),
		),
		constraint.Must(constraint.IsMatch(r.Spec.Template.Metadata.GenerateName,
			podGeneratedNamePattern)),
		constraint.ForEach(
			r.Spec.Template.Metadata.Labels,
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
			r.Spec.Template.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key, podAnnotationKeyPattern),
						constraint.IsMatch(entry.Key, podMetadataKeyLengths),
					),
				}
			},
		),
		constraint.Must(
			constraint.Any(
				constraint.Equals(r.Spec.Template.Spec.SecurityContext.FSGroup, ""),
				constraint.IsFormat(r.Spec.Template.Spec.SecurityContext.FSGroup, constraint.FormatInt64),
			),
		),
		constraint.Must(
			constraint.Any(
				constraint.Equals(r.Spec.Template.Spec.SecurityContext.RunAsGroup, ""),
				constraint.IsFormat(r.Spec.Template.Spec.SecurityContext.RunAsGroup, constraint.FormatInt64),
			),
		),
		constraint.Must(
			constraint.Any(
				constraint.Equals(r.Spec.Template.Spec.SecurityContext.RunAsUser, ""),
				constraint.IsFormat(r.Spec.Template.Spec.SecurityContext.RunAsUser, constraint.FormatInt64),
			),
		),
		constraint.Must(constraint.AtLeast(r.Spec.Template.Spec.ActiveDeadlineSeconds, 1)),
		constraint.When(constraint.Present(r.Spec.Template.Spec.DNSPolicy)).
			Require(
				constraint.OneOf(
					r.Spec.Template.Spec.DNSPolicy,
					`ClusterFirst`,
					`ClusterFirstWithHostNet`,
					`Default`,
					`None`,
				),
			),
		constraint.When(constraint.Present(r.Spec.Template.Spec.RestartPolicy)).
			Require(constraint.OneOf(r.Spec.Template.Spec.RestartPolicy, `Always`, `OnFailure`, `Never`)),
		constraint.Must(constraint.AtLeast(r.Spec.Template.Spec.TerminationGracePeriodSeconds, 0)),
		constraint.ForEach(
			r.Spec.Template.Spec.Container,
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
			r.Spec.Template.Spec.InitContainer,
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
			r.Spec.Template.Spec.HostAliases,
			func(hostAliases PodHostAlias) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(hostAliases.IP, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.DNSConfig.Nameservers,
			func(nameservers string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(nameservers, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.DNSConfig.Searches,
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
		constraint.When(constraint.Present(r.Spec.Template.Spec.SecurityContext.SeccompProfile.Type)).
			Require(
				constraint.OneOf(
					r.Spec.Template.Spec.SecurityContext.SeccompProfile.Type,
					`Localhost`,
					`RuntimeDefault`,
					`Unconfined`,
				),
			),
		constraint.When(constraint.Present(r.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy)).
			Require(
				constraint.OneOf(
					r.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy,
					`Always`,
					`OnRootMismatch`,
				),
			),
		constraint.When(constraint.Present(r.Spec.Template.Spec.OS)).
			Require(constraint.OneOf(r.Spec.Template.Spec.OS.Name, `linux`, `windows`)),
		constraint.ForEach(
			r.Spec.Template.Spec.Toleration,
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
			r.Spec.Template.Spec.TopologySpreadConstraint,
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
			r.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
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
			r.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
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
		constraint.ForEach(r.Spec.Template.Spec.Volume, func(volume PodVolume) []constraint.Constraint {
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
		constraint.Must(constraint.IsFormat(r.UpdateTimeout, constraint.FormatPositiveDuration)),
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
