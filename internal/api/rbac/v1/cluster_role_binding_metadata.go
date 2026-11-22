package v1

import (
	"net/url"
	"regexp"
	"strings"
)

func projectClusterBindingMetadata(observed, tracked map[string]string,
	expressions *[]string,
) (map[string]string, error) {
	patterns := make([]*regexp.Regexp, 0, len(roleValue(expressions)))
	for _, expression := range roleValue(expressions) {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pattern)
	}
	out := map[string]string{}
	for key, value := range observed {
		if _, present := tracked[key]; present {
			out[key] = value
			continue
		}
		if internalClusterBindingKey(key) {
			continue
		}
		ignored := false
		for _, pattern := range patterns {
			ignored = ignored || pattern.MatchString(key)
		}
		if !ignored {
			out[key] = value
		}
	}
	return out, nil
}

func internalClusterBindingKey(key string) bool {
	parsed, err := url.Parse("//" + key)
	if err != nil {
		return false
	}
	host := parsed.Host
	if host == "app.kubernetes.io" || host == "service.beta.kubernetes.io" {
		return false
	}
	return strings.HasSuffix(host, "kubernetes.io") ||
		strings.Contains(key, "deprecated.daemonset.template.generation")
}
