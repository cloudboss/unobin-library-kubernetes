package v1

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func clusterRoleResourceRegistration() runtime.ResourceRegistration {
	base := runtime.MakeResource[
		ClusterRoleResource, *ClusterRoleResourceOutput, *config.Configuration,
	]((&ClusterRoleResource{}).ResourceDefinition())
	return ClusterRoleRegistration[ClusterRoleResource, *ClusterRoleResourceOutput](
		base, base.Read, base.KnownReplacementReasons)
}

// ClusterRoleRegistration keeps the input and output models explicit for schema discovery.
func ClusterRoleRegistration[In ClusterRoleResource, Out *ClusterRoleResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) clusterRoleRegistration[P] {
	return clusterRoleRegistration[P]{base, read, known}
}

type clusterRoleRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r clusterRoleRegistration[P]) ReplacementReasons(
	receiver, cfg any, p P,
) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r clusterRoleRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	var prior clusterRolePrior
	capture := &ClusterRoleResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(slices.Clone(reasons), func(reason string) bool {
		return reason == "metadata"
	})
	if _, unknown := pending["metadata"]; !unknown {
		before := normalizeClusterRoleMetadata(prior.Inputs.Metadata)
		if prior.Inputs.Metadata.Name == nil {
			if prior.Outputs != nil && prior.Outputs.Name != "" {
				before.Name = prior.Outputs.Name
			} else if prior.Observed != nil {
				before.Name = prior.Observed.Name
			}
		}
		desired := receiver.(*ClusterRoleResource)
		after := normalizeClusterRoleMetadata(desired.Metadata)
		if (desired.Metadata.Name != nil && before.Name != after.Name) ||
			before.GenerateName != after.GenerateName {
			reasons = append(reasons, "metadata")
		}
	}
	slices.Sort(reasons)
	return slices.Compact(reasons), nil
}
