package v2

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
)

// HorizontalPodAutoscalerRegistration compares computed names with typed prior
// outputs through the public registration methods.
func HorizontalPodAutoscalerRegistration[
	In HorizontalPodAutoscalerResource, Out *HorizontalPodAutoscalerResourceOutput, P any,
](base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) hpaRegistration[P] {
	return hpaRegistration[P]{base, read, known}
}

type hpaRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r hpaRegistration[P]) ReplacementReasons(receiver, cfg any, p P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r hpaRegistration[P]) KnownReplacementReasons(receiver, cfg any, p P,
	pending map[string][]string, configPending bool) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	var prior hpaPrior
	if _, err := r.read(context.Background(), &HorizontalPodAutoscalerResource{
		capturePrior: &prior}, cfg, p); err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(slices.Clone(reasons), func(s string) bool { return s == "object" })
	desired := receiver.(*HorizontalPodAutoscalerResource)
	if _, unknown := pending["object"]; !unknown && hpaIdentityChanged(desired.Object.Metadata,
		prior) {
		reasons = append(reasons, "object")
	}
	slices.Sort(reasons)
	return slices.Compact(reasons), nil
}

func hpaIdentityChanged(desired HPAMetadata, p hpaPrior) bool {
	before := hpaMetadata(p.Inputs.Object.Metadata)
	after := hpaMetadata(desired)
	if p.Inputs.Object.Metadata.Name == nil {
		if p.Observed != nil && p.Observed.Object.Metadata.Name != "" {
			before.Name = p.Observed.Object.Metadata.Name
		} else if p.Outputs != nil {
			before.Name = p.Outputs.Object.Metadata.Name
		}
	}
	return (desired.Name != nil && before.Name != after.Name) ||
		before.Namespace != after.Namespace || before.GenerateName != after.GenerateName
}
