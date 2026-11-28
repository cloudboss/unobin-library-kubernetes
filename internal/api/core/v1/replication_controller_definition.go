package v1

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ReplicationControllerResource) ResourceDefinition() runtime.ResourceDefinition[
	ReplicationControllerResource, *ReplicationControllerResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *ReplicationControllerResource) *ServiceMetadata {
		return &r.Metadata
	})
	spec := runtime.InputField(func(r *ReplicationControllerResource) *ReplicationControllerSpec {
		return &r.Spec
	})
	return runtime.ResourceDefinition[
		ReplicationControllerResource, *ReplicationControllerResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(
			_ context.Context, input ReplicationControllerResource, _ *config.Configuration,
		) error {
			_, err := input.prepare()
			return err
		},
		StableID: func(
			_ ReplicationControllerResource, out *ReplicationControllerResourceOutput,
		) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing ReplicationController server or UID")
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[ReplicationControllerResource]{
			runtime.EqualBy(metadata, func(before, after ServiceMetadata) bool {
				return serviceIdentity(before) == serviceIdentity(after) &&
					reflect.DeepEqual(serviceMap(before.Labels), serviceMap(after.Labels)) &&
					reflect.DeepEqual(serviceMap(before.Annotations), serviceMap(after.Annotations))
			}),
			runtime.EqualBy(spec, func(before, after ReplicationControllerSpec) bool {
				return !replicationControllerSpecChanged(before, after, false)
			}),
			runtime.EqualBy(runtime.InputField(func(r *ReplicationControllerResource) **string {
				return &r.CreateTimeout
			}), func(before, after *string) bool {
				return equalReplicationControllerDurations(before, after)
			}),
			runtime.EqualBy(runtime.InputField(func(r *ReplicationControllerResource) **string {
				return &r.UpdateTimeout
			}), equalReplicationControllerDurations),
			runtime.EqualBy(runtime.InputField(func(r *ReplicationControllerResource) **string {
				return &r.DeleteTimeout
			}), func(before, after *string) bool {
				return equalReplicationControllerDurations(before, after)
			}),
			runtime.EqualBy(runtime.InputField(func(r *ReplicationControllerResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *ReplicationControllerResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		Replace: runtime.Replacement[
			ReplicationControllerResource, *ReplicationControllerResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ReplicationControllerResource]{
				runtime.ReplaceWhen(metadata, func(before, after ServiceMetadata) bool {
					return serviceIdentity(before) != serviceIdentity(after)
				}),
				runtime.ReplaceWhen(spec, func(before, after ReplicationControllerSpec) bool {
					return replicationControllerSpecChanged(before, after, true)
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func equalReplicationControllerDurations(before, after *string) bool {
	left, leftErr := replicationControllerDuration(before, "")
	right, rightErr := replicationControllerDuration(after, "")
	if leftErr != nil || rightErr != nil {
		return reflect.DeepEqual(before, after)
	}
	return left == right
}

// ReplicationControllerRegistration uses saved and observed values to classify immutable changes.
func ReplicationControllerRegistration[
	In ReplicationControllerResource, Out *ReplicationControllerResourceOutput, P any,
](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) replicationControllerRegistration[P] {
	return replicationControllerRegistration[P]{base, read, known}
}

type replicationControllerRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r replicationControllerRegistration[P]) ReplacementReasons(
	receiver, cfg any, prior P,
) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, prior, nil, false)
}

func (r replicationControllerRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, rawPrior P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, rawPrior, pending, configPending)
	if err != nil {
		return nil, err
	}
	var prior replicationControllerPrior
	capture := &ReplicationControllerResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, rawPrior); err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(reasons, func(reason string) bool {
		return reason == "metadata" || reason == "spec"
	})
	metadata, spec := replicationControllerImmutableChanges(
		receiver.(*ReplicationControllerResource), prior, configPending,
	)
	if _, unknown := pending["metadata"]; !unknown && metadata {
		reasons = append(reasons, "metadata")
	}
	if _, unknown := pending["spec"]; !unknown && spec {
		reasons = append(reasons, "spec")
	}
	slices.Sort(reasons)
	return reasons, nil
}

func (r replicationControllerRegistration[P]) PendingReplacementReasons(
	unresolved map[string][]string, configPending bool,
) []string {
	reasons := r.ResourceRegistration.PendingReplacementReasons(unresolved, configPending)
	// The deferred read resolves computed values and detects immutable spec drift.
	if configPending && !slices.Contains(reasons, "spec") {
		reasons = append(reasons, "spec")
		slices.Sort(reasons)
	}
	return reasons
}

func replicationControllerImmutableChanges(
	desired *ReplicationControllerResource, prior replicationControllerPrior, beforeRead bool,
) (metadata, spec bool) {
	metadata = serviceIdentity(desired.Metadata) != serviceIdentity(prior.Inputs.Metadata)
	input, previous := reflect.ValueOf(desired.Spec), reflect.ValueOf(prior.Inputs.Spec)
	var fallback reflect.Value
	switch {
	case prior.Observed != nil:
		fallback = reflect.ValueOf(prior.Observed.Spec)
	case prior.Outputs != nil:
		fallback = reflect.ValueOf(prior.Outputs.Spec)
	}
	model := reflect.TypeFor[ReplicationControllerSpec]()
	spec = replicationControllerCompare{
		input: input, fallback: fallback, replacementOnly: true, deferComputed: beforeRead,
	}.changed(model, previous, input)
	if !spec && prior.Outputs != nil && prior.Observed != nil {
		spec = replicationControllerCompare{
			input: input, priorInput: previous, replacementOnly: true, drift: true,
		}.changed(model, reflect.ValueOf(prior.Outputs.Spec), reflect.ValueOf(prior.Observed.Spec))
	}
	return metadata, spec
}

func replicationControllerSpecChanged(
	before, after ReplicationControllerSpec, replacementOnly bool,
) bool {
	return replicationControllerCompare{
		input: reflect.ValueOf(after), replacementOnly: replacementOnly,
	}.changed(
		reflect.TypeFor[ReplicationControllerSpec](), reflect.ValueOf(before), reflect.ValueOf(after),
	)
}

type replicationControllerCompare struct {
	input, priorInput, fallback reflect.Value
	replacementOnly             bool
	drift                       bool
	deferComputed               bool
}

func (c replicationControllerCompare) changed(
	model reflect.Type, before, after reflect.Value,
) bool {
	if model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	before, after = podIndirect(before), podIndirect(after)
	if !before.IsValid() && !after.IsValid() {
		return false
	}
	switch model.Kind() {
	case reflect.Struct:
		if !c.replacementOnly && before.IsValid() != after.IsValid() {
			return true
		}
		for field := range model.Fields() {
			if c.fieldChanged(field, podField(before, field.Name), podField(after, field.Name)) {
				return true
			}
		}
	case reflect.Slice:
		left, right := podListLen(before), podListLen(after)
		if !c.replacementOnly && left != right {
			return true
		}
		for i := range max(left, right) {
			child := c
			child.input, child.fallback = podIndex(c.input, i), podIndex(c.fallback, i)
			child.priorInput = podIndex(c.priorInput, i)
			if child.changed(model.Elem(), podIndex(before, i), podIndex(after, i)) {
				return true
			}
		}
	}
	return false
}

func (c replicationControllerCompare) fieldChanged(
	field reflect.StructField, before, after reflect.Value,
) bool {
	configured := podField(c.input, field.Name)
	if c.replacementOnly && !before.IsValid() && !podIndirect(after).IsValid() {
		return false
	}
	fallback := podField(c.fallback, field.Name)
	prior := podField(c.priorInput, field.Name)
	if field.Tag.Get("computed") == "true" {
		if !podConfigured(field, configured) {
			return false
		}
		if c.drift && !podConfigured(field, prior) {
			return false
		}
		if !c.drift && !podConfigured(field, before) {
			// An Update can only adopt a computed value after confirming it with a fresh read.
			if c.deferComputed {
				return false
			}
			before = fallback
		}
	}
	switch field.Name {
	case "LivenessProbe", "ReadinessProbe", "StartupProbe", "Resources", "SecurityContext",
		"PostStart", "PreStop", "VolumeMount", "VolumeDevice", "Searches":
		// Template descendants replace as complete blocks for this controller.
		if field.Tag.Get("replace") == "count" || field.Name == "Searches" {
			c.replacementOnly = false
		}
	}
	switch field.Tag.Get("replace") {
	case "count":
		if podListLen(before) != podListLen(after) {
			return true
		}
	case "true":
		c.replacementOnly = false
	}
	if field.Tag.Get("json") == "-" && (c.drift || c.replacementOnly) &&
		(field.Name != "SecretNamespace" || c.drift) {
		return false
	}
	model := field.Type
	if model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	child := c
	child.input, child.priorInput, child.fallback = configured, prior, fallback
	if model.Kind() == reflect.Struct ||
		model.Kind() == reflect.Slice && model.Elem().Kind() == reflect.Struct {
		return child.changed(field.Type, before, after)
	}
	if c.replacementOnly {
		return false
	}
	return !replicationControllerFieldsEqual(field, before, after)
}

func replicationControllerFieldsEqual(field reflect.StructField, before, after reflect.Value) bool {
	if field.Tag.Get("computed") == "true" && (!before.IsValid() || !after.IsValid()) {
		return !before.IsValid() && !after.IsValid()
	}
	if field.Tag.Get("format") == "quantity" && field.Tag.Get("omit") == "true" {
		left, right := podIndirect(before), podIndirect(after)
		// RC preparation omits empty quantities before parsing them.
		if (!left.IsValid() || left.IsZero()) && (!right.IsValid() || right.IsZero()) {
			return true
		}
	}
	// Both comparators use the same quantity, mode, signed integer and set conversion.
	return podFieldsEqual(field, before, after)
}
