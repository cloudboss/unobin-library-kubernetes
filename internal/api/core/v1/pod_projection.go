package v1

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (r *PodResource) output(
	pod *corev1.Pod, server string, tracked ServiceMetadataOutput,
) (*PodResourceOutput, error) {
	annotations, err := serviceMetadataProject(pod.Annotations, tracked.Annotations,
		r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := serviceMetadataProject(pod.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	out := &PodResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Pod",
		Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID),
		Metadata: ServiceMetadataOutput{
			Namespace: pod.Namespace, Name: pod.Name, GenerateName: pod.GenerateName,
			Annotations: annotations, Labels: labels,
		},
	}
	normalized := podFilteredSpec(&pod.Spec)
	podReadValue(reflect.ValueOf(&out.Spec).Elem(), reflect.ValueOf(*normalized))
	return out, nil
}

func podSavedIgnorePatterns(expressions *[]string) *[]string {
	if expressions == nil {
		return nil
	}
	return new(slices.DeleteFunc(slices.Clone(*expressions), func(expression string) bool {
		_, err := regexp.Compile(expression)
		return err != nil
	}))
}

func podFilteredSpec(spec *corev1.PodSpec) *corev1.PodSpec {
	pod := spec.DeepCopy()
	account := pod.ServiceAccountName
	if account == "" {
		account = "default"
	}
	token := regexp.MustCompile(regexp.QuoteMeta(account) + "-token-([a-z0-9]{5})")
	isToken := func(name string) bool {
		return strings.HasPrefix(name, "kube-api-access") || token.MatchString(name)
	}
	for i, volume := range pod.Volumes {
		if isToken(volume.Name) {
			pod.Volumes = podWithout(pod.Volumes, i)
			break
		}
	}
	for _, containers := range [][]corev1.Container{pod.Containers, pod.InitContainers} {
		for i := range containers {
			for j, mount := range containers[i].VolumeMounts {
				if isToken(mount.Name) {
					containers[i].VolumeMounts = podWithout(containers[i].VolumeMounts, j)
					break
				}
			}
			for j := range containers[i].VolumeMounts {
				mount := &containers[i].VolumeMounts[j]
				if mount.MountPropagation == nil {
					mount.MountPropagation = new(corev1.MountPropagationNone)
				}
			}
		}
	}
	if pod.Tolerations != nil {
		pod.Tolerations = pod.Tolerations[:0]
		for _, toleration := range spec.Tolerations {
			if !podSystemToleration(toleration.Key) {
				pod.Tolerations = append(pod.Tolerations, toleration)
			}
		}
		if len(pod.Tolerations) == 0 && len(spec.Tolerations) > 0 {
			pod.Tolerations = nil
		}
	}
	if dns := pod.DNSConfig; dns != nil &&
		len(dns.Nameservers) == 0 && len(dns.Searches) == 0 && len(dns.Options) == 0 {
		pod.DNSConfig = nil
	}
	return pod
}

func podWithout[T any](items []T, index int) []T {
	if len(items) == 1 {
		return nil
	}
	return append(items[:index], items[index+1:]...)
}

func podSystemToleration(key string) bool {
	switch key {
	case "node.kubernetes.io/not-ready",
		"node.kubernetes.io/unreachable",
		"node.kubernetes.io/unschedulable",
		"node.kubernetes.io/memory-pressure",
		"node.kubernetes.io/disk-pressure",
		"node.kubernetes.io/network-unavailable",
		"node.kubernetes.io/pid-pressure":
		return true
	default:
		return false
	}
}

