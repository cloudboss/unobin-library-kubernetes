package object

import (
	"bytes"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v4/value"
)

func ownedFields(obj *unstructured.Unstructured, manager string) (string, error) {
	fields := fieldpath.NewSet()
	for _, entry := range obj.GetManagedFields() {
		if entry.Manager != manager || entry.Operation != metav1.ManagedFieldsOperationApply ||
			entry.Subresource != "" || entry.FieldsV1 == nil {
			continue
		}
		owned := fieldpath.NewSet()
		if err := owned.FromJSON(bytes.NewReader(entry.FieldsV1.Raw)); err != nil {
			return "", fmt.Errorf("decode Kubernetes field ownership: %w", err)
		}
		owned.Leaves().Iterate(func(path fieldpath.Path) {
			if managedPath(path) {
				fields.Insert(path)
			}
		})
	}
	encoded, err := fields.ToJSON()
	return string(encoded), err
}

func fieldValues(obj *unstructured.Unstructured, fields string) (map[string]any, error) {
	selection := fieldpath.NewSet()
	if err := selection.FromJSON(bytes.NewBufferString(fields)); err != nil {
		return nil, fmt.Errorf("decode saved Kubernetes field selection: %w", err)
	}
	values := map[string]any{}
	var selectionErr error
	selection.Iterate(func(path fieldpath.Path) {
		segments := make([]string, len(path))
		for i, element := range path {
			segment, err := fieldpath.SerializePathElement(element)
			if err != nil {
				selectionErr = err
				return
			}
			segments[i] = segment
		}
		key, err := json.Marshal(segments)
		if err != nil {
			selectionErr = err
			return
		}
		if selected, found := selectValue(value.NewValueInterface(obj.Object), path); found {
			values[string(key)] = selected.Unstructured()
		}
	})
	return values, selectionErr
}

func managedPath(path fieldpath.Path) bool {
	if len(path) == 0 || path[0].FieldName == nil {
		return false
	}
	switch *path[0].FieldName {
	case "apiVersion", "kind", "status":
		return false
	case "metadata":
		if len(path) < 2 || path[1].FieldName == nil {
			return false
		}
		switch *path[1].FieldName {
		case "labels", "annotations", "finalizers", "ownerReferences":
			return true
		}
		return false
	}
	return true
}

func selectValue(current value.Value, path fieldpath.Path) (value.Value, bool) {
	for _, element := range path {
		var found bool
		if element.FieldName != nil && current.IsMap() {
			current, found = current.AsMap().Get(*element.FieldName)
		} else if current.IsList() {
			current, found = selectList(current.AsList(), element)
		}
		if !found {
			return nil, false
		}
	}
	return current, true
}

func selectList(list value.List, element fieldpath.PathElement) (value.Value, bool) {
	if element.Index != nil {
		if index := *element.Index; index >= 0 && index < list.Length() {
			return list.At(index), true
		}
		return nil, false
	}
	for i := 0; i < list.Length(); i++ {
		item := list.At(i)
		if element.Value != nil && value.Equals(item, *element.Value) {
			return item, true
		}
		if element.Key != nil && matchesKey(item, *element.Key) {
			return item, true
		}
	}
	return nil, false
}

func matchesKey(item value.Value, key value.FieldList) bool {
	if !item.IsMap() {
		return false
	}
	for _, field := range key {
		actual, found := item.AsMap().Get(field.Name)
		if !found || !value.Equals(actual, field.Value) {
			return false
		}
	}
	return true
}
