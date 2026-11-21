package v1

import "strings"

type configMapPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func configMapMapPatch(path string, before, after map[string]string) []configMapPatchOperation {
	operations := []configMapPatchOperation{}
	if len(before) == 0 {
		if len(after) > 0 {
			operations = append(operations, configMapPatchOperation{
				Op: "add", Path: path, Value: after,
			})
		}
		return operations
	}
	for key := range before {
		if _, present := after[key]; !present {
			operations = append(operations, configMapPatchOperation{
				Op: "remove", Path: configMapPointer(path, key),
			})
		}
	}
	for key, value := range after {
		old, present := before[key]
		if present && old == value {
			continue
		}
		op := "add"
		if present {
			op = "replace"
		}
		operations = append(operations, configMapPatchOperation{
			Op: op, Path: configMapPointer(path, key), Value: value,
		})
	}
	return operations
}

func configMapPointer(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
