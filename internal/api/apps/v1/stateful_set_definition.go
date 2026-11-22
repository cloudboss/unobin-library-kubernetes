package v1

import (
	"context"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *StatefulSetResource) ResourceDefinition() runtime.ResourceDefinition[
	StatefulSetResource, *StatefulSetResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		StatefulSetResource, *StatefulSetResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(
			_ context.Context, input StatefulSetResource, _ *config.Configuration,
		) error {
			_, err := input.statefulSetSpecMap(nil)
			return err
		},
		StableID: func(_ StatefulSetResource, out *StatefulSetResourceOutput) (string, error) {
			if out == nil || out.UID == "" {
				return "", fmt.Errorf("missing StatefulSet UID")
			}
			return out.Server + "/" + out.UID, nil
		},
		Equality: []runtime.InputEqualityRule[StatefulSetResource]{
			runtime.EqualBy(runtime.InputField(func(r *StatefulSetResource) *Metadata {
				return &r.Metadata
			}), func(a, b Metadata) bool {
				a.Name, b.Name = nil, nil
				return !statefulSetMetadataChanged(a, b) &&
					reflect.DeepEqual(metadataMap(a.Labels), metadataMap(b.Labels)) &&
					reflect.DeepEqual(metadataMap(a.Annotations), metadataMap(b.Annotations))
			}),
			runtime.EqualBy(runtime.InputField(func(r *StatefulSetResource) *StatefulSetSpec {
				return &r.Spec
			}), func(a, b StatefulSetSpec) bool {
				return equivalentStatefulSetSpec(statefulSetMutableSpec(a), statefulSetMutableSpec(b))
			}),
		},
		Replace: runtime.Replacement[
			StatefulSetResource, *StatefulSetResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[StatefulSetResource]{
				runtime.ReplaceWhen(runtime.InputField(func(r *StatefulSetResource) *Metadata {
					return &r.Metadata
				}), statefulSetMetadataChanged),
				runtime.ReplaceWhen(runtime.InputField(func(r *StatefulSetResource) *StatefulSetSpec {
					return &r.Spec
				}), statefulSetReplacement),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Namespace
				}),
			},
		},
	}
}

func statefulSetMetadataChanged(a, b Metadata) bool {
	if b.Name == nil {
		b.Name = a.Name
	}
	return outerIdentity(a) != outerIdentity(b)
}

func equivalentStatefulSetSpec(a, b StatefulSetSpec) bool {
	previous, err := (&StatefulSetResource{Spec: a}).statefulSetSpecMap(nil)
	if err != nil {
		return false
	}
	next, err := (&StatefulSetResource{Spec: b}).statefulSetSpecMap(&a)
	if err == nil && b.PersistentVolumeClaimRetentionPolicy == nil {
		if policy, present := previous["persistentVolumeClaimRetentionPolicy"]; present {
			next["persistentVolumeClaimRetentionPolicy"] = policy
		}
	}
	return err == nil &&
		statefulSetValuesEqual(previous, next, reflect.TypeFor[StatefulSetSpec]()) &&
		reflect.DeepEqual(
			ineffectiveValues(reflect.ValueOf(a)), ineffectiveValues(reflect.ValueOf(b)))
}

