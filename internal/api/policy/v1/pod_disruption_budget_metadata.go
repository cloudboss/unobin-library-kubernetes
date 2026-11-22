package v1

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type pdbPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func pdbMapPatch(path string, before, after map[string]string) []pdbPatchOperation {
	operations := []pdbPatchOperation{}
	if len(before) == 0 {
		if len(after) > 0 {
			operations = append(operations, pdbPatchOperation{Op: "add", Path: path, Value: after})
		}
		return operations
	}
	keys := make([]string, 0, len(before)+len(after))
	for key := range before {
		keys = append(keys, key)
	}
	for key := range after {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		old, had := before[key]
		value, has := after[key]
		if had && has && old == value {
			continue
		}
		pointer := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		op := "add"
		if !has {
			op = "remove"
		} else if had {
			op = "replace"
		}
		operation := pdbPatchOperation{Op: op, Path: pointer}
		if has {
			operation.Value = value
		}
		operations = append(operations, operation)
	}
	return operations
}

func projectPDBMetadata(
	observed, tracked map[string]string, expressions *[]string,
) (map[string]string, error) {
	var ignored []*regexp.Regexp
	for _, expression := range pdbValue(expressions) {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		ignored = append(ignored, pattern)
	}
	out := map[string]string{}
	for key, value := range observed {
		if _, present := tracked[key]; present {
			out[key] = value
			continue
		}
		parsed, _ := url.Parse("//" + key)
		internal := strings.Contains(key, "deprecated.daemonset.template.generation")
		if parsed != nil {
			if parsed.Host == "app.kubernetes.io" || parsed.Host == "service.beta.kubernetes.io" {
				internal = false
			} else {
				internal = internal || strings.HasSuffix(parsed.Host, "kubernetes.io")
			}
		}
		for _, pattern := range ignored {
			internal = internal || pattern.MatchString(key)
		}
		if !internal {
			out[key] = value
		}
	}
	return out, nil
}
