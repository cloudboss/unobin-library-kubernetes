package v1

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"k8s.io/utils/ptr"
)

type namespacePatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func normalizeNamespaceMetadata(input NamespaceMetadata) NamespaceMetadataOutput {
	annotations, labels := map[string]string{}, map[string]string{}
	maps.Copy(annotations, ptr.Deref(input.Annotations, nil))
	maps.Copy(labels, ptr.Deref(input.Labels, nil))
	return NamespaceMetadataOutput{
		Name: ptr.Deref(input.Name, ""), GenerateName: ptr.Deref(input.GenerateName, ""),
		Annotations: annotations, Labels: labels,
	}
}

func projectNamespaceMetadata(
	observed, tracked map[string]string, expressions *[]string,
) (map[string]string, error) {
	var ignored []*regexp.Regexp
	for _, expression := range ptr.Deref(expressions, nil) {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		ignored = append(ignored, pattern)
	}
	projected := map[string]string{}
	for key, value := range observed {
		if _, present := tracked[key]; present {
			projected[key] = value
			continue
		}
		domain, _, _ := strings.Cut(key, "/")
		internal := domain != "app.kubernetes.io" && domain != "service.beta.kubernetes.io" &&
			(strings.HasSuffix(domain, "kubernetes.io") ||
				strings.Contains(key, "deprecated.daemonset.template.generation"))
		for _, pattern := range ignored {
			internal = internal || pattern.MatchString(key)
		}
		if !internal {
			projected[key] = value
		}
	}
	return projected, nil
}

func namespaceMapPatch(path string, before, after map[string]string) []namespacePatchOperation {
	if len(before) == 0 {
		if len(after) == 0 {
			return nil
		}
		return []namespacePatchOperation{{Op: "add", Path: path, Value: after}}
	}
	keys := make([]string, 0, len(before)+len(after))
	for key := range before {
		keys = append(keys, key)
	}
	for key := range after {
		if _, present := before[key]; !present {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var operations []namespacePatchOperation
	for _, key := range keys {
		value, present := after[key]
		previous, existed := before[key]
		if present && existed && value == previous {
			continue
		}
		op := "replace"
		if !present {
			op = "remove"
		} else if !existed {
			op = "add"
		}
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operation := namespacePatchOperation{Op: op, Path: path + "/" + pointer}
		if present {
			operation.Value = value
		}
		operations = append(operations, operation)
	}
	return operations
}
