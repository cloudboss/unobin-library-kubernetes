package v1

import (
	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

// HorizontalPodAutoscalerResource manages an autoscaling/v1 HPA.
type HorizontalPodAutoscalerResource struct {
	capturePrior      *hpaPrior `ub:"-"`
	Object            HPAObject
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
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
	TargetCPUUtilizationPercentage *int
}

type HPATargetRef struct {
	APIVersion *string
	Kind       string
	Name       string
}

// HorizontalPodAutoscalerResourceOutput contains stable identity and observed spec.
type HorizontalPodAutoscalerResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     HPAObjectOutput
}

type HPAObjectOutput struct {
	Metadata HPAMetadataOutput
	Spec     HPASpec
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
	}
}
