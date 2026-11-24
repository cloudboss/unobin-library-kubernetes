package object

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
)

// EnvRegistration uses the saved REST scope to classify namespace edits.
func EnvRegistration[In EnvResource, Out *EnvResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) envRegistration[P] {
	return envRegistration[P]{base, read, known}
}

type envRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r envRegistration[P]) ReplacementReasons(receiver, cfg any, prior P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, prior, nil, false)
}

func (r envRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, rawPrior P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, rawPrior, pending, configPending)
	if err != nil || !slices.Contains(reasons, "metadata.namespace") {
		return reasons, err
	}

	var prior envPrior
	capture := &EnvResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, rawPrior); err != nil {
		return nil, err
	}
	if prior.Outputs != nil && prior.Outputs.Namespace == "" {
		reasons = slices.DeleteFunc(reasons, func(reason string) bool {
			return reason == "metadata.namespace"
		})
	}
	return reasons, nil
}
