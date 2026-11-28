package v1

import (
	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
)

// LeaseResource manages a namespaced Lease object. It does not elect a leader,
// renew a lease, or determine expiration. Each configured spec field and metadata
// key is managed independently; other live values are preserved.
type LeaseResource struct {
	// Metadata selects explicit or generated identity and independently managed metadata keys.
	Metadata LeaseMetadata
	// Spec has no defaults. Explicit renewal times are managed values and can cause updates.
	Spec *LeaseSpec
	// CreateTimeout bounds requests and refresh; defaults to 5m.
	CreateTimeout *string
	// UpdateTimeout bounds requests, retries and refresh; defaults to 5m.
	UpdateTimeout *string
	// DeleteTimeout bounds deletion and disappearance waiting; defaults to 5m.
	DeleteTimeout *string
}

type LeaseMetadata struct {
	// Name or GenerateName is required, exclusively. Changes replace the object.
	Name *string
	// GenerateName is a DNS subdomain prefix. The generated name is saved separately.
	GenerateName *string
	// Namespace defaults to default. Changes replace the object.
	Namespace *string
	// Labels manages only configured keys. Omission clears previously managed keys.
	Labels *map[string]string
	// Annotations preserves values verbatim and limits aggregate key/value bytes to 262144.
	Annotations *map[string]string
}

// LeaseSpec preserves omission separately from empty holder identity and zero transitions.
type LeaseSpec struct {
	// HolderIdentity permits an explicit empty value. It does not acquire leadership.
	HolderIdentity *string
	// LeaseDurationSeconds is between 1 and 2147483647; it does not start a renewal loop.
	LeaseDurationSeconds *int64
	// LeaseTransitions is between 0 and 2147483647 and is never incremented automatically.
	LeaseTransitions *int64
	// AcquireTime and RenewTime accept RFC3339 and normalize to UTC microseconds.
	AcquireTime *string
	RenewTime   *string
}

// LeaseResourceOutput contains stable identity and observed managed values only.
type LeaseResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Metadata   LeaseMetadataOutput
	Spec       LeaseSpec
}

type LeaseMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	UID          string
	Labels       map[string]string
	Annotations  map[string]string
}

func (r LeaseResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.Metadata.Namespace, "default"),
		defaults.NullableValue(r.CreateTimeout, "5m"),
		defaults.NullableValue(r.UpdateTimeout, "5m"),
		defaults.NullableValue(r.DeleteTimeout, "5m"),
	}
}

func (r LeaseResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(constraint.IsMatch(r.Metadata.Name,
			`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253)).Message("invalid metadata.name"),
		constraint.Must(constraint.Any(
			constraint.All(constraint.IsMatch(r.Metadata.GenerateName,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
				constraint.MaxItems(r.Metadata.GenerateName, 253)),
			constraint.All(constraint.IsMatch(r.Metadata.GenerateName,
				`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*[-.]?)?[\x00-\x7f]-$`),
				constraint.MaxItems(r.Metadata.GenerateName, 254)),
		)).Message("invalid metadata.generate-name"),
		constraint.Must(constraint.IsMatch(r.Metadata.Namespace,
			`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`), constraint.MaxItems(r.Metadata.Namespace, 63)).
			Message("invalid metadata.namespace"),
		constraint.ForEach(r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
							`[A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.labels key"),
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?)?$`)).
						Message("invalid metadata.labels value"),
				}
			}),
		constraint.ForEach(r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?`+
							`(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?)*/)?`+
							`[A-Za-z0-9İK]([-_.A-Za-z0-9İK]{0,61}[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.annotations key"),
				}
			}),
		constraint.Must(constraint.Expr(
			"@core.sum([for k, v in input.annotations ?? {}: "+
				"@core.byte-length(k) + @core.byte-length(v)]) <= 262144",
			constraint.Bind("annotations", r.Metadata.Annotations),
		)).Message("metadata.annotations exceeds 262144 bytes"),
		constraint.Must(constraint.AtLeast(r.Spec.LeaseDurationSeconds, 1),
			constraint.AtMost(r.Spec.LeaseDurationSeconds, 2147483647)).
			Message("spec.lease-duration-seconds must be between 1 and 2147483647"),
		constraint.Must(constraint.AtLeast(r.Spec.LeaseTransitions, 0),
			constraint.AtMost(r.Spec.LeaseTransitions, 2147483647)).
			Message("spec.lease-transitions must be between 0 and 2147483647"),
		constraint.Must(constraint.IsFormat(r.Spec.AcquireTime, constraint.FormatDateTime)).
			Message("spec.acquire-time must be RFC3339"),
		constraint.Must(constraint.IsFormat(r.Spec.RenewTime, constraint.FormatDateTime)).
			Message("spec.renew-time must be RFC3339"),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)).
			Message("create-timeout must be a positive duration"),
		constraint.Must(constraint.IsFormat(r.UpdateTimeout, constraint.FormatPositiveDuration)).
			Message("update-timeout must be a positive duration"),
		constraint.Must(constraint.IsFormat(r.DeleteTimeout, constraint.FormatPositiveDuration)).
			Message("delete-timeout must be a positive duration"),
	}
}
