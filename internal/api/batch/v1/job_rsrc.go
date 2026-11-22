package v1

import (
	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

// JobResource manages a namespaced Job with completion waits enabled by default.
// Failed completion returns an error without deleting the created Job. A completion
// wait that observes a missing Job returns its last successful API values.
// Standalone reads have a 20m budget and never wait for completion.
type JobResource struct {
	capture *prior `ub:"-"`

	// Metadata defaults the namespace to default. Omitted labels retain observed values;
	// omitted annotations clear selected keys. Reads filter untracked internal keys
	// and Job-generated labels according to manual-selector.
	// Set exactly one nonempty name or generate-name.
	Metadata Metadata
	// Spec sets the Job. Immutable changes require replacement.
	Spec JobSpec
	// WaitForCompletion defaults true and accepts ordered Complete/Failed conditions
	// or disappearance. Counts alone do not end the wait.
	WaitForCompletion *bool
	// CreateTimeout is one positive duration budget for creation and polling, default 1m.
	CreateTimeout *string
	// UpdateTimeout is one positive duration budget for refresh, patch and polling, default 1m.
	UpdateTimeout *string
	// DeleteTimeout is one positive duration budget for discovery and foreground deletion,
	// default 1m. Deletion preserves finalizers and waits for the saved UID to disappear.
	DeleteTimeout *string
	// IgnoreAnnotations filters untracked outer annotation keys by regular expression during reads.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked outer label keys by regular expression during reads.
	IgnoreLabels *[]string
}

// JobResourceOutput contains stable identity and normalized metadata/spec without status.
type JobResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   Metadata
	Spec       JobSpec
}
type prior = runtime.Prior[JobResource, *JobResourceOutput, *config.Configuration]

