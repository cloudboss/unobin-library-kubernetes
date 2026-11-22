package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (r *DeploymentResource) expand(observed *DeploymentSpec) (*appsv1.Deployment, error) {
	metadata, err := expandValue(reflect.ValueOf(r.Metadata), reflect.Value{}, "metadata")
	if err != nil {
		return nil, err
	}
	m := metadata.(map[string]any)
	if r.Metadata.Namespace == nil {
		m["namespace"] = "default"
	}
	var priorValue reflect.Value
	if observed != nil {
		priorValue = reflect.ValueOf(*observed)
	}
	spec, err := expandValue(reflect.ValueOf(r.Spec), priorValue, "spec")
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": m, "spec": spec,
	})
	if err != nil {
		return nil, err
	}
	obj := &appsv1.Deployment{}
	if err := json.Unmarshal(encoded, obj); err != nil {
		return nil, fmt.Errorf("convert Deployment: %w", err)
	}
	if obj.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		obj.Spec.Strategy.RollingUpdate = nil
	}
	return obj, nil
}

func indirect(v reflect.Value) reflect.Value {
	for v.IsValid() && v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func expandValue(v, observed reflect.Value, location string) (any, error) {
	v, observed = indirect(v), indirect(observed)
	if !v.IsValid() {
		return nil, nil
	}
	switch v.Kind() {
	case reflect.Struct:
		out := map[string]any{}
		for i := range v.NumField() {
			field := v.Type().Field(i)
			name := field.Tag.Get("json")
			value := v.Field(i)
			configured := value.Kind() != reflect.Pointer || !value.IsNil()
			if !configured && field.Tag.Get("format") == "mode" &&
				field.Tag.Get("default") == "" {
				continue
			}
			var old reflect.Value
			if observed.IsValid() {
				old = observed.Field(i)
			}
			if name == "-" {
				continue
			}
			if value.Kind() == reflect.Pointer && value.IsNil() {
				if field.Tag.Get("computed") == "true" && indirect(old).IsValid() {
					value = old
				} else if value.Type().Elem().Kind() != reflect.Struct &&
					value.Type().Elem().Kind() != reflect.Slice &&
					value.Type().Elem().Kind() != reflect.Map &&
					field.Tag.Get("computed") != "true" && field.Tag.Get("omit") != "true" {
					value = reflect.New(value.Type().Elem())
					if def, ok := field.Tag.Lookup("default"); ok {
						if err := setScalar(value.Elem(), def); err != nil {
							return nil, err
						}
					}
				} else {
					continue
				}
			}
			value = indirect(value)
			if field.Tag.Get("omit") == "true" && field.Tag.Get("format") != "quantity" &&
				value.IsValid() && value.IsZero() {
				continue
			}
			item, err := expandValue(value, old, location+"."+name)
			if err != nil {
				return nil, err
			}
			item, err = formatValue(field.Tag.Get("format"), item)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", location, name, err)
			}
			if item == nil {
				continue
			}
			if field.Tag.Get("cast") == "int32" {
				item = int64(int32(item.(int64)))
			}
			if field.Tag.Get("set") == "true" {
				item = canonicalSet(item.([]any))
			}
			out[name] = item
		}
		switch v.Interface().(type) {
		case SeccompProfile:
			if out["type"] != "Localhost" {
				delete(out, "localhostProfile")
			}
		case ProjectedVolume:
			groups := out["sources"].([]any)
			sources := []any{}
			for _, item := range groups {
				group := item.(map[string]any)
				for _, key := range []string{"secret", "configMap"} {
					if list, ok := group[key].([]any); ok {
						for _, source := range list {
							sources = append(sources, map[string]any{key: source})
						}
					}
				}
				for _, key := range []string{"downwardAPI", "serviceAccountToken"} {
					if source, ok := group[key]; ok {
						sources = append(sources, map[string]any{key: source})
					}
				}
			}
			out["sources"] = sources
		}
		return out, nil
	case reflect.Slice:
		out := make([]any, v.Len())
		for i := range v.Len() {
			var old reflect.Value
			if observed.IsValid() && i < observed.Len() {
				old = observed.Index(i)
			}
			value, err := expandValue(v.Index(i), old, location+"["+strconv.Itoa(i)+"]")
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	case reflect.Map:
		out := map[string]any{}
		for _, key := range v.MapKeys() {
			out[key.String()] = v.MapIndex(key).Interface()
		}
		return out, nil
	default:
		return v.Interface(), nil
	}
}

func setScalar(v reflect.Value, text string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(text)
	case reflect.Bool:
		value, err := strconv.ParseBool(text)
		if err != nil {
			return err
		}
		v.SetBool(value)
	case reflect.Int64:
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(value)
	}
	return nil
}

func formatValue(format string, value any) (any, error) {
	switch format {
	case "int32-cast":
		return int64(int32(value.(int64))), nil
	case "int32", "int64":
		text := value.(string)
		if text == "" {
			return nil, nil
		}
		bits := 64
		if format == "int32" {
			bits = 32
		}
		return strconv.ParseInt(text, 10, bits)
	case "mode":
		text := value.(string)
		return strconv.ParseInt(text, 8, 32)
	case "threshold":
		parsed := intstr.Parse(value.(string))
		return parsed, nil
	case "port":
		return intstr.Parse(value.(string)), nil
	case "quantity":
		quantity, err := resource.ParseQuantity(value.(string))
		if err != nil {
			return nil, err
		}
		return quantity.String(), nil
	case "quantities":
		out := map[string]any{}
		for key, text := range value.(map[string]any) {
			quantity, err := resource.ParseQuantity(text.(string))
			if err != nil {
				return nil, err
			}
			out[key] = quantity.String()
		}
		return out, nil
	}
	return value, nil
}

func canonicalSet(items []any) []any {
	slices.SortFunc(items, func(a, b any) int {
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	})
	return slices.CompactFunc(items, reflect.DeepEqual)
}
