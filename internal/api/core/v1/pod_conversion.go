package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (r *PodResource) prepare() (*corev1.Pod, error) {
	spec, err := podRequestValue(reflect.ValueOf(r.Spec), "spec")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	identity := serviceIdentity(r.Metadata)
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: identity[0], GenerateName: identity[1], Namespace: identity[2],
			Annotations: serviceMap(r.Metadata.Annotations), Labels: serviceMap(r.Metadata.Labels),
		},
	}
	if err := json.Unmarshal(body, &pod.Spec); err != nil {
		return nil, fmt.Errorf("convert Pod spec: %w", err)
	}
	return pod, nil
}

func podRequestValue(value reflect.Value, path string) (any, error) {
	value = podIndirect(value)
	if !value.IsValid() {
		return nil, nil
	}
	switch value.Kind() {
	case reflect.Struct:
		result := map[string]any{}
		for i := range value.NumField() {
			field := value.Type().Field(i)
			name := podJSONName(field)
			if name == "-" {
				continue
			}
			item, err := podRequestField(value.Field(i), field, path+"."+name)
			if err != nil {
				return nil, err
			}
			if item != nil {
				result[name] = item
			}
		}
		switch value.Type() {
		case reflect.TypeFor[PodSecurityContext]():
			if _, ok := result["windowsOptions"]; !ok {
				result["windowsOptions"] = map[string]any{}
			}
		case reflect.TypeFor[PodSeccompProfile]():
			if result["type"] != "Localhost" {
				delete(result, "localhostProfile")
			}
		case reflect.TypeFor[PodProjectedVolume]():
			result["sources"] = podProjections(result["sources"].([]any))
		}
		return result, nil
	case reflect.Slice:
		result := make([]any, value.Len())
		for i := range value.Len() {
			item, err := podRequestValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			result[i] = item
		}
		return result, nil
	default:
		return value.Interface(), nil
	}
}

func podRequestField(value reflect.Value, field reflect.StructField, path string) (any, error) {
	value = podIndirect(value)
	if !value.IsValid() {
		model := field.Type.Elem()
		switch model.Kind() {
		case reflect.Struct, reflect.Slice, reflect.Map:
			return nil, nil
		}
		if field.Tag.Get("computed") == "true" || field.Tag.Get("omit") == "true" {
			return nil, nil
		}
		if field.Tag.Get("format") == "mode" && field.Tag.Get("default") == "" {
			return nil, nil
		}
		value = reflect.New(model).Elem()
		if text, ok := field.Tag.Lookup("default"); ok {
			podSetDefault(value, text)
		}
	}
	if field.Tag.Get("omit") == "true" && field.Tag.Get("format") != "quantity" &&
		value.IsZero() {
		return nil, nil
	}
	item, err := podRequestValue(value, path)
	if err != nil {
		return nil, err
	}
	item, err = podFormatValue(field.Tag.Get("format"), item)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if field.Tag.Get("cast") == "int32" {
		item = int64(int32(item.(int64)))
	}
	if field.Tag.Get("set") == "true" {
		item = podSet(item.([]any))
	}
	return item, nil
}

func podJSONName(field reflect.StructField) string {
	if name, found := field.Tag.Lookup("json"); found {
		return name
	}
	return strings.ToLower(field.Name[:1]) + field.Name[1:]
}

func podIndirect(value reflect.Value) reflect.Value {
	for value.IsValid() && value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}

func podSetDefault(value reflect.Value, text string) {
	switch value.Kind() {
	case reflect.String:
		value.SetString(text)
	case reflect.Bool:
		value.SetBool(text == "true")
	case reflect.Int64:
		number, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			panic(err)
		}
		value.SetInt(number)
	}
}

func podFormatValue(format string, value any) (any, error) {
	switch format {
	case "int32-cast":
		return int64(int32(value.(int64))), nil
	case "int64":
		text := value.(string)
		if text == "" {
			return nil, nil
		}
		return strconv.ParseInt(text, 10, 64)
	case "mode":
		return strconv.ParseInt(value.(string), 8, 32)
	case "port":
		return intstr.Parse(value.(string)), nil
	case "quantity":
		return resource.ParseQuantity(value.(string))
	case "quantities":
		input := value.(map[string]string)
		out := make(map[string]resource.Quantity, len(input))
		for key, text := range input {
			quantity, err := resource.ParseQuantity(text)
			if err != nil {
				return nil, fmt.Errorf("quantity %q: %w", key, err)
			}
			out[key] = quantity
		}
		return out, nil
	}
	return value, nil
}

func podSet(items []any) []any {
	slices.SortFunc(items, func(a, b any) int {
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	})
	return slices.CompactFunc(items, reflect.DeepEqual)
}

func podProjections(groups []any) []any {
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
	return sources
}
