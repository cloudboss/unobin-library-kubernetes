package object

import (
	"context"
	"maps"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"
)

// ObjectRegistration compares opaque manifests without SDK field selectors.
func ObjectRegistration[In ObjectResource, Out *ObjectResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) objectRegistration[P] {
	return objectRegistration[P]{base, read, known}
}

type objectRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r objectRegistration[P]) InputsEqual(receiver any, inputs map[string]any) (bool, error) {
	return r.KnownInputsEqual(receiver, inputs, nil)
}

func (r objectRegistration[P]) KnownInputsEqual(
	receiver any, inputs map[string]any, pending map[string][]string,
) (bool, error) {
	if _, unresolved := pending["manifest"]; !unresolved {
		current := receiver.(*ObjectResource)
		left, leftErr := structuredManifest(current.Manifest)
		right, rightErr := structuredManifest(inputs["manifest"])
		if leftErr != nil || rightErr != nil || !reflect.DeepEqual(left.Object, right.Object) {
			return false, nil
		}
		inputs = maps.Clone(inputs)
		inputs["manifest"] = current.Manifest
	}
	return r.ResourceRegistration.KnownInputsEqual(receiver, inputs, pending)
}

func (r objectRegistration[P]) ReplacementReasons(receiver, cfg any, p P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r objectRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil {
		return nil, err
	}
	if _, unresolved := pending["manifest"]; unresolved {
		return reasons, nil
	}
	var saved prior
	capture := &ObjectResource{capturePrior: &saved}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}
	current := receiver.(*ObjectResource)
	if manifestIdentity(saved.Inputs.Manifest) != manifestIdentity(current.Manifest) {
		reasons = append(reasons, "manifest")
	}
	return reasons, nil
}

func (r objectRegistration[P]) PendingReplacementReasons(
	pending map[string][]string, configPending bool,
) []string {
	reasons := r.ResourceRegistration.PendingReplacementReasons(pending, configPending)
	if _, unresolved := pending["manifest"]; unresolved {
		reasons = append(reasons, "manifest")
	}
	return reasons
}
