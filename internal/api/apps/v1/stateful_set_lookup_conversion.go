package v1

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strconv"
)

func readStatefulSetLookup(v reflect.Value, data any) error {
	if data == nil {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return readStatefulSetLookup(v.Elem(), data)
	}
	switch v.Kind() {
	case reflect.Struct:
		values, ok := data.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object for %s", v.Type())
		}
		if v.Type() == reflect.TypeFor[ProjectedVolume]() {
			values = maps.Clone(values)
			groups := []any{}
			if sources, ok := values["sources"].([]any); ok {
				for _, item := range sources {
					source := maps.Clone(item.(map[string]any))
					for _, key := range []string{"secret", "configMap"} {
						if item, ok := source[key]; ok {
							source[key] = []any{item}
						}
					}
					groups = append(groups, source)
				}
			}
			values["sources"] = groups
		}
		for i := range v.NumField() {
			field := v.Type().Field(i)
			name := field.Tag.Get("json")
			item, present := values[name]
			if name == "-" {
				continue
			}
			if !present || item == nil {
				switch child := v.Field(i); child.Kind() {
				case reflect.Slice:
					child.Set(reflect.MakeSlice(child.Type(), 0, 0))
				case reflect.Map:
					child.Set(reflect.MakeMap(child.Type()))
				}
				continue
			}
			switch field.Tag.Get("format") {
			case "int32", "int64":
				item = fmt.Sprint(item)
			case "mode":
				number, err := strconv.ParseInt(fmt.Sprint(item), 10, 64)
				if err != nil {
					return fmt.Errorf("read %s: %w", name, err)
				}
				item = fmt.Sprintf("0%o", number)
			case "threshold", "port":
				if number, ok := item.(json.Number); ok {
					item = number.String()
				}
			}
			if err := readStatefulSetLookup(v.Field(i), item); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			}
		}
	case reflect.Slice:
		items, ok := data.([]any)
		if !ok {
			return fmt.Errorf("expected list for %s", v.Type())
		}
		v.Set(reflect.MakeSlice(v.Type(), len(items), len(items)))
		for i, item := range items {
			if err := readStatefulSetLookup(v.Index(i), item); err != nil {
				return err
			}
		}
	default:
		return readDeploymentLookup(v, data)
	}
	return nil
}
