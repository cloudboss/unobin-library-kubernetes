package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func (r *DeploymentResource) output(obj *appsv1.Deployment, server string) (
	*DeploymentResourceOutput, error,
) {
	if obj.UID == "" {
		return nil, fmt.Errorf("deployment has no Kubernetes UID")
	}
	obj = obj.DeepCopy()
	filterMetadata(obj.Labels, r.Metadata.Labels, r.IgnoreLabels)
	filterMetadata(obj.Annotations, r.Metadata.Annotations, r.IgnoreAnnotations)
	data, err := nativeMap(obj.ObjectMeta)
	if err != nil {
		return nil, err
	}
	out := &DeploymentResourceOutput{
		Server: server, APIVersion: "apps/v1", Kind: "Deployment",
		Name: obj.Name, Namespace: obj.Namespace, UID: string(obj.UID),
	}
	if err := readValue(reflect.ValueOf(&out.Metadata).Elem(), data); err != nil {
		return nil, err
	}
	out.Spec, err = normalizedSpec(obj.Spec)
	return out, err
}

func normalizedSpec(spec appsv1.DeploymentSpec) (DeploymentSpec, error) {
	spec = *spec.DeepCopy()
	pod := &spec.Template.Spec
	account := pod.ServiceAccountName
	if account == "" {
		account = "default"
	}
	token := regexp.MustCompile(account + "-token-([a-z0-9]{5})")
	isToken := func(name string) bool {
		return strings.HasPrefix(name, "kube-api-access") || token.MatchString(name)
	}
	for i, volume := range pod.Volumes {
		if isToken(volume.Name) {
			pod.Volumes = append(pod.Volumes[:i], pod.Volumes[i+1:]...)
			break
		}
	}
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for i := range containers {
			for j, mount := range containers[i].VolumeMounts {
				if isToken(mount.Name) {
					containers[i].VolumeMounts = append(
						containers[i].VolumeMounts[:j], containers[i].VolumeMounts[j+1:]...)
					break
				}
			}
		}
	}
	data, err := nativeMap(spec)
	if err != nil {
		return DeploymentSpec{}, err
	}
	var result DeploymentSpec
	err = readValue(reflect.ValueOf(&result).Elem(), data)
	return result, err
}

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
			name := field.Tag.Get("json")
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
			case "int32", "int64":
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

func filterMetadata(values map[string]string, configured *map[string]string, ignore *[]string) {
	for key := range values {
		if configured != nil {
			if _, present := (*configured)[key]; present {
				continue
			}
		}
		host, _, qualified := strings.Cut(key, "/")
		internal := qualified && strings.HasSuffix(host, "kubernetes.io") &&
			host != "app.kubernetes.io" && host != "service.beta.kubernetes.io"
		internal = internal || strings.Contains(key, "deprecated.daemonset.template.generation")
		if ignore != nil {
			for _, pattern := range *ignore {
				match, err := regexp.MatchString(pattern, key)
				internal = internal || err == nil && match
			}
		}
		if internal {
			delete(values, key)
		}
	}
}
