package v1

import (
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func jobLookupOutput(obj *batchv1.Job) (*JobDataSourceOutput, error) {
	out := &JobDataSourceOutput{
		Metadata: JobLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec:   &JobLookupSpec{},
		Status: &JobLookupStatus{},
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	if err := readJobLookup(reflect.ValueOf(out.Spec).Elem(), reflect.ValueOf(&obj.Spec).Elem(),
		""); err != nil {
		return nil, err
	}
	s := &obj.Status
	if err := readJobLookup(reflect.ValueOf(out.Status).Elem(), reflect.ValueOf(s).Elem(),
		""); err != nil {
		return nil, err
	}
	return out, nil
}

// readJobLookup maps only the existing typed vocabulary, without resource read normalization.
// Native pointers retain absence; nonpointer native scalars retain their zero values.
func readJobLookup(dst, src reflect.Value, format string) error {
	if !src.IsValid() {
		return nil
	}
	if src.Kind() == reflect.Pointer {
		if src.IsNil() {
			return nil
		}
		src = src.Elem()
	}
	if (src.Kind() == reflect.Map || src.Kind() == reflect.Slice) && src.IsNil() &&
		dst.Kind() == reflect.Pointer {
		return nil
	}
	if dst.Kind() == reflect.Pointer {
		if src.Type() == reflect.TypeFor[metav1.Time]() &&
			src.Interface().(metav1.Time).Time.IsZero() {
			return nil
		}
		dst.Set(reflect.New(dst.Type().Elem()))
		return readJobLookup(dst.Elem(), src, format)
	}
	switch dst.Kind() {
	case reflect.Struct:
		for i := range dst.NumField() {
			field := dst.Type().Field(i)
			name := jsonName(field)
			if name == "-" {
				continue
			}
			value := jobLookupField(src, name)
			if !value.IsValid() {
				continue
			}
			if err := readJobLookup(dst.Field(i), value, field.Tag.Get("format")); err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			}
		}
	case reflect.Slice:
		if src.Kind() != reflect.Slice {
			// A Kubernetes projected source is a single object; the typed vocabulary uses a list.
			dst.Set(reflect.MakeSlice(dst.Type(), 1, 1))
			return readJobLookup(dst.Index(0), src, format)
		}
		dst.Set(reflect.MakeSlice(dst.Type(), src.Len(), src.Len()))
		for i := range src.Len() {
			if err := readJobLookup(dst.Index(i), src.Index(i), format); err != nil {
				return err
			}
		}
	case reflect.Map:
		dst.Set(reflect.MakeMapWithSize(dst.Type(), src.Len()))
		iter := src.MapRange()
		for iter.Next() {
			value := reflect.New(dst.Type().Elem()).Elem()
			if err := readJobLookup(value, iter.Value(), format); err != nil {
				return err
			}
			dst.SetMapIndex(iter.Key().Convert(dst.Type().Key()), value)
		}
	case reflect.String:
		switch {
		case src.Type() == reflect.TypeFor[metav1.Time]():
			value := src.Interface().(metav1.Time)
			dst.SetString(value.UTC().Format(time.RFC3339))
		case src.Kind() == reflect.String:
			dst.SetString(src.String())
		case src.Kind() >= reflect.Int && src.Kind() <= reflect.Int64:
			if format == "mode" {
				dst.SetString(fmt.Sprintf("0%o", src.Int()))
			} else {
				dst.SetString(strconv.FormatInt(src.Int(), 10))
			}
		default:
			if src.CanAddr() {
				if value, ok := src.Addr().Interface().(fmt.Stringer); ok {
					dst.SetString(value.String())
					return nil
				}
			}
			copy := reflect.New(src.Type())
			copy.Elem().Set(src)
			if value, ok := copy.Interface().(fmt.Stringer); ok {
				dst.SetString(value.String())
				return nil
			}
			return fmt.Errorf("cannot convert %s to string", src.Type())
		}
	case reflect.Int64:
		dst.SetInt(src.Int())
	case reflect.Bool:
		dst.SetBool(src.Bool())
	}
	return nil
}

func jobLookupField(src reflect.Value, name string) reflect.Value {
	if src.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	for i := range src.NumField() {
		field := src.Type().Field(i)
		if field.Anonymous {
			if value := jobLookupField(src.Field(i), name); value.IsValid() {
				return value
			}
		}
		nativeName := strings.Split(field.Tag.Get("json"), ",")[0]
		if nativeName == "" {
			nativeName = jsonName(field)
		}
		if nativeName == name {
			return src.Field(i)
		}
	}
	return reflect.Value{}
}
