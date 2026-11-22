package v1

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
)

type cronJobRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

// MakeCronJobRegistration adds effective immutable comparisons to the typed CronJob registration.
// The first two type arguments preserve the input/output types for schema extraction.
func MakeCronJobRegistration[In CronJobResource, Out *CronJobResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) cronJobRegistration[P] {
	return cronJobRegistration[P]{ResourceRegistration: base, read: read, known: known}
}

func (r cronJobRegistration[P]) ReplacementReasons(receiver, cfg any, p P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r cronJobRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	var captured cronJobPrior
	capture := &CronJobResource{capture: &captured}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(reasons)+2)
	for _, reason := range reasons {
		if reason != "metadata" && reason != "spec" {
			result = append(result, reason)
		}
	}
	metadata, spec := receiver.(*CronJobResource).immutableChanges(captured)
	if _, unknown := pending["metadata"]; !unknown && metadata {
		result = append(result, "metadata")
	}
	if _, unknown := pending["spec"]; !unknown && spec {
		result = append(result, "spec")
	}
	slices.Sort(result)
	result = slices.Compact(result)
	if _, unknown := pending["spec"]; !unknown && len(result) != 0 {
		if _, err := receiver.(*CronJobResource).prepare(nil); err != nil {
			return nil, err
		}
	}
	return result, nil
}
