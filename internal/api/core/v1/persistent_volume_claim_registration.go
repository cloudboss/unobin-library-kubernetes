package v1

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func pvcResourceRegistration() runtime.ResourceRegistration {
	base := runtime.MakeResource[
		PersistentVolumeClaimResource, *PersistentVolumeClaimResourceOutput, *config.Configuration,
	]((&PersistentVolumeClaimResource{}).ResourceDefinition())
	return PersistentVolumeClaimRegistration[PersistentVolumeClaimResource,
		*PersistentVolumeClaimResourceOutput](base, base.Read, base.KnownReplacementReasons)
}

// PersistentVolumeClaimRegistration preserves explicit input/output types for schema extraction.
func PersistentVolumeClaimRegistration[
	In PersistentVolumeClaimResource, Out *PersistentVolumeClaimResourceOutput, P any,
](base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) persistentVolumeClaimRegistration[P] {
	return persistentVolumeClaimRegistration[P]{base, read, known}
}

type persistentVolumeClaimRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r persistentVolumeClaimRegistration[P]) ReplacementReasons(receiver, cfg any, p P,
) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r persistentVolumeClaimRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	if _, unknown := pending["object"]; unknown {
		return reasons, nil
	}
	var prior persistentVolumeClaimPrior
	capture := &PersistentVolumeClaimResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	replace, err := pvcReplacement(receiver.(*PersistentVolumeClaimResource), prior)
	if err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(slices.Clone(reasons), func(reason string) bool {
		return reason == "object"
	})
	if replace {
		reasons = append(reasons, "object")
	}
	slices.Sort(reasons)
	return slices.Compact(reasons), nil
}
