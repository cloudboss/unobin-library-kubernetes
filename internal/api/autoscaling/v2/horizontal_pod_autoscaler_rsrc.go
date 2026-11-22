package v2

import (
	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

// HorizontalPodAutoscalerResource manages an autoscaling/v2 HPA. Create and update
// refresh the accepted object; deletion returns on API acceptance. No operation
// waits for metrics, scaling, readiness, or final deletion.
type HorizontalPodAutoscalerResource struct {
	capturePrior *hpaPrior `ub:"-"`
	// Object contains metadata and the HPA spec. Namespace defaults to default and
	// min-replicas defaults to 1. Identity changes require replacement. Omitted
	// metric, behavior, and direction lists retain observed values on update;
	// explicit empty lists remain configured.
	// Exactly one nonempty name or generate-name is required. Omitted or empty
	// metadata maps clear tracked keys.
	// The legacy target-cpu-utilization-percentage input has no API effect.
	Object HPAObject
	// IgnoreAnnotations filters untracked annotation keys on refresh. Tracked keys
	// take precedence over both these expressions and internal-key filtering.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked label keys on refresh. Tracked keys take precedence.
	IgnoreLabels *[]string
}

type HPAObject struct {
	Metadata HPAMetadata
	Spec     HPASpec
}
type HPAMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}
type HPASpec struct {
	MaxReplicas                    int
	MinReplicas                    *int
	ScaleTargetRef                 HPATargetRef
	Metric                         *[]HPAMetric
	Behavior                       *HPABehavior
	TargetCPUUtilizationPercentage *int
}
type HPATargetRef struct {
	APIVersion *string
	Kind       string
	Name       string
}
type HPADescribedObject struct {
	APIVersion string
	Kind       string
	Name       string
}
type HPAMetric struct {
	Type              string
	ContainerResource *HPAContainerResource
	External          *HPAExternal
	Object            *HPAObjectMetric
	Pods              *HPAExternal
	Resource          *HPAResource
}
type HPAResource struct {
	Name   string
	Target *HPAMetricTarget
}
type HPAContainerResource struct {
	Name      string
	Container string
	Target    *HPAMetricTarget
}
type HPAExternal struct {
	Metric HPAMetricIdentifier
	Target *HPAMetricTarget
}
type HPAObjectMetric struct {
	Metric          HPAMetricIdentifier
	DescribedObject HPADescribedObject
	Target          *HPAMetricTarget
}
type HPAMetricIdentifier struct {
	Name     string
	Selector *[]HPASelector
}
type HPASelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]HPASelectorExpression
}
type HPASelectorExpression struct {
	Key      *string
	Operator *string
	Values   *[]string
}
type HPAMetricTarget struct {
	Type               string
	AverageUtilization *int
	AverageValue       *string
	Value              *string
}
type HPABehavior struct {
	ScaleUp   *[]HPARule
	ScaleDown *[]HPARule
}
type HPARule struct {
	SelectPolicy               *string
	StabilizationWindowSeconds *int
	Policy                     []HPAPolicy
}
type HPAPolicy struct {
	Type          string
	PeriodSeconds int
	Value         int
}

// HorizontalPodAutoscalerResourceOutput contains stable identity and live spec
// values. Status and volatile server metadata do not participate in drift.
type HorizontalPodAutoscalerResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     HPAObjectOutput
}
type HPAObjectOutput struct {
	Metadata HPAMetadataOutput
	Spec     HPASpecOutput
}
type HPAMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	UID          string
	Annotations  map[string]string
	Labels       map[string]string
}
type hpaPrior = runtime.Prior[
	HorizontalPodAutoscalerResource, *HorizontalPodAutoscalerResourceOutput, *config.Configuration,
]

type HPASpecOutput struct {
	MaxReplicas    int
	MinReplicas    *int
	ScaleTargetRef HPATargetRef
	Metric         *[]HPAMetric
	Behavior       *HPABehavior
}

func (r HorizontalPodAutoscalerResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Object.Metadata.Name, r.Object.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Object.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Object.Metadata.Name, 253),
		).Message("metadata.name must be a DNS subdomain of at most 253 bytes"),
		// Generated-prefix validation masks the final two bytes before checking DNS label syntax.
		constraint.Must(constraint.IsMatch(r.Object.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
				`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`)).Message(
			"metadata.generate-name must be a DNS label prefix"),
		constraint.ForEach(
			r.Object.Metadata.Labels,
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
			r.Object.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Object.Metadata.Annotations,
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
		constraint.ForEach(r.Object.Spec.Metric, func(m HPAMetric) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.OneOf(m.Type, "ContainerResource", "External", "Object", "Pods",
					"Resource")).Message(
					"metric type must be ContainerResource, External, Object, Pods, or Resource"),
			}
		}),
		constraint.ForEach(r.Object.Spec.Behavior.ScaleUp, func(rule HPARule) []constraint.Constraint {
			return []constraint.Constraint{constraint.Must(constraint.NotEmpty(rule.Policy)).Message(
				"policy must not be empty")}
		}),
		constraint.ForEach(r.Object.Spec.Behavior.ScaleDown, func(rule HPARule) []constraint.Constraint {
			return []constraint.Constraint{constraint.Must(constraint.NotEmpty(rule.Policy)).Message(
				"policy must not be empty")}
		}),
	}
}
