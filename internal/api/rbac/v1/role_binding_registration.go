package v1

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func roleBindingResourceRegistration() runtime.ResourceRegistration {
	base := runtime.MakeResource[
		RoleBindingResource, *RoleBindingResourceOutput, *config.Configuration,
	]((&RoleBindingResource{}).ResourceDefinition())
	return RoleBindingRegistration[RoleBindingResource, *RoleBindingResourceOutput](
		base, base.Read, base.KnownReplacementReasons)
}

// RoleBindingRegistration keeps the input and output models explicit for schema discovery.
func RoleBindingRegistration[In RoleBindingResource, Out *RoleBindingResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) roleBindingRegistration[P] {
	return roleBindingRegistration[P]{base, read, known}
}

type roleBindingRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r roleBindingRegistration[P]) ReplacementReasons(
	receiver, cfg any, p P,
) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r roleBindingRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	var prior roleBindingPrior
	capture := &RoleBindingResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(slices.Clone(reasons), func(reason string) bool {
		return reason == "metadata"
	})
	if _, unknown := pending["metadata"]; !unknown {
		before := normalizeRoleMetadata(prior.Inputs.Metadata)
		if prior.Inputs.Metadata.Name == nil {
			if prior.Outputs != nil && prior.Outputs.Name != "" {
				before.Name = prior.Outputs.Name
			} else if prior.Observed != nil {
				before.Name = prior.Observed.Name
			}
		}
		desired := receiver.(*RoleBindingResource)
		after := normalizeRoleMetadata(desired.Metadata)
		if (desired.Metadata.Name != nil && before.Name != after.Name) ||
			before.Namespace != after.Namespace || before.GenerateName != after.GenerateName {
			reasons = append(reasons, "metadata")
		}
	}
	slices.Sort(reasons)
	return slices.Compact(reasons), nil
}
