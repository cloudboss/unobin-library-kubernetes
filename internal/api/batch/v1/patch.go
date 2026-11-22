package v1

import (
	"reflect"
	"slices"
	"strings"
)

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func mapPatch(path string, before, after *map[string]string) []patchOperation {
	old, desired := map[string]string{}, map[string]string{}
	if before != nil {
		old = *before
	}
	if after != nil {
		desired = *after
	}
	if reflect.DeepEqual(old, desired) || len(old) == 0 && len(desired) == 0 {
		return nil
	}
	if len(old) == 0 {
		return []patchOperation{{Op: "add", Path: path, Value: desired}}
	}
	keys := make([]string, 0, len(old)+len(desired))
	for key := range old {
		keys = append(keys, key)
	}
	for key := range desired {
		if _, present := old[key]; !present {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	operations := []patchOperation{}
	for _, key := range keys {
		next, present := desired[key]
		previous, existed := old[key]
		if present && existed && next == previous {
			continue
		}
		op := "replace"
		if !present {
			op = "remove"
		} else if !existed {
			op = "add"
		}
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operation := patchOperation{Op: op, Path: path + "/" + pointer}
		if present {
			operation.Value = next
		}
		operations = append(operations, operation)
	}
	return operations
}
