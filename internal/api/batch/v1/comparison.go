package v1

import (
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

func comparisonValue(value any, model reflect.Type, format string, computed bool) any {
	if value == nil {
		return nil
	}
	for model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	if !computed && (model.Kind() == reflect.Slice || model.Kind() == reflect.Map) &&
		reflect.ValueOf(value).Len() == 0 {
		return nil
	}
	if format == "quantity" {
		quantity, err := resource.ParseQuantity(value.(string))
		if err == nil {
			mantissa, exponent := quantity.AsCanonicalBytes(nil)
			return string(mantissa) + "e" + strconv.Itoa(int(exponent))
		}
	}
	if format == "quantities" {
		out := map[string]any{}
		for key, quantity := range value.(map[string]any) {
			out[key] = comparisonValue(quantity, reflect.TypeFor[string](), "quantity", false)
		}
		return out
	}
	switch model.Kind() {
	case reflect.Struct:
		values, ok := value.(map[string]any)
		if !ok {
			return value
		}
		out := maps.Clone(values)
		for field := range model.Fields() {
			name := jsonName(field)
			if item, present := values[name]; present {
				item = comparisonValue(item, field.Type, field.Tag.Get("format"),
					field.Tag.Get("computed") == "true")
				if item == nil {
					delete(out, name)
				} else {
					out[name] = item
				}
			}
		}
		return out
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return comparisonValue(value, model.Elem(), "", false)
		}
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = comparisonValue(item, model.Elem(), "", false)
		}
		return out
	}
	return value
}

func ineffectiveValues(v reflect.Value) any {
	v = indirect(v)
	if !v.IsValid() {
		return nil
	}
	out := map[string]any{}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if field.Tag.Get("json") == "-" {
				out[field.Name] = v.Field(i).Interface()
			} else if child := ineffectiveValues(v.Field(i)); child != nil {
				out[field.Name] = child
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			if child := ineffectiveValues(v.Index(i)); child != nil {
				out[fmt.Sprint(i)] = child
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func duration(value *string, field string) (time.Duration, error) {
	if value == nil {
		return time.Minute, nil
	}
	parsed, err := time.ParseDuration(*value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", field)
	}
	return parsed, nil
}
