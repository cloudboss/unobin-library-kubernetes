package v2

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

func projectHPAMetadata(observed, tracked map[string]string, expressions *[]string) (
	map[string]string, error) {
	var ignored []*regexp.Regexp
	for _, pattern := range hpaValue(expressions) {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		ignored = append(ignored, re)
	}
	out := map[string]string{}
	for key, value := range observed {
		if _, ok := tracked[key]; ok {
			out[key] = value
			continue
		}
		internal := false
		parsed, err := url.Parse("//" + key)
		if err == nil && parsed.Hostname() != "app.kubernetes.io" &&
			parsed.Hostname() != "service.beta.kubernetes.io" {
			internal = strings.HasSuffix(parsed.Hostname(), "kubernetes.io") || strings.Contains(key,
				"deprecated.daemonset.template.generation")
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

type hpaPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func hpaMapPatch(path string, before, after map[string]string) []hpaPatchOperation {
	out := []hpaPatchOperation{}
	if len(before) == 0 {
		if len(after) > 0 {
			out = append(out, hpaPatchOperation{Op: "add", Path: path, Value: after})
		}
		return out
	}
	keys := make([]string, 0, len(before))
	for key := range before {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	pointer := func(key string) string {
		return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	}
	for _, key := range keys {
		if _, ok := after[key]; !ok {
			out = append(out, hpaPatchOperation{Op: "remove", Path: pointer(key)})
		}
	}
	keys = keys[:0]
	for key := range after {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value := after[key]
		previous, ok := before[key]
		if ok && value == previous {
			continue
		}
		op := "add"
		if ok {
			op = "replace"
		}
		out = append(out, hpaPatchOperation{Op: op, Path: pointer(key), Value: value})
	}
	return out
}
