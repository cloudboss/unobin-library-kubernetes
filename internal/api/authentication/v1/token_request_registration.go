package v1

import (
	"context"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
)

// TokenRequestRegistration keeps the input and output types available to the compiler.
func TokenRequestRegistration[In TokenRequestResource, Out *TokenRequestResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) tokenRequestRegistration[P] {
	return tokenRequestRegistration[P]{base, read, known}
}

type tokenRequestRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r tokenRequestRegistration[P]) ReplacementReasons(receiver, cfg any, p P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r tokenRequestRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	if _, unknown := pending["spec"]; unknown {
		return reasons, nil
	}
	desired := receiver.(*TokenRequestResource)
	if desired.Spec == nil {
		return reasons, nil
	}
	var prior tokenRequestPrior
	capture := &TokenRequestResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	if desired.replacesIssuedSpec(prior) {
		reasons = append(reasons, "spec")
		slices.Sort(reasons)
	}
	return reasons, nil
}
