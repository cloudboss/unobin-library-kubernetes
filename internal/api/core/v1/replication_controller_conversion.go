package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *ReplicationControllerResource) prepare() (*corev1.ReplicationController, error) {
	spec, err := replicationControllerRequestValue(reflect.ValueOf(r.Spec), "spec")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	identity := serviceIdentity(r.Metadata)
	result := &corev1.ReplicationController{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ReplicationController"},
		ObjectMeta: metav1.ObjectMeta{Name: identity[0], GenerateName: identity[1],
			Namespace: identity[2],
			Labels:    serviceMap(r.Metadata.Labels), Annotations: serviceMap(r.Metadata.Annotations)},
	}
	if err := json.Unmarshal(body, &result.Spec); err != nil {
		return nil, fmt.Errorf("convert ReplicationController spec: %w", err)
	}
	if result.Spec.Template.Spec.TerminationGracePeriodSeconds == nil {
		result.Spec.Template.Spec.TerminationGracePeriodSeconds = new(int64(0))
	}
	return result, nil
}

func replicationControllerRequestValue(value reflect.Value, path string) (any, error) {
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
			item, err := replicationControllerRequestField(value.Field(i), field, path+"."+name)
			if err != nil {
				return nil, err
			}
			if item != nil {
				result[name] = item
			}
		}
		switch value.Type() {
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
			item, err := replicationControllerRequestValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i))
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

func replicationControllerRequestField(value reflect.Value, field reflect.StructField,
	path string) (any, error) {
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
	if field.Tag.Get("omit") == "true" &&
		value.IsZero() {
		return nil, nil
	}
	item, err := replicationControllerRequestValue(value, path)
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

func (r *ReplicationControllerResource) output(
	live *corev1.ReplicationController, server string, tracked ServiceMetadataOutput,
) (*ReplicationControllerResourceOutput, error) {
	out := &ReplicationControllerResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ReplicationController", Namespace: live.Namespace,
		Name: live.Name, UID: string(live.UID),
	}
	var err error
	out.Metadata, err = r.projectMetadata(live.ObjectMeta, tracked)
	if err != nil {
		return nil, err
	}
	if live.Spec.Template == nil {
		return nil, fmt.Errorf("ReplicationController has no template")
	}
	spec := live.Spec.DeepCopy()
	pod := &spec.Template.Spec
	account := pod.ServiceAccountName
	if account == "" {
		account = "default"
	}
	token := regexp.MustCompile(regexp.QuoteMeta(account) + "-token-([a-z0-9]{5})")
	podRemoveTokens(pod, token)
	if dns := pod.DNSConfig; dns != nil && len(dns.Nameservers) == 0 &&
		len(dns.Searches) == 0 && len(dns.Options) == 0 {
		pod.DNSConfig = nil
	}
	podReadValue(reflect.ValueOf(&out.Spec).Elem(), reflect.ValueOf(*spec))
	out.Spec.Template.Metadata, err = r.projectMetadata(spec.Template.ObjectMeta, tracked)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ReplicationControllerResource) projectMetadata(
	metadata metav1.ObjectMeta, tracked ServiceMetadataOutput,
) (ServiceMetadataOutput, error) {
	labels, err := serviceMetadataProject(metadata.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return ServiceMetadataOutput{}, err
	}
	annotations, err := serviceMetadataProject(metadata.Annotations, tracked.Annotations,
		r.IgnoreAnnotations)
	if err != nil {
		return ServiceMetadataOutput{}, err
	}
	return ServiceMetadataOutput{Name: metadata.Name, GenerateName: metadata.GenerateName,
		Namespace: metadata.Namespace, Labels: labels, Annotations: annotations}, nil
}

func replicationControllerComputed(desired, live reflect.Value) {
	desired, live = podIndirect(desired), podIndirect(live)
	if !desired.IsValid() || !live.IsValid() {
		return
	}
	switch desired.Kind() {
	case reflect.Struct:
		for field := range desired.Type().Fields() {
			target, observed := desired.FieldByName(field.Name), live.FieldByName(field.Name)
			if !observed.IsValid() {
				continue
			}
			if field.Tag.Get("computed") == "true" && !podConfigured(field, target) &&
				observed.Type().AssignableTo(target.Type()) {
				target.Set(observed)
				continue
			}
			replicationControllerComputed(target, observed)
		}
	case reflect.Slice:
		for i := range min(desired.Len(), live.Len()) {
			replicationControllerComputed(desired.Index(i), live.Index(i))
		}
	}
}
