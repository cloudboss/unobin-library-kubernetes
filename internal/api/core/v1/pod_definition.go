package v1

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *PodResource) ResourceDefinition() runtime.ResourceDefinition[
	PodResource, *PodResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *PodResource) *ServiceMetadata { return &r.Metadata })
	spec := runtime.InputField(func(r *PodResource) *PodSpec { return &r.Spec })
	return runtime.ResourceDefinition[
		PodResource, *PodResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input PodResource, _ *config.Configuration) error {
			_, err := input.prepare()
			return err
		},
		StableID: func(_ PodResource, out *PodResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.UID == "" {
				return "", fmt.Errorf("missing Pod server or UID")
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[PodResource]{
			runtime.EqualBy(metadata, func(before, after ServiceMetadata) bool {
				return serviceIdentity(before) == serviceIdentity(after) &&
					reflect.DeepEqual(serviceMap(before.Labels), serviceMap(after.Labels)) &&
					reflect.DeepEqual(serviceMap(before.Annotations), serviceMap(after.Annotations))
			}),
			runtime.EqualBy(spec, func(before, after PodSpec) bool {
				return !podSpecChanged(before, after, false)
			}),
			runtime.EqualBy(runtime.InputField(func(r *PodResource) **[]string {
				return &r.TargetState
			}), func(before, after *[]string) bool {
				defaultPhase := []string{"Running"}
				return slices.Equal(serviceSet(serviceDefault(before, defaultPhase)),
					serviceSet(serviceDefault(after, defaultPhase)))
			}),
			runtime.EqualBy(runtime.InputField(func(r *PodResource) **string {
				return &r.CreateTimeout
			}), func(before, after *string) bool {
				return equalPodDurations(before, after)
			}),
			runtime.EqualBy(runtime.InputField(func(r *PodResource) **string {
				return &r.DeleteTimeout
			}), func(before, after *string) bool {
				return equalPodDurations(before, after)
			}),
			runtime.EqualBy(runtime.InputField(func(r *PodResource) **[]string {
				return &r.IgnoreAnnotations
			}), equalConfigMapLists),
			runtime.EqualBy(runtime.InputField(func(r *PodResource) **[]string {
				return &r.IgnoreLabels
			}), equalConfigMapLists),
		},
		Replace: runtime.Replacement[
			PodResource, *PodResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[PodResource]{
				runtime.ReplaceWhen(metadata, func(before, after ServiceMetadata) bool {
					return serviceIdentity(before) != serviceIdentity(after)
				}),
				runtime.ReplaceWhen(spec, func(before, after PodSpec) bool {
					return podSpecChanged(before, after, true)
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

func equalPodDurations(before, after *string) bool {
	left, leftErr := podDuration(before, "")
	right, rightErr := podDuration(after, "")
	if leftErr != nil || rightErr != nil {
		return reflect.DeepEqual(before, after)
	}
	return left == right
}

// PodRegistration uses saved and observed values to classify immutable changes.
func PodRegistration[In PodResource, Out *PodResourceOutput, P any](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) podRegistration[P] {
	return podRegistration[P]{base, read, known}
}

type podRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func (r podRegistration[P]) ReplacementReasons(receiver, cfg any, prior P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, prior, nil, false)
}

func (r podRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, rawPrior P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, rawPrior, pending, configPending)
	if err != nil {
		return nil, err
	}
	var prior podPrior
	capture := &PodResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, rawPrior); err != nil {
		return nil, err
	}
	reasons = slices.DeleteFunc(reasons, func(reason string) bool {
		return reason == "metadata" || reason == "spec"
	})
	metadata, spec := podImmutableChanges(receiver.(*PodResource), prior, configPending)
	if _, unknown := pending["metadata"]; !unknown && metadata {
		reasons = append(reasons, "metadata")
	}
	if _, unknown := pending["spec"]; !unknown && spec {
		reasons = append(reasons, "spec")
	}
	slices.Sort(reasons)
	return reasons, nil
}

func (r podRegistration[P]) PendingReplacementReasons(
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

func podImmutableChanges(
	desired *PodResource, prior podPrior, beforeRead bool,
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
	model := reflect.TypeFor[PodSpec]()
	spec = podCompare{
		input: input, fallback: fallback, replacementOnly: true, deferComputed: beforeRead,
	}.changed(model, previous, input)
	if !spec && prior.Outputs != nil && prior.Observed != nil {
		spec = podCompare{
			input: input, priorInput: previous, replacementOnly: true, drift: true,
		}.changed(model, reflect.ValueOf(prior.Outputs.Spec), reflect.ValueOf(prior.Observed.Spec))
	}
	return metadata, spec
}

func podSpecChanged(before, after PodSpec, replacementOnly bool) bool {
	return podCompare{
		input: reflect.ValueOf(after), replacementOnly: replacementOnly,
	}.changed(reflect.TypeFor[PodSpec](), reflect.ValueOf(before), reflect.ValueOf(after))
}

type podCompare struct {
	input, priorInput, fallback reflect.Value
	replacementOnly             bool
	drift                       bool
	deferComputed               bool
}

func (c podCompare) changed(model reflect.Type, before, after reflect.Value) bool {
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

func (c podCompare) fieldChanged(field reflect.StructField, before, after reflect.Value) bool {
	configured := podField(c.input, field.Name)
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
	switch field.Tag.Get("replace") {
	case "count":
		if podListLen(before) != podListLen(after) {
			return true
		}
	case "true":
		c.replacementOnly = false
	}
	if field.Tag.Get("json") == "-" && (c.drift || c.replacementOnly) {
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
	return !podFieldsEqual(field, before, after)
}

func podConfigured(field reflect.StructField, value reflect.Value) bool {
	value = podIndirect(value)
	return value.IsValid() && (field.Tag.Get("omit") != "true" || !value.IsZero())
}

func podFieldsEqual(field reflect.StructField, before, after reflect.Value) bool {
	// An absent parent does not materialize the defaults of its children.
	convert := func(v reflect.Value) (any, error) {
		if !v.IsValid() {
			return nil, nil
		}
		return podRequestField(v, field, field.Name)
	}
	left, leftErr := convert(before)
	right, rightErr := convert(after)
	if leftErr != nil || rightErr != nil {
		return reflect.DeepEqual(podValue(before), podValue(after))
	}
	switch a := left.(type) {
	case resource.Quantity:
		b, ok := right.(resource.Quantity)
		return ok && a.Cmp(b) == 0
	case map[string]resource.Quantity:
		b, ok := right.(map[string]resource.Quantity)
		if !ok || len(a) != len(b) {
			return false
		}
		for key, quantity := range a {
			other, ok := b[key]
			if !ok || quantity.Cmp(other) != 0 {
				return false
			}
		}
		return true
	}
	if field.Tag.Get("computed") != "true" && podEmptyCollection(left) && podEmptyCollection(right) {
		return true
	}
	return reflect.DeepEqual(left, right)
}

func podValue(v reflect.Value) any {
	if v.IsValid() {
		return v.Interface()
	}
	return nil
}

func podEmptyCollection(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	return (v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && v.Len() == 0
}

func podField(value reflect.Value, field string) reflect.Value {
	value = podIndirect(value)
	if value.IsValid() {
		return value.FieldByName(field)
	}
	return reflect.Value{}
}

func podIndex(value reflect.Value, i int) reflect.Value {
	value = podIndirect(value)
	if value.IsValid() && value.Len() > i {
		return value.Index(i)
	}
	return reflect.Value{}
}

func podListLen(value reflect.Value) int {
	value = podIndirect(value)
	if !value.IsValid() {
		return 0
	}
	if value.Kind() == reflect.Slice {
		return value.Len()
	}
	return 1
}
