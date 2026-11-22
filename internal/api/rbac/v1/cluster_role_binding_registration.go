package v1

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func clusterRoleBindingResourceRegistration() runtime.ResourceRegistration {
	base := runtime.MakeResource[
		ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput, *config.Configuration,
	]((&ClusterRoleBindingResource{}).ResourceDefinition())
	return ClusterRoleBindingRegistration[
		ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput,
	](
		base, base.Read, base.KnownReplacementReasons)
}

// ClusterRoleBindingRegistration keeps the input and output models explicit for schema discovery.
func ClusterRoleBindingRegistration[
	In ClusterRoleBindingResource, Out *ClusterRoleBindingResourceOutput, P any,
](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) clusterRoleBindingRegistration[P] {
	return clusterRoleBindingRegistration[P]{base, read, known}
}

type clusterRoleBindingRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r clusterRoleBindingRegistration[P]) ReplacementReasons(
	receiver, cfg any, p P,
) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r clusterRoleBindingRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	var prior clusterRoleBindingPrior
	capture := &ClusterRoleBindingResource{capturePrior: &prior}
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
		desired := receiver.(*ClusterRoleBindingResource)
		after := normalizeClusterRoleMetadata(desired.Metadata)
		if (desired.Metadata.Name != nil && before.Name != after.Name) ||
			before.GenerateName != after.GenerateName {
			reasons = append(reasons, "metadata")
		}
	}
	slices.Sort(reasons)
	return slices.Compact(reasons), nil
}
