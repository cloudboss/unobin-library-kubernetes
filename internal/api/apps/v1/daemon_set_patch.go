package v1

import (
	"maps"
	"slices"
	"strings"
)

func daemonSetMapPatch(
	path string, before, desired *map[string]string, live map[string]string,
) []patchOperation {
	if before != nil && len(*before) != 0 || len(live) == 0 || desired == nil {
		return mapPatch(path, before, desired)
	}
	operations := []patchOperation{}
	for _, key := range slices.Sorted(maps.Keys(*desired)) {
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operations = append(operations, patchOperation{
			Op: "add", Path: path + "/" + pointer, Value: (*desired)[key],
		})
	}
	return operations
}