func podReadValue(target, source reflect.Value) {
	source = podIndirect(source)
	if !source.IsValid() {
		return
	}
	if target.Kind() == reflect.Pointer {
		target.Set(reflect.New(target.Type().Elem()))
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		if target.Type() == reflect.TypeFor[PodProjectionGroupOutput]() {
			podReadProjection(target, source.Interface().(corev1.VolumeProjection))
			return
		}
		fields := map[string]reflect.Value{}
		podAPIFields(source, fields)
		for i := range target.NumField() {
			field := target.Type().Field(i)
			name := podJSONName(field)
			item := podIndirect(fields[name])
			if !item.IsValid() || podOmitLiveValue(field, item) {
				continue
			}
			if field.Tag.Get("set") == "true" && item.Kind() == reflect.Slice {
				item = podReadSet(item)
			}
			podReadField(target.Field(i), item, field.Tag.Get("format"))
		}
	case reflect.Slice:
		if !source.IsNil() {
			target.Set(reflect.MakeSlice(target.Type(), source.Len(), source.Len()))
			for i := range source.Len() {
				podReadValue(target.Index(i), source.Index(i))
			}
		}
	case reflect.Map:
		if !source.IsNil() {
			target.Set(reflect.MakeMapWithSize(target.Type(), source.Len()))
			for entry := source.MapRange(); entry.Next(); {
				item := reflect.New(target.Type().Elem()).Elem()
				podReadValue(item, entry.Value())
				target.SetMapIndex(entry.Key().Convert(target.Type().Key()), item)
			}
		}
	case reflect.String:
		switch value := source.Interface().(type) {
		case resource.Quantity:
			target.SetString(value.String())
		case intstr.IntOrString:
			target.SetString(value.String())
		default:
			target.SetString(source.String())
		}
	case reflect.Bool:
		target.SetBool(source.Bool())
	case reflect.Int64:
		target.SetInt(source.Int())
	}
}

func podAPIFields(source reflect.Value, fields map[string]reflect.Value) {
	for i := range source.NumField() {
		field := source.Type().Field(i)
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.Anonymous && name == "" && strings.Contains(options, "inline") {
			value := podIndirect(source.Field(i))
			if value.IsValid() {
				podAPIFields(value, fields)
			}
		} else if name != "" && name != "-" {
			fields[name] = source.Field(i)
		}
	}
}

func podOmitLiveValue(field reflect.StructField, item reflect.Value) bool {
	if (item.Kind() == reflect.Map || item.Kind() == reflect.Slice) && item.IsNil() {
		return true
	}
	return (field.Tag.Get("omit") == "true" || field.Tag.Get("computed") == "true") &&
		field.Tag.Get("preserve-live-empty") != "true" && item.IsZero()
}

func podReadField(target, source reflect.Value, format string) {
	switch format {
	case "int64":
		source = reflect.ValueOf(fmt.Sprintf("%d", source.Int()))
	case "mode":
		source = reflect.ValueOf(fmt.Sprintf("0%o", source.Int()))
	}
	podReadValue(target, source)
}

func podReadSet(source reflect.Value) reflect.Value {
	items := make([]any, source.Len())
	for i := range source.Len() {
		items[i] = source.Index(i).Interface()
	}
	items = podSet(items)
	result := reflect.MakeSlice(source.Type(), len(items), len(items))
	for i, item := range items {
		result.Index(i).Set(reflect.ValueOf(item))
	}
	return result
}

func podReadProjection(target reflect.Value, source corev1.VolumeProjection) {
	group := target.Addr().Interface().(*PodProjectionGroupOutput)
	if source.Secret != nil {
		entries := make([]PodSecretProjection, 1)
		podReadValue(reflect.ValueOf(&entries[0]).Elem(), reflect.ValueOf(source.Secret))
		group.Secret = &entries
	}
	if source.ConfigMap != nil {
		entries := make([]PodConfigMapProjectionOutput, 1)
		podReadValue(reflect.ValueOf(&entries[0]).Elem(), reflect.ValueOf(source.ConfigMap))
		group.ConfigMap = &entries
	}
	podReadValue(target.FieldByName("DownwardAPI"), reflect.ValueOf(source.DownwardAPI))
	podReadValue(target.FieldByName("ServiceAccountToken"),
		reflect.ValueOf(source.ServiceAccountToken))
}
