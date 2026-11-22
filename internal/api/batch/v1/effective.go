package v1

import "reflect"

var immutableSpecKeys = []string{
	"completions", "backoffLimitPerIndex", "completionMode", "podFailurePolicy",
	"selector", "template",
}

func mutableMetadataEqual(a, b Metadata) bool {
	a.Name, a.GenerateName, a.Namespace = nil, nil, nil
	b.Name, b.GenerateName, b.Namespace = nil, nil, nil
	return metadataEqual(a, b)
}

func mutableSpecEqual(a, b JobSpec) bool {
	left, err := comparedSpec(a, nil)
	if err != nil {
		return false
	}
	right, err := comparedSpec(b, &a)
	if err != nil {
		return false
	}
	for _, key := range immutableSpecKeys {
		delete(left.(map[string]any), key)
		delete(right.(map[string]any), key)
	}
	return reflect.DeepEqual(left, right)
}

func (r *JobResource) immutableChanges(p prior) (bool, bool) {
	baseline := p.Observed
	if baseline == nil {
		baseline = p.Outputs
	}
	metadata := p.Inputs.Metadata
	if baseline == nil {
		return !metadataIdentityEqual(metadata, r.Metadata), immutableChanged(p.Inputs.Spec, r.Spec)
	}
	metadata.Name = baseline.Metadata.Name
	if metadata.Name == nil {
		metadata.Name = &baseline.Name
	}
	effective := computedSnapshot(reflect.ValueOf(p.Inputs.Spec), reflect.ValueOf(baseline.Spec))
	left, err := comparedSpec(effective.Interface().(JobSpec), nil)
	if err != nil {
		return !metadataIdentityEqual(metadata, r.Metadata), true
	}
	right, err := comparedSpec(r.Spec, &baseline.Spec)
	if err != nil {
		return !metadataIdentityEqual(metadata, r.Metadata), true
	}
	changed := false
	for _, key := range immutableSpecKeys {
		if !reflect.DeepEqual(left.(map[string]any)[key], right.(map[string]any)[key]) {
			changed = true
		}
	}
	changed = changed || !reflect.DeepEqual(
		ineffectiveValues(reflect.ValueOf(p.Inputs.Spec.Template)),
		ineffectiveValues(reflect.ValueOf(r.Spec.Template)))
	return !metadataIdentityEqual(metadata, r.Metadata), changed
}

func computedSnapshot(input, observed reflect.Value) reflect.Value {
	if !observed.IsValid() || input.Type() != observed.Type() {
		return input
	}
	switch input.Kind() {
	case reflect.Pointer:
		if input.IsNil() || observed.IsNil() {
			return input
		}
		out := reflect.New(input.Type().Elem())
		out.Elem().Set(computedSnapshot(input.Elem(), observed.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(input.Type()).Elem()
		out.Set(input)
		for i := range input.NumField() {
			if input.Type().Field(i).Tag.Get("computed") == "true" {
				out.Field(i).Set(observed.Field(i))
			} else {
				out.Field(i).Set(computedSnapshot(input.Field(i), observed.Field(i)))
			}
		}
		return out
	case reflect.Slice:
		if input.IsNil() {
			return input
		}
		out := reflect.MakeSlice(input.Type(), input.Len(), input.Len())
		reflect.Copy(out, input)
		for i := range min(input.Len(), observed.Len()) {
			out.Index(i).Set(computedSnapshot(input.Index(i), observed.Index(i)))
		}
		return out
	}
	return input
}