func statefulSetReplacement(a, b StatefulSetSpec) bool {
	effective := reflect.New(reflect.TypeFor[StatefulSetSpec]()).Elem()
	effective.Set(reflect.ValueOf(b))
	retainStatefulSetComputed(effective, reflect.ValueOf(a))
	after := effective.Interface().(StatefulSetSpec)
	left, err := statefulSetReplacementSpecMap(a)
	if err != nil {
		return true
	}
	right, err := statefulSetReplacementSpecMap(after)
	if err != nil {
		return true
	}
	for _, field := range []struct {
		name  string
		model reflect.Type
	}{
		{"podManagementPolicy", reflect.TypeFor[string]()},
		{"revisionHistoryLimit", reflect.TypeFor[int64]()},
		{"selector", reflect.TypeFor[Selector]()},
		{"serviceName", reflect.TypeFor[string]()},
		{"volumeClaimTemplates", reflect.TypeFor[[]StatefulSetClaimTemplate]()},
	} {
		if !statefulSetValuesEqual(left[field.name], right[field.name], field.model) {
			return true
		}
	}
	if metadataIdentity(a.Template.Metadata) != metadataIdentity(after.Template.Metadata) {
		return true
	}
	return !reflect.DeepEqual(
		replacementValues(reflect.ValueOf(a.Template), ""),
		replacementValues(reflect.ValueOf(after.Template), ""))
}

func retainStatefulSetComputed(current, old reflect.Value) {
	if current.Kind() == reflect.Pointer {
		if current.IsNil() {
			return
		}
		copy := reflect.New(current.Type().Elem())
		copy.Elem().Set(current.Elem())
		current.Set(copy)
		if old.IsNil() {
			return
		}
		retainStatefulSetComputed(current.Elem(), old.Elem())
		return
	}
	switch current.Kind() {
	case reflect.Struct:
		for i := range current.NumField() {
			field := current.Type().Field(i)
			value, prior := current.Field(i), old.Field(i)
			if field.Tag.Get("computed") == "true" &&
				value.Kind() == reflect.Pointer && value.IsNil() {
				value.Set(prior)
			} else {
				retainStatefulSetComputed(value, prior)
			}
		}
	case reflect.Slice:
		copy := reflect.MakeSlice(current.Type(), current.Len(), current.Len())
		reflect.Copy(copy, current)
		current.Set(copy)
		for i := range current.Len() {
			if i < old.Len() {
				retainStatefulSetComputed(current.Index(i), old.Index(i))
			}
		}
	}
}

func (r *StatefulSetResource) statefulSetImmutableChanges(p statefulSetPrior) (bool, bool) {
	previous := p.Inputs
	observed := p.Outputs
	if p.Observed != nil {
		observed = p.Observed
	}
	if observed != nil {
		if previous.Metadata.Name == nil {
			previous.Metadata.Name = &observed.Name
		}
		retainStatefulSetComputed(reflect.ValueOf(&previous.Spec).Elem(),
			reflect.ValueOf(observed.Spec))
	}
	return statefulSetMetadataChanged(previous.Metadata, r.Metadata),
		statefulSetReplacement(previous.Spec, r.Spec)
}

func statefulSetReplacementSpecMap(spec StatefulSetSpec) (map[string]any, error) {
	return (&StatefulSetResource{Spec: spec}).statefulSetSpecMap(nil)
}

func statefulSetMutableSpec(spec StatefulSetSpec) StatefulSetSpec {
	spec.PodManagementPolicy, spec.RevisionHistoryLimit = nil, nil
	spec.VolumeClaimTemplate = nil
	spec.Template.Metadata.Name = nil
	clearStatefulSetComputedImmutables(reflect.ValueOf(&spec).Elem())
	return spec
}

func clearStatefulSetComputedImmutables(value reflect.Value) {
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			copy := reflect.New(value.Type().Elem())
			copy.Elem().Set(value.Elem())
			value.Set(copy)
			clearStatefulSetComputedImmutables(value.Elem())
		}
	case reflect.Struct:
		for i := range value.NumField() {
			field := value.Type().Field(i)
			if field.Tag.Get("replace") == "true" && field.Tag.Get("computed") == "true" {
				value.Field(i).SetZero()
			} else {
				clearStatefulSetComputedImmutables(value.Field(i))
			}
		}
	case reflect.Slice:
		if !value.IsNil() {
			copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			reflect.Copy(copy, value)
			value.Set(copy)
			for i := range value.Len() {
				clearStatefulSetComputedImmutables(value.Index(i))
			}
		}
	}
}
