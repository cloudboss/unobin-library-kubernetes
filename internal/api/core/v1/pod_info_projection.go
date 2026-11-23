package v1

import (
	"fmt"
	"reflect"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func projectPodLookup(spec *corev1.PodSpec) (*PodLookupSpec, error) {
	pod := spec.DeepCopy()
	token, err := regexp.Compile(podLookupTokenAccount(pod) + "-token-([a-z0-9]{5})")
	if err != nil {
		return nil, fmt.Errorf("service account token pattern: %w", err)
	}
	podRemoveTokens(pod, token)
	for _, volume := range pod.Volumes {
		if err := podLookupVolumePointers(volume); err != nil {
			return nil, fmt.Errorf("volume %q: %w", volume.Name, err)
		}
	}
	podLookupOptionalBlocks(pod)
	out := &PodLookupSpec{}
	podLookupValue(reflect.ValueOf(out).Elem(), reflect.ValueOf(pod))
	return out, nil
}

func podLookupTokenAccount(pod *corev1.PodSpec) string {
	if pod.ServiceAccountName == "" {
		return "default"
	}
	return pod.ServiceAccountName
}

func podLookupVolumePointers(volume corev1.Volume) error {
	switch {
	case volume.FC != nil && volume.FC.Lun == nil:
		return fmt.Errorf("fc.lun is absent")
	case volume.AzureDisk != nil && volume.AzureDisk.CachingMode == nil:
		return fmt.Errorf("azure-disk.caching-mode is absent")
	case volume.Ephemeral != nil && volume.Ephemeral.VolumeClaimTemplate == nil:
		return fmt.Errorf("ephemeral.volume-claim-template is absent")
	default:
		return nil
	}
}

func podLookupOptionalBlocks(pod *corev1.PodSpec) {
	if affinity := pod.Affinity; affinity != nil && *affinity == (corev1.Affinity{}) {
		pod.Affinity = nil
	} else if affinity != nil {
		if n := affinity.NodeAffinity; n != nil {
			required := n.RequiredDuringSchedulingIgnoredDuringExecution
			switch {
			case required == nil && n.PreferredDuringSchedulingIgnoredDuringExecution == nil:
				affinity.NodeAffinity = nil
			case required != nil && len(required.NodeSelectorTerms) == 0:
				n.RequiredDuringSchedulingIgnoredDuringExecution = nil
			}
		}
		if a := affinity.PodAffinity; a != nil &&
			len(a.RequiredDuringSchedulingIgnoredDuringExecution) == 0 &&
			len(a.PreferredDuringSchedulingIgnoredDuringExecution) == 0 {
			affinity.PodAffinity = nil
		}
		if a := affinity.PodAntiAffinity; a != nil &&
			len(a.RequiredDuringSchedulingIgnoredDuringExecution) == 0 &&
			len(a.PreferredDuringSchedulingIgnoredDuringExecution) == 0 {
			affinity.PodAntiAffinity = nil
		}
	}
	if dns := pod.DNSConfig; dns != nil &&
		len(dns.Nameservers) == 0 && len(dns.Searches) == 0 && len(dns.Options) == 0 {
		pod.DNSConfig = nil
	}
	if c := pod.SecurityContext; c != nil {
		if c.FSGroup == nil && c.RunAsGroup == nil && c.RunAsUser == nil && c.RunAsNonRoot == nil &&
			c.FSGroupChangePolicy == nil && len(c.SupplementalGroups) == 0 &&
			c.SELinuxOptions == nil && c.SeccompProfile == nil && c.Sysctls == nil &&
			c.WindowsOptions == nil {
			pod.SecurityContext = nil
		}
	}
}

func podLookupValue(target, source reflect.Value) {
	source = podIndirect(source)
	if target.Kind() == reflect.Pointer {
		if !source.IsValid() || !podLookupBlockPresent(source) {
			return
		}
		target.Set(reflect.New(target.Type().Elem()))
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		if source.IsValid() {
			podLookupStruct(target, source)
		}
	case reflect.Slice:
		size := 0
		if source.IsValid() {
			size = source.Len()
		}
		target.Set(reflect.MakeSlice(target.Type(), size, size))
		for index := range size {
			podLookupValue(target.Index(index), source.Index(index))
		}
	case reflect.Map:
		target.Set(reflect.MakeMap(target.Type()))
		if source.IsValid() {
			for entry := source.MapRange(); entry.Next(); {
				item := reflect.New(target.Type().Elem()).Elem()
				podReadValue(item, entry.Value())
				target.SetMapIndex(entry.Key().Convert(target.Type().Key()), item)
			}
		}
	default:
		podReadValue(target, source)
	}
}

func podLookupStruct(target, source reflect.Value) {
	fields := map[string]reflect.Value{}
	podAPIFields(source, fields)
	if seccomp, ok := source.Interface().(corev1.SeccompProfile); ok &&
		seccomp.Type != corev1.SeccompProfileTypeLocalhost {
		delete(fields, "localhostProfile")
	}
	for index := range target.NumField() {
		field := target.Type().Field(index)
		item := podIndirect(fields[podJSONName(field)])
		if item.IsValid() && field.Tag.Get("set") == "true" {
			item = podReadSet(item)
		}
		if format := field.Tag.Get("format"); item.IsValid() && format != "" {
			podReadField(target.Field(index), item, format)
			continue
		}
		podLookupValue(target.Field(index), item)
	}
}

func podLookupBlockPresent(source reflect.Value) bool {
	switch value := source.Interface().(type) {
	case metav1.ObjectMeta:
		return len(value.Labels) > 0 || len(value.Annotations) > 0
	default:
		return true
	}
}