func (r JobResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(
				r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z`+
					`0-9])?)*$`,
			),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.Must(
			constraint.Any(
				constraint.All(
					constraint.IsMatch(
						r.Metadata.GenerateName,
						`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`,
					),
					constraint.MaxItems(r.Metadata.GenerateName, 63),
				),
				constraint.All(
					constraint.IsMatch(
						r.Metadata.GenerateName,
						`^([\x00-\x7f]|[a-z0-9][-a-z0-9]*[\x00-\x7f])-$`,
					),
					constraint.MaxItems(r.Metadata.GenerateName, 64),
				),
			),
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-`+
								`z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`,
						),
						constraint.IsMatch(
							entry.Key,
							`^([^/]{1,253}/)?[^/]{1,63}$`,
						),
					),
					constraint.Must(
						constraint.IsMatch(
							entry.Value,
							`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`,
						),
						constraint.MaxItems(entry.Value, 63),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([A-Za-zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?(\.[A-Za-z`+
								`Kİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?)*/)?[A-Za-zKİ0-9](`+
								`[-A-Za-zKİ0-9_.]*[A-Za-zKİ0-9])?$`,
						),
						constraint.IsMatch(
							entry.Key,
							`^([^/]{1,253}/)?[^/]{1,63}$`,
						),
					),
				}
			},
		),
		constraint.Must(constraint.AtLeast(r.Spec.ActiveDeadlineSeconds, 1)),
		constraint.Must(constraint.AtLeast(r.Spec.BackoffLimit, 0)),
		constraint.Must(constraint.AtLeast(r.Spec.BackoffLimitPerIndex, 0)),
		constraint.Must(constraint.AtLeast(r.Spec.Completions, 1)),
		constraint.When(constraint.Present(r.Spec.CompletionMode)).
			Require(
				constraint.OneOf(r.Spec.CompletionMode, `Indexed`, `NonIndexed`),
			),
		constraint.Must(constraint.AtLeast(r.Spec.MaxFailedIndexes, 0)),
		constraint.Must(constraint.AtLeast(r.Spec.Parallelism, 0)),
		constraint.When(constraint.Present(r.Spec.PodFailurePolicy)).
			Require(constraint.NotEmpty(r.Spec.PodFailurePolicy.Rule)),
		constraint.ForEach(
			r.Spec.PodFailurePolicy.Rule,
			func(podFailureRule PodFailureRule) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(podFailureRule.OnExitCodes)).
						Require(constraint.NotEmpty(podFailureRule.OnExitCodes.Values)),
					constraint.Must(constraint.MaxItems(podFailureRule.OnExitCodes.Values, 255)),
					constraint.ForEach(
						podFailureRule.OnExitCodes.Values,
						func(code int64) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.Must(constraint.NotEquals(code, 0)),
							}
						},
					),
				}
			},
		),
		constraint.AtMostOneOf(r.Spec.Template.Metadata.Name, r.Spec.Template.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(
				r.Spec.Template.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z`+
					`0-9])?)*$`,
			),
			constraint.MaxItems(r.Spec.Template.Metadata.Name, 253),
		),
		constraint.Must(
			constraint.Any(
				constraint.All(
					constraint.IsMatch(
						r.Spec.Template.Metadata.GenerateName,
						`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`,
					),
					constraint.MaxItems(r.Spec.Template.Metadata.GenerateName, 63),
				),
				constraint.All(
					constraint.IsMatch(
						r.Spec.Template.Metadata.GenerateName,
						`^([\x00-\x7f]|[a-z0-9][-a-z0-9]*[\x00-\x7f])-$`,
					),
					constraint.MaxItems(r.Spec.Template.Metadata.GenerateName, 64),
				),
			),
		),
		constraint.ForEach(
			r.Spec.Template.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-`+
								`z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`,
						),
						constraint.IsMatch(
							entry.Key,
							`^([^/]{1,253}/)?[^/]{1,63}$`,
						),
					),
					constraint.Must(
						constraint.IsMatch(
							entry.Value,
							`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`,
						),
						constraint.MaxItems(entry.Value, 63),
					),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([A-Za-zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?(\.[A-Za-z`+
								`Kİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?)*/)?[A-Za-zKİ0-9](`+
								`[-A-Za-zKİ0-9_.]*[A-Za-zKİ0-9])?$`,
						),
						constraint.IsMatch(
							entry.Key,
							`^([^/]{1,253}/)?[^/]{1,63}$`,
						),
					),
				}
			},
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
			Require(
				constraint.OneOf(r.Spec.Template.Spec.RestartPolicy, `Always`, `OnFailure`, `Never`),
			),
		constraint.Must(constraint.AtLeast(r.Spec.Template.Spec.TerminationGracePeriodSeconds, 0)),
		constraint.ForEach(
			r.Spec.Template.Spec.Container,
			func(container Container) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(container.Lifecycle.PostStart.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.Lifecycle.PostStart.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PostStart.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PostStart.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PostStart.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PostStart.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.Lifecycle.PreStop.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.Lifecycle.PreStop.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PreStop.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PreStop.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PreStop.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PreStop.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.LivenessProbe.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.LivenessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.LivenessProbe.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.LivenessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.LivenessProbe.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.LivenessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.GRPC.Port, 1),
						constraint.AtMost(container.LivenessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.ReadinessProbe.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.ReadinessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.ReadinessProbe.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.ReadinessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.ReadinessProbe.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.ReadinessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.GRPC.Port, 1),
						constraint.AtMost(container.ReadinessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.StartupProbe.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.StartupProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.StartupProbe.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.StartupProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.StartupProbe.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.StartupProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(constraint.AtLeast(container.StartupProbe.GRPC.Port, 1),
						constraint.AtMost(container.StartupProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.StartupProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.TimeoutSeconds, 1)),
					constraint.ForEach(
						container.Port,
						func(port Port) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.Must(constraint.AtLeast(port.ContainerPort, 1),
									constraint.AtMost(port.ContainerPort, 65535),
								),
								constraint.Must(constraint.AtLeast(port.HostPort, 1),
									constraint.AtMost(port.HostPort, 65535),
								),
								constraint.Must(
									constraint.Any(
										constraint.IsMatch(
											port.Name,
											`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
												`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
										),
										constraint.All(constraint.IsMatch(port.Name, `^[a-z0-9]+(-[a-z0-9]+)*$`),
											constraint.IsMatch(port.Name, `[a-z]`),
											constraint.MaxItems(port.Name, 15),
										),
									),
								),
								constraint.When(constraint.Present(port.Protocol)).
									Require(
										constraint.OneOf(port.Protocol, `TCP`, `UDP`),
									),
							}
						},
					),
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
						Require(
							constraint.OneOf(container.RestartPolicy, `Always`),
						),
					constraint.When(constraint.Present(container.TerminationMessagePolicy)).
						Require(
							constraint.OneOf(container.TerminationMessagePolicy, `File`, `FallbackToLogsOnError`),
						),
					constraint.ForEach(
						container.VolumeMount,
						func(mount Mount) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(mount.MountPropagation)).
									Require(
										constraint.OneOf(mount.MountPropagation, `None`, `HostToContainer`, `Bidirectional`),
									),
							}
						},
					),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.InitContainer,
			func(container Container) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(container.Lifecycle.PostStart.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.Lifecycle.PostStart.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PostStart.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PostStart.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PostStart.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PostStart.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.Lifecycle.PreStop.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.Lifecycle.PreStop.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PreStop.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PreStop.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PreStop.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PreStop.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.LivenessProbe.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.LivenessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.LivenessProbe.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.LivenessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.LivenessProbe.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.LivenessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.GRPC.Port, 1),
						constraint.AtMost(container.LivenessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.ReadinessProbe.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.ReadinessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.ReadinessProbe.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.ReadinessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.ReadinessProbe.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.ReadinessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.GRPC.Port, 1),
						constraint.AtMost(container.ReadinessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.StartupProbe.HTTPGet.Scheme)).
						Require(
							constraint.OneOf(container.StartupProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`),
						),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.StartupProbe.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.StartupProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
									`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.StartupProbe.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.StartupProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(constraint.AtLeast(container.StartupProbe.GRPC.Port, 1),
						constraint.AtMost(container.StartupProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.StartupProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.TimeoutSeconds, 1)),
					constraint.ForEach(
						container.Port,
						func(port Port) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.Must(constraint.AtLeast(port.ContainerPort, 1),
									constraint.AtMost(port.ContainerPort, 65535),
								),
								constraint.Must(constraint.AtLeast(port.HostPort, 1),
									constraint.AtMost(port.HostPort, 65535),
								),
								constraint.Must(
									constraint.Any(
										constraint.IsMatch(
											port.Name,
											`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0`+
												`-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
										),
										constraint.All(constraint.IsMatch(port.Name, `^[a-z0-9]+(-[a-z0-9]+)*$`),
											constraint.IsMatch(port.Name, `[a-z]`),
											constraint.MaxItems(port.Name, 15),
										),
									),
								),
								constraint.When(constraint.Present(port.Protocol)).
									Require(
										constraint.OneOf(port.Protocol, `TCP`, `UDP`),
									),
							}
						},
					),
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
						Require(
							constraint.OneOf(container.RestartPolicy, `Always`),
						),
					constraint.When(constraint.Present(container.TerminationMessagePolicy)).
						Require(
							constraint.OneOf(container.TerminationMessagePolicy, `File`, `FallbackToLogsOnError`),
						),
					constraint.ForEach(
						container.VolumeMount,
						func(mount Mount) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(mount.MountPropagation)).
									Require(
										constraint.OneOf(mount.MountPropagation, `None`, `HostToContainer`, `Bidirectional`),
									),
							}
						},
					),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.HostAliases,
			func(hostAlias HostAlias) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(hostAlias.IP, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.DNSConfig.Nameservers,
			func(ip string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(ip, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.DNSConfig.Searches,
			func(name string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							name,
							`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z`+
								`0-9])?)*$`,
						),
						constraint.MaxItems(name, 253),
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
		constraint.When(constraint.Present(r.Spec.Template.Spec.OS.Name)).
			Require(
				constraint.OneOf(r.Spec.Template.Spec.OS.Name, `linux`, `windows`),
			),
		constraint.ForEach(
			r.Spec.Template.Spec.Toleration,
			func(toleration Toleration) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(toleration.Effect)).
						Require(
							constraint.OneOf(toleration.Effect, `NoSchedule`, `PreferNoSchedule`, `NoExecute`),
						),
					constraint.When(constraint.Present(toleration.Operator)).
						Require(
							constraint.OneOf(toleration.Operator, `Exists`, `Equal`),
						),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.TopologySpreadConstraint,
			func(topologySpreadConstraint TopologySpreadConstraint) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.AtLeast(topologySpreadConstraint.MaxSkew, 1)),
					constraint.Must(constraint.AtLeast(topologySpreadConstraint.MinDomains, 1)),
					constraint.When(constraint.Present(topologySpreadConstraint.NodeAffinityPolicy)).
						Require(
							constraint.OneOf(topologySpreadConstraint.NodeAffinityPolicy, `Honor`, `Ignore`),
						),
					constraint.When(constraint.Present(topologySpreadConstraint.NodeTaintsPolicy)).
						Require(
							constraint.OneOf(topologySpreadConstraint.NodeTaintsPolicy, `Honor`, `Ignore`),
						),
					constraint.When(constraint.Present(topologySpreadConstraint.WhenUnsatisfiable)).
						Require(
							constraint.OneOf(
								topologySpreadConstraint.WhenUnsatisfiable,
								`DoNotSchedule`,
								`ScheduleAnyway`,
							),
						),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
				NodeSelectorTerm,
			func(nodeTerm NodeTerm) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.ForEach(
						nodeTerm.MatchExpressions,
						func(nodeExpression NodeExpression) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(nodeExpression.Operator)).
									Require(
										constraint.OneOf(
											nodeExpression.Operator,
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
			func(preferredSchedulingTerm PreferredSchedulingTerm) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.ForEach(
						preferredSchedulingTerm.Preference.MatchExpressions,
						func(nodeExpression NodeExpression) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(nodeExpression.Operator)).
									Require(
										constraint.OneOf(
											nodeExpression.Operator,
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
			r.Spec.Template.Spec.Volume,
			func(volume Volume) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(volume.ConfigMap.DefaultMode, `^0[0-7]*$`),
						constraint.IsMatch(
							volume.ConfigMap.DefaultMode,
							`^0*[0-7]{0,3}$`,
						),
					),
					constraint.ForEach(
						volume.ConfigMap.Items,
						func(keyPath KeyPath) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(keyPath.Path)).
									Require(
										constraint.Not(constraint.IsMatch(keyPath.Path, `^/|^\.\.|(^|/)\.\.(/|$)`)),
									),
								constraint.Must(constraint.IsMatch(keyPath.Mode, `^0[0-7]*$`),
									constraint.IsMatch(
										keyPath.Mode,
										`^0*[0-7]{0,3}$`,
									),
								),
							}
						},
					),
					constraint.Must(constraint.IsMatch(volume.Secret.DefaultMode, `^0[0-7]*$`),
						constraint.IsMatch(
							volume.Secret.DefaultMode,
							`^0*[0-7]{0,3}$`,
						),
					),
					constraint.ForEach(
						volume.Secret.Items,
						func(keyPath KeyPath) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(keyPath.Path)).
									Require(
										constraint.Not(constraint.IsMatch(keyPath.Path, `^/|^\.\.|(^|/)\.\.(/|$)`)),
									),
								constraint.Must(constraint.IsMatch(keyPath.Mode, `^0[0-7]*$`),
									constraint.IsMatch(
										keyPath.Mode,
										`^0*[0-7]{0,3}$`,
									),
								),
							}
						},
					),
					constraint.Must(constraint.IsMatch(volume.DownwardAPI.DefaultMode, `^0[0-7]*$`),
						constraint.IsMatch(
							volume.DownwardAPI.DefaultMode,
							`^0*[0-7]{0,3}$`,
						),
					),
					constraint.ForEach(
						volume.DownwardAPI.Items,
						func(downwardItem DownwardItem) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(downwardItem.Path)).
									Require(
										constraint.Not(constraint.IsMatch(downwardItem.Path, `^/|^\.\.|(^|/)\.\.(/|$)`)),
									),
								constraint.Must(constraint.IsMatch(downwardItem.Mode, `^0[0-7]*$`),
									constraint.IsMatch(
										downwardItem.Mode,
										`^0*[0-7]{0,3}$`,
									),
								),
							}
						},
					),
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
					constraint.When(constraint.Present(volume.GitRepo.Directory)).
						Require(
							constraint.Not(constraint.IsMatch(volume.GitRepo.Directory, `^/|^\.\.|(^|/)\.\.(/|$)`)),
						),
					constraint.Must(constraint.IsMatch(volume.Projected.DefaultMode, `^0[0-7]*$`),
						constraint.IsMatch(
							volume.Projected.DefaultMode,
							`^0*[0-7]{0,3}$`,
						),
					),
					constraint.When(constraint.Present(volume.Projected)).
						Require(constraint.NotEmpty(volume.Projected.Sources)),
					constraint.ForEach(
						volume.Projected.Sources,
						func(projectionGroup ProjectionGroup) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.ForEach(
									projectionGroup.Secret,
									func(secretProjection SecretProjection) []constraint.Constraint {
										return []constraint.Constraint{
											constraint.ForEach(
												secretProjection.Items,
												func(keyPath KeyPath) []constraint.Constraint {
													return []constraint.Constraint{
														constraint.When(constraint.Present(keyPath.Path)).
															Require(
																constraint.Not(constraint.IsMatch(keyPath.Path, `^/|^\.\.|(^|/)\.\.(/|$)`)),
															),
														constraint.Must(constraint.IsMatch(keyPath.Mode, `^0[0-7]*$`),
															constraint.IsMatch(
																keyPath.Mode,
																`^0*[0-7]{0,3}$`,
															),
														),
													}
												},
											),
										}
									},
								),
								constraint.ForEach(
									projectionGroup.ConfigMap,
									func(configMapProjection ConfigMapProjection) []constraint.Constraint {
										return []constraint.Constraint{
											constraint.ForEach(
												configMapProjection.Items,
												func(keyPath KeyPath) []constraint.Constraint {
													return []constraint.Constraint{
														constraint.When(constraint.Present(keyPath.Path)).
															Require(
																constraint.Not(constraint.IsMatch(keyPath.Path, `^/|^\.\.|(^|/)\.\.(/|$)`)),
															),
														constraint.Must(constraint.IsMatch(keyPath.Mode, `^0[0-7]*$`),
															constraint.IsMatch(
																keyPath.Mode,
																`^0*[0-7]{0,3}$`,
															),
														),
													}
												},
											),
										}
									},
								),
								constraint.ForEach(
									projectionGroup.DownwardAPI.Items,
									func(projectedDownwardItem ProjectedDownwardItem) []constraint.Constraint {
										return []constraint.Constraint{
											constraint.When(constraint.Present(projectedDownwardItem.Path)).
												Require(
													constraint.Not(
														constraint.IsMatch(
															projectedDownwardItem.Path,
															`^/|^\.\.|(^|/)\.\.(/|$)`,
														),
													),
												),
											constraint.Must(constraint.IsMatch(projectedDownwardItem.Mode, `^0[0-7]*$`),
												constraint.IsMatch(
													projectedDownwardItem.Mode,
													`^0*[0-7]{0,3}$`,
												),
											),
										}
									},
								),
								constraint.Must(
									constraint.AtLeast(
										projectionGroup.ServiceAccountToken.ExpirationSeconds,
										600,
									),
								),
							}
						},
					),
					constraint.ForEach(
						volume.Ephemeral.VolumeClaimTemplate.Metadata.Labels,
						func(entry constraint.MapEntry[string]) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.Must(
									constraint.IsMatch(
										entry.Key,
										`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-`+
											`z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`,
									),
									constraint.IsMatch(
										entry.Key,
										`^([^/]{1,253}/)?[^/]{1,63}$`,
									),
								),
								constraint.Must(
									constraint.IsMatch(
										entry.Value,
										`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`,
									),
									constraint.MaxItems(entry.Value, 63),
								),
							}
						},
					),
					constraint.ForEach(
						volume.Ephemeral.VolumeClaimTemplate.Metadata.Annotations,
						func(entry constraint.MapEntry[string]) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.Must(
									constraint.IsMatch(
										entry.Key,
										`^([A-Za-zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?(\.[A-Za-z`+
											`Kİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?)*/)?[A-Za-zKİ0-9](`+
											`[-A-Za-zKİ0-9_.]*[A-Za-zKİ0-9])?$`,
									),
									constraint.IsMatch(
										entry.Key,
										`^([^/]{1,253}/)?[^/]{1,63}$`,
									),
								),
							}
						},
					),
					constraint.ForEach(
						volume.Ephemeral.VolumeClaimTemplate.Spec.AccessModes,
						func(value string) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.Must(
									constraint.OneOf(
										value,
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
							constraint.OneOf(volume.Ephemeral.VolumeClaimTemplate.Spec.VolumeMode, `Block`,
								`Filesystem`),
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
			},
		),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.UpdateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.DeleteTimeout, constraint.FormatPositiveDuration)),
		constraint.ForEach(
			r.IgnoreLabels,
			func(pattern string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
				}
			},
		),
		constraint.ForEach(
			r.IgnoreAnnotations,
			func(pattern string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
				}
			},
		),
	}
}
