package v1

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strconv"
)

func readDeploymentLookup(v reflect.Value, data any) error {
	if data == nil {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return readDeploymentLookup(v.Elem(), data)
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
			if name == "-" || !present || item == nil {
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
			if list, ok := item.([]any); ok && field.Tag.Get("set") == "true" {
				item = canonicalSet(list)
			}
			if err := readDeploymentLookup(v.Field(i), item); err != nil {
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
			if err := readDeploymentLookup(v.Index(i), item); err != nil {
				return err
			}
		}
	case reflect.Map:
		items, ok := data.(map[string]any)
		if !ok {
			return fmt.Errorf("expected map for %s", v.Type())
		}
		v.Set(reflect.MakeMapWithSize(v.Type(), len(items)))
		for key, item := range items {
			text, ok := item.(string)
			if !ok {
				return fmt.Errorf("expected string at %s", key)
			}
			v.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(text))
		}
	case reflect.String:
		text, ok := data.(string)
		if !ok {
			return fmt.Errorf("expected string for %s", v.Type())
		}
		v.SetString(text)
	case reflect.Bool:
		value, ok := data.(bool)
		if !ok {
			return fmt.Errorf("expected boolean for %s", v.Type())
		}
		v.SetBool(value)
	case reflect.Int64:
		value, err := strconv.ParseInt(fmt.Sprint(data), 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(value)
	}
	return nil
}
