package v1

import (
	"encoding/json"
	"fmt"
	"net"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

func (r *DeploymentResource) expand(observed *DeploymentSpec) (*appsv1.Deployment, error) {
	if err := r.validateControls(); err != nil {
		return nil, err
	}
	if err := validateMetadata(r.Metadata); err != nil {
		return nil, err
	}
	if err := validateMetadata(r.Spec.Template.Metadata); err != nil {
		return nil, err
	}
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
		switch value := v.Interface().(type) {
		case Metadata:
			if err := validateMetadata(value); err != nil {
				return nil, err
			}
		case ClaimMetadata:
			if err := validateMetadata(Metadata{
				Annotations: value.Annotations, Labels: value.Labels,
			}); err != nil {
				return nil, err
			}
		}
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
				// Ineffective source members still receive local validation.
				if _, err := expandValue(value, reflect.Value{}, location+"."+field.Name); err != nil {
					return nil, err
				}
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
			if field.Tag.Get("omit") == "true" && value.IsValid() && value.IsZero() {
				if err := validateField(field, value.Interface(), configured); err != nil {
					return nil, fmt.Errorf("%s.%s: %w", location, name, err)
				}
				continue
			}
			item, err := expandValue(value, old, location+"."+name)
			if err != nil {
				return nil, err
			}
			if err := validateField(field, item, configured); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", location, name, err)
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
		if !regexp.MustCompile(`^0[0-7]*$`).MatchString(text) {
			return nil, fmt.Errorf("mode must be leading-zero octal")
		}
		mode, err := strconv.ParseInt(text, 8, 32)
		if err != nil || mode > 0777 {
			return nil, fmt.Errorf("mode must be between 0 and 0777")
		}
		return mode, nil
	case "threshold":
		if !regexp.MustCompile(`^([0-9]+|[0-9]+%|)$`).MatchString(value.(string)) {
			return nil, fmt.Errorf("must be an integer or percentage")
		}
		parsed := intstr.Parse(value.(string))
		return parsed, nil
	case "port":
		if value != "" {
			if err := validatePort(value.(string)); err != nil {
				return nil, err
			}
		}
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

func validateField(field reflect.StructField, value any, configured bool) error {
	if choices := field.Tag.Get("enum"); choices != "" &&
		(configured || field.Tag.Get("default") != "") {
		values := []any{value}
		if list, ok := value.([]any); ok {
			values = list
		}
		for _, item := range values {
			if !slices.Contains(strings.Split(choices, ","), item.(string)) {
				return fmt.Errorf("invalid value %q", item)
			}
		}
	}
	if minimum := field.Tag.Get("min"); minimum != "" {
		minValue, _ := strconv.ParseInt(minimum, 10, 64)
		number, ok := value.(int64)
		if list, isList := value.([]any); isList {
			number, ok = int64(len(list)), true
		}
		if ok && number < minValue {
			return fmt.Errorf("must be at least %d", minValue)
		}
	}
	switch field.Tag.Get("format") {
	case "ip":
		if net.ParseIP(value.(string)) == nil {
			return fmt.Errorf("invalid IP address")
		}
	case "ips", "names":
		for _, item := range value.([]any) {
			if field.Tag.Get("format") == "ips" && net.ParseIP(item.(string)) == nil {
				return fmt.Errorf("invalid IP address")
			}
			if field.Tag.Get("format") == "names" &&
				len(validation.IsDNS1123Subdomain(item.(string))) > 0 {
				return fmt.Errorf("invalid DNS name")
			}
		}
	case "port-number":
		if field.Type.Kind() == reflect.Pointer && !configured {
			return nil
		}
		if number := value.(int64); number < 1 || number > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
	case "port-name":
		if configured {
			return validatePort(value.(string))
		}
	case "port":
		if configured {
			return validatePort(value.(string))
		}
	case "relative-path":
		text := value.(string)
		if path.IsAbs(text) || strings.HasPrefix(text, "..") ||
			slices.Contains(strings.Split(text, "/"), "..") {
			return fmt.Errorf("must be a relative path without parent components")
		}
	}
	return nil
}

func validatePort(text string) error {
	if number, err := strconv.ParseInt(text, 10, 64); err == nil {
		if number >= 1 && number <= 65535 {
			return nil
		}
	} else if len(validation.IsValidPortName(text)) == 0 {
		return nil
	}
	return fmt.Errorf("invalid port number or name %q", text)
}

func canonicalSet(items []any) []any {
	slices.SortFunc(items, func(a, b any) int {
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	})
	return slices.CompactFunc(items, reflect.DeepEqual)
}

func validateMetadata(m Metadata) error {
	if m.Name != nil && m.GenerateName != nil {
		return fmt.Errorf("name and generate-name are mutually exclusive")
	}
	if m.Name != nil && len(apivalidation.NameIsDNSSubdomain(*m.Name, false)) > 0 {
		return fmt.Errorf("invalid metadata name")
	}
	if m.GenerateName != nil && len(apivalidation.NameIsDNSLabel(*m.GenerateName, true)) > 0 {
		return fmt.Errorf("invalid metadata generate-name")
	}
	for _, entry := range []struct {
		values     *map[string]string
		annotation bool
	}{{m.Labels, false}, {m.Annotations, true}} {
		if entry.values == nil {
			continue
		}
		for key, value := range *entry.values {
			validationKey := key
			if entry.annotation {
				validationKey = strings.ToLower(key)
			}
			if len(validation.IsQualifiedName(validationKey)) > 0 {
				return fmt.Errorf("invalid metadata key %q", key)
			}
			if !entry.annotation && len(validation.IsValidLabelValue(value)) > 0 {
				return fmt.Errorf("invalid label value %q", value)
			}
		}
	}
	return nil
}
