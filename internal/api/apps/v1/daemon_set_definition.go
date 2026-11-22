package v1

import (
	"context"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *DaemonSetResource) ResourceDefinition() runtime.ResourceDefinition[
	DaemonSetResource, *DaemonSetResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[
		DaemonSetResource, *DaemonSetResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, input DaemonSetResource, _ *config.Configuration) error {
			_, err := input.expand(nil)
			return err
		},
		StableID: func(_ DaemonSetResource, output *DaemonSetResourceOutput) (string, error) {
			if output == nil || output.UID == "" {
				return "", fmt.Errorf("missing DaemonSet UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Equality: []runtime.InputEqualityRule[DaemonSetResource]{
			runtime.EqualBy(runtime.InputField(func(r *DaemonSetResource) *Metadata {
				return &r.Metadata
			}), func(a, b Metadata) bool {
				return outerIdentity(a) == outerIdentity(b) &&
					reflect.DeepEqual(metadataMap(a.Labels), metadataMap(b.Labels)) &&
					reflect.DeepEqual(metadataMap(a.Annotations), metadataMap(b.Annotations))
			}),
			runtime.EqualBy(runtime.InputField(func(r *DaemonSetResource) *DaemonSetSpec {
				return &r.Spec
			}), equivalentDaemonSetSpec),
		},
		Replace: runtime.Replacement[
			DaemonSetResource, *DaemonSetResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[DaemonSetResource]{
				runtime.ReplaceWhen(runtime.InputField(func(r *DaemonSetResource) *Metadata {
					return &r.Metadata
				}), func(a, b Metadata) bool { return outerIdentity(a) != outerIdentity(b) }),
				runtime.ReplaceWhen(runtime.InputField(func(r *DaemonSetResource) *DaemonSetSpec {
					return &r.Spec
				}), func(a, b DaemonSetSpec) bool {
					b = retainedDaemonSetSpec(b, a)
					return metadataIdentity(a.Template.Metadata) != metadataIdentity(b.Template.Metadata) ||
						!reflect.DeepEqual(replacementValues(reflect.ValueOf(a), ""),
							replacementValues(reflect.ValueOf(b), ""))
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
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

func equivalentDaemonSetSpec(a, b DaemonSetSpec) bool {
	b = retainedDaemonSetSpec(b, a)
	left, leftErr := expandValue(reflect.ValueOf(a), reflect.Value{}, "spec")
	right, rightErr := expandValue(reflect.ValueOf(b), reflect.Value{}, "spec")
	return leftErr == nil && rightErr == nil &&
		reflect.DeepEqual(comparisonValue(left, reflect.TypeFor[DaemonSetSpec](), "", false),
			comparisonValue(right, reflect.TypeFor[DaemonSetSpec](), "", false)) &&
		reflect.DeepEqual(ineffectiveValues(reflect.ValueOf(a)), ineffectiveValues(reflect.ValueOf(b)))
}

func equivalentObservedDaemonSetSpec(a, b DaemonSetSpec) bool {
	left, leftErr := nativeMap(a)
	right, rightErr := nativeMap(b)
	return leftErr == nil && rightErr == nil &&
		reflect.DeepEqual(comparisonValue(left, reflect.TypeFor[DaemonSetSpec](), "", false),
			comparisonValue(right, reflect.TypeFor[DaemonSetSpec](), "", false))
}

func retainedDaemonSetSpec(desired, old DaemonSetSpec) DaemonSetSpec {
	retained := retainDaemonSetComputed(reflect.ValueOf(desired), reflect.ValueOf(old))
	return retained.Interface().(DaemonSetSpec)
}

func retainDaemonSetComputed(desired, old reflect.Value) reflect.Value {
	if desired.Kind() == reflect.Pointer {
		if desired.IsNil() {
			return desired
		}
		out := reflect.New(desired.Type().Elem())
		prior := reflect.Zero(desired.Type().Elem())
		if old.IsValid() && !old.IsNil() {
			prior = old.Elem()
		}
		out.Elem().Set(retainDaemonSetComputed(desired.Elem(), prior))
		return out
	}
	switch desired.Kind() {
	case reflect.Struct:
		out := reflect.New(desired.Type()).Elem()
		for i := range desired.NumField() {
			field := desired.Field(i)
			if field.Kind() == reflect.Pointer && field.IsNil() &&
				desired.Type().Field(i).Tag.Get("computed") == "true" {
				out.Field(i).Set(old.Field(i))
			} else {
				out.Field(i).Set(retainDaemonSetComputed(field, old.Field(i)))
			}
		}
		return out
	case reflect.Slice:
		if desired.IsNil() {
			return desired
		}
		out := reflect.MakeSlice(desired.Type(), desired.Len(), desired.Len())
		for i := range desired.Len() {
			prior := reflect.Zero(desired.Type().Elem())
			if i < old.Len() {
				prior = old.Index(i)
			}
			out.Index(i).Set(retainDaemonSetComputed(desired.Index(i), prior))
		}
		return out
	}
	return desired
}
