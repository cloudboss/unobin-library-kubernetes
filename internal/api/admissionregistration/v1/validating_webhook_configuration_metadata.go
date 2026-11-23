package v1

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type webhookPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func webhookMetadataPatch(path string, before, after map[string]string) []webhookPatchOperation {
	operations := []webhookPatchOperation{}
	if len(before) == 0 {
		if len(after) > 0 {
			operations = append(operations, webhookPatchOperation{Op: "add", Path: path, Value: after})
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
		operation := webhookPatchOperation{Op: op, Path: pointer}
		if has {
			operation.Value = value
		}
		operations = append(operations, operation)
	}
	return operations
}

func reconcileWebhookMetadata(
	observed, current, priorManaged, desired map[string]string, priorFilters, desiredFilters *[]string,
) (map[string]string, map[string]string) {
	before := projectWebhookMetadata(observed, desiredFilters)
	for key := range priorManaged {
		delete(before, key)
	}
	for key := range desired {
		delete(before, key)
	}
	after := make(map[string]string, len(desired))
	maps.Copy(after, desired)

	previouslyVisible := projectWebhookMetadata(current, priorFilters)
	currentlyVisible := projectWebhookMetadata(current, desiredFilters, priorManaged, desired)
	for key, value := range currentlyVisible {
		_, wasVisible := previouslyVisible[key]
		_, wasManaged := priorManaged[key]
		_, isManaged := desired[key]
		_, wasObserved := observed[key]
		switch {
		case !wasVisible || wasManaged || isManaged:
			before[key] = value
		case !wasObserved:
			// Keep late external keys on both sides to avoid replacing a nonempty live map.
			before[key] = value
			after[key] = value
		}
	}
	return before, after
}

func projectWebhookMetadata(
	observed map[string]string, expressions *[]string, tracked ...map[string]string,
) map[string]string {
	patterns := make([]*regexp.Regexp, 0, len(valueOr(expressions, nil)))
	for _, expression := range valueOr(expressions, nil) {
		pattern, err := regexp.Compile(expression)
		// Native constraints reject malformed desired patterns. Saved inputs must remain readable.
		if err == nil {
			patterns = append(patterns, pattern)
		}
	}
	out := map[string]string{}
	for key, value := range observed {
		if slices.ContainsFunc(tracked, func(values map[string]string) bool {
			_, present := values[key]
			return present
		}) {
			out[key] = value
			continue
		}
		if internalWebhookMetadataKey(key) {
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
	return out
}

func internalWebhookMetadataKey(key string) bool {
	parsed, err := url.Parse("//" + key)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "app.kubernetes.io" || host == "service.beta.kubernetes.io" {
		return false
	}
	return strings.HasSuffix(host, "kubernetes.io") ||
		strings.Contains(key, "deprecated.daemonset.template.generation")
}
