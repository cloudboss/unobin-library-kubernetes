package v1

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func statefulSetResourceRegistration() runtime.ResourceRegistration {
	base := runtime.MakeResource[
		StatefulSetResource, *StatefulSetResourceOutput, *config.Configuration,
	]((&StatefulSetResource{}).ResourceDefinition())
	return StatefulSetRegistration[StatefulSetResource, *StatefulSetResourceOutput](
		base, base.Read, base.KnownReplacementReasons)
}

// StatefulSetRegistration keeps the input and output models explicit for schema discovery.
func StatefulSetRegistration[In StatefulSetResource, Out *StatefulSetResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) statefulSetRegistration[P] {
	return makeStatefulSetRegistration(base, read, known)
}

type statefulSetRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func makeStatefulSetRegistration[P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) statefulSetRegistration[P] {
	return statefulSetRegistration[P]{base, read, known}
}

func (r statefulSetRegistration[P]) ReplacementReasons(
	receiver, cfg any, p P,
) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r statefulSetRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	var prior statefulSetPrior
	capture := &StatefulSetResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	desired := receiver.(*StatefulSetResource)
	reasons = slices.DeleteFunc(reasons, func(reason string) bool {
		return reason == "metadata" || reason == "spec"
	})
	metadata, spec := desired.statefulSetImmutableChanges(prior)
	if _, unknown := pending["metadata"]; !unknown && metadata {
		reasons = append(reasons, "metadata")
	}
	if _, unknown := pending["spec"]; !unknown && spec {
		reasons = append(reasons, "spec")
	}
	slices.Sort(reasons)
	return slices.Compact(reasons), nil
}
