package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strconv"
)

func nativeMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	err = decoder.Decode(&data)
	return data, err
}

func readValue(v reflect.Value, data any) error {
	if v.Kind() == reflect.Pointer {
		v.Set(reflect.New(v.Type().Elem()))
		return readValue(v.Elem(), data)
	}
	switch v.Kind() {
	case reflect.Struct:
		values, _ := data.(map[string]any)
		if v.Type() == reflect.TypeFor[ProjectedVolume]() {
			copy := make(map[string]any, len(values))
			maps.Copy(copy, values)
			groups := []any{}
			if sources, ok := values["sources"].([]any); ok {
				for _, item := range sources {
					source := item.(map[string]any)
					group := map[string]any{}
					for key, value := range source {
						if key == "secret" || key == "configMap" {
							value = []any{value}
						}
						group[key] = value
					}
					groups = append(groups, group)
				}
			}
			copy["sources"] = groups
			values = copy
		}
		for i := range v.NumField() {
			field := v.Type().Field(i)
			name := jsonName(field)
			if name == "-" || field.Tag.Get("read") == "omit" {
				continue
			}
			item, present := values[name]
			if !present {
				if field.Type.Kind() != reflect.Pointer ||
					field.Type.Elem().Kind() == reflect.Struct ||
					field.Type.Elem().Kind() == reflect.Map ||
					field.Type.Elem().Kind() == reflect.Slice ||
					field.Tag.Get("computed") == "true" ||
					field.Tag.Get("omit") == "true" ||
					field.Tag.Get("format") == "int64" ||
					field.Tag.Get("format") == "mode" {
					continue
				}
				item = reflect.Zero(field.Type.Elem()).Interface()
			}
			switch field.Tag.Get("format") {
			case "int32", "int64", "ttl":
				item = fmt.Sprint(item)
			case "mode":
				number, err := strconv.ParseInt(fmt.Sprint(item), 10, 64)
				if err != nil {
					return err
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
			if err := readValue(v.Field(i), item); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			}
		}
	case reflect.Slice:
		items, _ := data.([]any)
		v.Set(reflect.MakeSlice(v.Type(), len(items), len(items)))
		for i, item := range items {
			if err := readValue(v.Index(i), item); err != nil {
				return err
			}
		}
	case reflect.Map:
		items, _ := data.(map[string]any)
		if len(items) == 0 {
			return nil
		}
		v.Set(reflect.MakeMap(v.Type()))
		for key, item := range items {
			v.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(item.(string)))
		}
	case reflect.String:
		v.SetString(data.(string))
	case reflect.Bool:
		v.SetBool(data.(bool))
	case reflect.Int64:
		switch number := data.(type) {
		case int64:
			v.SetInt(number)
		case json.Number:
			integer, err := number.Int64()
			if err != nil {
				return err
			}
			v.SetInt(integer)
		}
	}
	return nil
}
